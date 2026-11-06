package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/relay"
	"github.com/songquanpeng/one-api/relay/adaptor"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/relay/anthropic"
	"github.com/songquanpeng/one-api/relay/apitype"
	"github.com/songquanpeng/one-api/relay/billing"
	billingratio "github.com/songquanpeng/one-api/relay/billing/ratio"
	"github.com/songquanpeng/one-api/relay/meta"
	"github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/relaymode"
)

// RelayMessagesHelper keeps Anthropic requests native when the selected channel
// speaks Messages, and translates only for OpenAI-compatible channels.
func RelayMessagesHelper(c *gin.Context) *model.ErrorWithStatusCode {
	clearNativeMessagesError(c)
	ctx := c.Request.Context()
	m := meta.GetByContext(c)
	a := relay.GetAdaptor(m.APIType)
	if m.APIType != apitype.Anthropic && m.APIType != apitype.OpenAI {
		return openai.ErrorWrapper(fmt.Errorf("selected channel does not support the Anthropic Messages API"), "unsupported_messages_channel", http.StatusBadRequest)
	}
	a.Init(m)
	textRequest, body, systemPromptReset, err := prepareMessagesRequest(c, m, a)
	if err != nil {
		return openai.ErrorWrapper(err, "invalid_messages_request", http.StatusBadRequest)
	}
	// Include system prompts and tool schemas in the legacy quota estimate as well.
	// ZeoNexus performs its independent conservative reservation in authentication.
	m.PromptTokens = openai.CountTokenText(string(body), m.ActualModelName)
	modelRatio := billingratio.GetModelRatio(m.ActualModelName, m.ChannelType)
	groupRatio := billingratio.GetGroupRatio(m.Group)
	ratio := modelRatio * groupRatio
	preConsumedQuota, bizErr := preConsumeQuota(ctx, textRequest, m.PromptTokens, ratio, m)
	if bizErr != nil {
		return bizErr
	}
	settled := false
	defer func() {
		if !settled {
			billing.ReturnPreConsumedQuota(ctx, preConsumedQuota, m.TokenId)
		}
	}()

	resp, err := doMessagesRequest(c, m, a, body)
	if err != nil {
		return openai.ErrorWrapper(err, "do_request_failed", http.StatusBadGateway)
	}
	if isErrorHappened(m, resp) {
		if m.APIType == apitype.Anthropic && resp != nil {
			captureNativeMessagesError(c, resp)
		}
		bizErr := RelayErrorHandler(resp)
		if bizErr.StatusCode < http.StatusBadRequest {
			bizErr.StatusCode = http.StatusBadGateway
		}
		return bizErr
	}
	copyMessagesResponseHeaders(c, resp)

	var usage *model.Usage
	if m.APIType == apitype.Anthropic {
		usage, err = anthropic.HandleNative(c, resp, m.OriginModelName, m.IsStream)
	} else {
		usage, err = anthropic.HandleOpenAI(c, resp, m.OriginModelName, m.IsStream)
	}
	if err != nil {
		return openai.ErrorWrapper(err, "invalid_upstream_response", http.StatusBadGateway)
	}
	// A completed HTTP response without provider usage cannot settle a reservation.
	// Authentication leaves such ZeoNexus requests available for reconciliation.
	if usage == nil || usage.Estimated || ctx.Err() != nil {
		return nil
	}
	postConsumeQuota(c, usage, m, textRequest, ratio, preConsumedQuota, modelRatio, groupRatio, systemPromptReset)
	settled = true
	return nil
}

// Token counting is a native provider operation. It authorizes and routes like a
// Messages request, but does not run generation or consume a generation quota.
func RelayMessagesCountTokensHelper(c *gin.Context) *model.ErrorWithStatusCode {
	clearNativeMessagesError(c)
	m := meta.GetByContext(c)
	if m.APIType != apitype.Anthropic {
		return openai.ErrorWrapper(fmt.Errorf("token counting requires an Anthropic channel; exact Anthropic token counts are unavailable for the selected upstream"), "unsupported_token_count", http.StatusBadRequest)
	}
	body, err := common.GetRequestBody(c)
	if err != nil {
		return openai.ErrorWrapper(err, "invalid_messages_request", http.StatusBadRequest)
	}
	var raw map[string]json.RawMessage
	if err = json.Unmarshal(body, &raw); err != nil || raw == nil {
		return openai.ErrorWrapper(fmt.Errorf("request body must be a JSON object"), "invalid_messages_request", http.StatusBadRequest)
	}
	originalMaxTokens, hadMaxTokens := raw["max_tokens"]
	// Reuse the same structural validation without requiring a generation budget.
	raw["max_tokens"] = json.RawMessage("0")
	validationBody, err := json.Marshal(raw)
	if err != nil {
		return openai.ErrorWrapper(err, "invalid_messages_request", http.StatusBadRequest)
	}
	request, err := anthropic.ParseRequest(validationBody)
	if err != nil {
		return openai.ErrorWrapper(err, "invalid_messages_request", http.StatusBadRequest)
	}
	m.OriginModelName = request.Model
	m.ActualModelName, _ = getMappedModelName(request.Model, m.ModelMapping)
	request.SetModel(m.ActualModelName)
	if m.ForcedSystemPrompt != "" {
		request.SetSystem(m.ForcedSystemPrompt)
	}
	validationBody, err = request.Body()
	if err != nil {
		return openai.ErrorWrapper(err, "invalid_messages_request", http.StatusBadRequest)
	}
	if err = json.Unmarshal(validationBody, &raw); err != nil {
		return openai.ErrorWrapper(err, "invalid_messages_request", http.StatusBadRequest)
	}
	if hadMaxTokens {
		raw["max_tokens"] = originalMaxTokens
	} else {
		delete(raw, "max_tokens")
	}
	body, err = json.Marshal(raw)
	if err != nil {
		return openai.ErrorWrapper(err, "invalid_messages_request", http.StatusBadRequest)
	}
	a := relay.GetAdaptor(m.APIType)
	a.Init(m)
	resp, err := doMessagesRequest(c, m, a, body)
	if err != nil {
		return openai.ErrorWrapper(err, "do_request_failed", http.StatusBadGateway)
	}
	if resp.StatusCode != http.StatusOK {
		captureNativeMessagesError(c, resp)
		return RelayErrorHandler(resp)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(responseBody) > 1<<20 {
		return openai.ErrorWrapper(fmt.Errorf("cannot read token count response"), "invalid_upstream_response", http.StatusBadGateway)
	}
	var result struct {
		InputTokens *int `json:"input_tokens"`
	}
	if json.Unmarshal(responseBody, &result) != nil || result.InputTokens == nil || *result.InputTokens < 0 {
		return openai.ErrorWrapper(fmt.Errorf("upstream returned an invalid token count"), "invalid_upstream_response", http.StatusBadGateway)
	}
	copyMessagesResponseHeaders(c, resp)
	c.Data(http.StatusOK, "application/json", responseBody)
	return nil
}

func clearNativeMessagesError(c *gin.Context) {
	c.Set("anthropic_upstream_error_body", []byte(nil))
	c.Set("anthropic_upstream_error_status", 0)
	for key := range c.Writer.Header() {
		if isMessagesResponseHeader(key) {
			c.Writer.Header().Del(key)
		}
	}
}

func captureNativeMessagesError(c *gin.Context, resp *http.Response) {
	copyMessagesResponseHeaders(c, resp)
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	var envelope struct {
		Type  string `json:"type"`
		Error *struct {
			Type    string  `json:"type"`
			Message *string `json:"message"`
		} `json:"error"`
	}
	if err == nil && json.Unmarshal(body, &envelope) == nil && envelope.Type == "error" && envelope.Error != nil && envelope.Error.Type != "" && envelope.Error.Message != nil {
		c.Set("anthropic_upstream_error_body", body)
		c.Set("anthropic_upstream_error_status", resp.StatusCode)
	}
}

func copyMessagesResponseHeaders(c *gin.Context, resp *http.Response) {
	for key, values := range resp.Header {
		if isMessagesResponseHeader(key) {
			c.Writer.Header()[key] = append([]string(nil), values...)
		}
	}
}

func isMessagesResponseHeader(key string) bool {
	name := strings.ToLower(key)
	return name == "request-id" || name == "x-request-id" || name == "retry-after" || name == "x-should-retry" || strings.HasPrefix(name, "anthropic-ratelimit-") || strings.HasPrefix(name, "x-ratelimit-")
}

func prepareMessagesRequest(c *gin.Context, m *meta.Meta, a adaptor.Adaptor) (*model.GeneralOpenAIRequest, []byte, bool, error) {
	body, err := common.GetRequestBody(c)
	if err != nil {
		return nil, nil, false, fmt.Errorf("cannot read request body: %w", err)
	}
	request, err := anthropic.ParseRequest(body)
	if err != nil {
		return nil, nil, false, err
	}
	m.OriginModelName = request.Model
	m.ActualModelName, _ = getMappedModelName(request.Model, m.ModelMapping)
	m.IsStream = request.Stream
	request.SetModel(m.ActualModelName)
	systemPromptReset := m.ForcedSystemPrompt != ""
	if systemPromptReset {
		request.SetSystem(m.ForcedSystemPrompt)
	}
	textRequest := &model.GeneralOpenAIRequest{Model: m.ActualModelName, MaxTokens: request.MaxTokens, Stream: request.Stream}
	if m.APIType == apitype.Anthropic {
		body, err = request.Body()
		return textRequest, body, systemPromptReset, err
	}
	textRequest, err = request.ToOpenAI()
	if err != nil {
		return nil, nil, false, err
	}
	m.Mode = relaymode.ChatCompletions
	m.RequestURLPath = "/v1/chat/completions"
	converted, err := a.ConvertRequest(c, m.Mode, textRequest)
	if err != nil {
		return nil, nil, false, err
	}
	body, err = json.Marshal(converted)
	return textRequest, body, systemPromptReset, err
}

func doMessagesRequest(c *gin.Context, m *meta.Meta, a adaptor.Adaptor, body []byte) (*http.Response, error) {
	requestURL, err := a.GetRequestURL(m)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err = a.SetupRequestHeader(c, request, m); err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	return adaptor.DoRequest(c, request)
}
