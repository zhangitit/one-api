package controller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/client"
	"github.com/songquanpeng/one-api/common/ctxkey"
	native "github.com/songquanpeng/one-api/relay/adaptor/anthropic"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/relay/apitype"
	"github.com/songquanpeng/one-api/relay/channeltype"
	"github.com/songquanpeng/one-api/relay/meta"
	"github.com/songquanpeng/one-api/relay/relaymode"
)

func TestPrepareNativeMessagesPreservesProviderFields(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", strings.NewReader(`{
		"model":"public-model","max_tokens":0,"system":[{"type":"text","text":"original","cache_control":{"type":"ephemeral"}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}],
		"thinking":{"type":"adaptive"},"future_provider_field":{"keep":true}
	}`))
	m := &meta.Meta{APIType: apitype.Anthropic, ModelMapping: map[string]string{"public-model": "provider-model"}, ForcedSystemPrompt: "forced"}
	request, body, reset, err := prepareMessagesRequest(c, m, &native.Adaptor{})
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err = json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if !reset || raw["model"] != "provider-model" || raw["system"] != "forced" || raw["max_tokens"] != float64(0) {
		t.Fatalf("model/system/budget mismatch: %s", body)
	}
	if raw["thinking"].(map[string]any)["type"] != "adaptive" || raw["future_provider_field"].(map[string]any)["keep"] != true || !strings.Contains(string(body), `"cache_control"`) {
		t.Fatalf("native provider fields were lost: %s", body)
	}
	if request.Model != "provider-model" || m.OriginModelName != "public-model" || m.ActualModelName != "provider-model" {
		t.Fatal("usage model mapping was not preserved")
	}
}

func TestPrepareMessagesBridgesToChatEndpoint(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"public-model","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	m := &meta.Meta{APIType: apitype.OpenAI, ChannelType: channeltype.OpenAICompatible, BaseURL: "https://api.example.com/v1", RequestURLPath: "/v1/messages", ModelMapping: map[string]string{"public-model": "provider-model"}}
	a := &openai.Adaptor{}
	a.Init(m)
	request, body, _, err := prepareMessagesRequest(c, m, a)
	if err != nil {
		t.Fatal(err)
	}
	if m.Mode != relaymode.ChatCompletions || m.RequestURLPath != "/v1/chat/completions" || request.Model != "provider-model" || !m.IsStream {
		t.Fatalf("wrong upstream routing: %+v", m)
	}
	if !strings.Contains(string(body), `"include_usage":true`) {
		t.Fatalf("missing authoritative stream usage request: %s", body)
	}
}

func TestNativeCountTokensUsesProviderWithoutGenerationQuota(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/v1/messages/count_tokens?beta=true" || r.Header.Get("x-api-key") != "upstream-secret" {
			t.Errorf("wrong native request: %s, key %q", r.URL.RequestURI(), r.Header.Get("x-api-key"))
		}
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(data, &body)
		if body["model"] != "provider-model" || body["max_tokens"] != nil || body["system"] != "forced" || body["thinking"] == nil {
			t.Errorf("count request changed: %s", data)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("request-id", "provider-request")
		io.WriteString(w, `{"input_tokens":23,"future_count_field":true}`)
	}))
	defer upstream.Close()
	previousClient := client.HTTPClient
	client.HTTPClient = upstream.Client()
	defer func() { client.HTTPClient = previousClient }()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens?beta=true", strings.NewReader(`{"model":"public-model","messages":[{"role":"user","content":"hello"}],"thinking":{"type":"adaptive"}}`))
	c.Request.Header.Set("Authorization", "Bearer upstream-secret")
	c.Set(ctxkey.Channel, channeltype.Anthropic)
	c.Set(ctxkey.BaseURL, upstream.URL+"/v1")
	c.Set(ctxkey.ModelMapping, map[string]string{"public-model": "provider-model"})
	c.Set(ctxkey.SystemPrompt, "forced")
	// No user, token, database, or quota exists: counting must not consume any.
	if err := RelayMessagesCountTokensHelper(c); err != nil {
		t.Fatalf("count failed: %+v", err)
	}
	if w.Code != http.StatusOK || w.Body.String() != `{"input_tokens":23,"future_count_field":true}` || w.Header().Get("request-id") != "provider-request" {
		t.Fatalf("count response was not preserved: %d %s %v", w.Code, w.Body.String(), w.Header())
	}
}

func TestCountTokensRejectsInexactOpenAIConversion(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(`{"model":"test","messages":[{"role":"user","content":"hello"}]}`))
	c.Set(ctxkey.Channel, channeltype.OpenAICompatible)
	if err := RelayMessagesCountTokensHelper(c); err == nil || err.StatusCode != http.StatusBadRequest || err.Code != "unsupported_token_count" {
		t.Fatalf("inexact count accepted: %+v", err)
	}
}

func TestNativeErrorsArePreservedAndClearedForRetry(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	raw := `{"type":"error","error":{"type":"invalid_request_error","message":"unsupported beta"},"request_id":"upstream-id"}`
	resp := &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(raw)), Header: make(http.Header)}
	resp.Header.Set("retry-after", "5")
	resp.Header.Set("x-should-retry", "false")
	resp.Header.Set("anthropic-ratelimit-unified-status", "allowed")
	captureNativeMessagesError(c, resp)
	got, _ := c.Get("anthropic_upstream_error_body")
	if string(got.([]byte)) != raw || c.GetInt("anthropic_upstream_error_status") != http.StatusBadRequest || c.Writer.Header().Get("Retry-After") != "5" {
		t.Fatal("native upstream error or retry headers were lost")
	}
	parsed := RelayErrorHandler(resp)
	if parsed.StatusCode != http.StatusBadRequest || parsed.Message != "unsupported beta" {
		t.Fatalf("error body not restored for channel handling: %+v", parsed)
	}
	clearNativeMessagesError(c)
	got, _ = c.Get("anthropic_upstream_error_body")
	if len(got.([]byte)) != 0 || c.GetInt("anthropic_upstream_error_status") != 0 || c.Writer.Header().Get("Retry-After") != "" {
		t.Fatal("retry retained old upstream error")
	}
}

func TestMessagesUpstreamRequestKeepsCancellation(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
	previousClient := client.HTTPClient
	client.HTTPClient = &http.Client{}
	defer func() { client.HTTPClient = previousClient }()
	_, err := doMessagesRequest(c, &meta.Meta{APIType: apitype.Anthropic, BaseURL: "http://127.0.0.1:1", RequestURLPath: "/v1/messages"}, &native.Adaptor{}, []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("request cancellation was lost: %v", err)
	}
}

func TestNativePlaintextErrorsAreNotSentAsJSON(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	clearNativeMessagesError(c)
	resp := &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader("upstream is down")), Header: make(http.Header)}
	captureNativeMessagesError(c, resp)
	got, _ := c.Get("anthropic_upstream_error_body")
	if len(got.([]byte)) != 0 || c.GetInt("anthropic_upstream_error_status") != 0 {
		t.Fatal("plaintext upstream error was captured as a native JSON envelope")
	}
	data, _ := io.ReadAll(resp.Body)
	if string(data) != "upstream is down" {
		t.Fatal("upstream error body was not restored")
	}
}
