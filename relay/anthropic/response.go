package anthropic

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/relay/model"
)

// Native input_tokens excludes both cache buckets, unlike OpenAI prompt_tokens.
type nativeUsage struct {
	Input         *int `json:"input_tokens"`
	Output        *int `json:"output_tokens"`
	CacheCreation *int `json:"cache_creation_input_tokens"`
	CacheRead     *int `json:"cache_read_input_tokens"`
}

func (u *nativeUsage) merge(next nativeUsage) {
	if next.Input != nil {
		u.Input = next.Input
	}
	if next.Output != nil {
		u.Output = next.Output
	}
	if next.CacheCreation != nil {
		u.CacheCreation = next.CacheCreation
	}
	if next.CacheRead != nil {
		u.CacheRead = next.CacheRead
	}
}

func (u nativeUsage) usage() (*model.Usage, error) {
	out := &model.Usage{Estimated: u.Input == nil || u.Output == nil}
	for _, n := range []*int{u.Input, u.Output, u.CacheCreation, u.CacheRead} {
		if n != nil && *n < 0 {
			return nil, fmt.Errorf("upstream returned negative usage")
		}
	}
	if u.Input != nil {
		out.PromptTokens = *u.Input
	}
	if u.CacheCreation != nil {
		out.PromptTokens += *u.CacheCreation
	}
	if u.CacheRead != nil {
		out.PromptTokens += *u.CacheRead
		out.PromptCacheHitTokens = u.CacheRead
	}
	if u.Output != nil {
		out.CompletionTokens = *u.Output
	}
	out.TotalTokens = out.PromptTokens + out.CompletionTokens
	return out, nil
}

// HandleNative preserves the complete native body, including fields introduced
// after this gateway was built, while independently extracting billing usage.
func HandleNative(c *gin.Context, resp *http.Response, modelName string, stream bool) (*model.Usage, error) {
	defer resp.Body.Close()
	if stream {
		return handleNativeStream(c, resp)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var response struct {
		Type  string      `json:"type"`
		Usage nativeUsage `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &response) != nil || response.Type != "message" {
		return nil, fmt.Errorf("upstream did not return an Anthropic message")
	}
	usage, err := response.Usage.usage()
	if err != nil {
		return nil, err
	}
	copyResponseHeaders(c, resp)
	c.Data(resp.StatusCode, "application/json", body)
	return usage, nil
}

func copyResponseHeaders(c *gin.Context, resp *http.Response) {
	// Do not forward length/encoding: streaming or protocol conversion may change
	// them. Keep request IDs, retry hints and provider rate-limit information.
	for key, values := range resp.Header {
		lower := strings.ToLower(key)
		if lower == "request-id" || lower == "x-request-id" || lower == "retry-after" || lower == "x-should-retry" || strings.HasPrefix(lower, "anthropic-ratelimit-") || strings.HasPrefix(lower, "x-ratelimit-") {
			c.Writer.Header()[http.CanonicalHeaderKey(key)] = append([]string(nil), values...)
		}
	}
}

func streamHeaders(c *gin.Context, resp *http.Response) {
	copyResponseHeaders(c, resp)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
}

// readSSE parses event boundaries rather than transport chunks. Its raw frame is
// retained for native passthrough, and multiline data follows the SSE standard.
func readSSE(reader io.Reader, handle func(event string, data []byte, raw []byte) error) error {
	r := bufio.NewReader(reader)
	var frame bytes.Buffer
	var data []string
	var event string
	dispatch := func() error {
		if frame.Len() == 0 {
			return nil
		}
		err := handle(event, []byte(strings.Join(data, "\n")), append([]byte(nil), frame.Bytes()...))
		frame.Reset()
		data = nil
		event = ""
		return err
	}
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			frame.WriteString(line)
			if frame.Len() > 16<<20 {
				return fmt.Errorf("upstream SSE event exceeds 16 MiB")
			}
			value := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if value == "" {
				if e := dispatch(); e != nil {
					return e
				}
			} else if strings.HasPrefix(value, "data:") {
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(value, "data:"), " "))
			} else if strings.HasPrefix(value, "event:") {
				event = strings.TrimSpace(strings.TrimPrefix(value, "event:"))
			}
		}
		if err != nil {
			if err == io.EOF {
				return dispatch()
			}
			return err
		}
	}
}

func handleNativeStream(c *gin.Context, resp *http.Response) (*model.Usage, error) {
	streamHeaders(c, resp)
	var usage nativeUsage
	started, finalOutput, stopped := false, false, false
	err := readSSE(resp.Body, func(event string, data, raw []byte) error {
		if len(data) > 0 {
			if stopped {
				return fmt.Errorf("upstream sent data after message_stop")
			}
			var frame struct {
				Type    string `json:"type"`
				Message *struct {
					Usage nativeUsage `json:"usage"`
				} `json:"message"`
				Usage nativeUsage `json:"usage"`
			}
			if json.Unmarshal(data, &frame) != nil {
				return fmt.Errorf("invalid native SSE JSON")
			}
			if frame.Type == "" {
				frame.Type = event
			}
			switch frame.Type {
			case "message_start":
				if started || frame.Message == nil {
					return fmt.Errorf("invalid message_start")
				}
				started = true
				usage.merge(frame.Message.Usage)
			case "message_delta":
				if !started {
					return fmt.Errorf("message_delta arrived before message_start")
				}
				for _, pair := range [][2]*int{{usage.Input, frame.Usage.Input}, {usage.Output, frame.Usage.Output}, {usage.CacheCreation, frame.Usage.CacheCreation}, {usage.CacheRead, frame.Usage.CacheRead}} {
					if pair[1] != nil && (*pair[1] < 0 || pair[0] != nil && *pair[1] < *pair[0]) {
						return fmt.Errorf("upstream returned decreasing or negative cumulative usage")
					}
				}
				if frame.Usage.Output != nil {
					finalOutput = true
				}
				usage.merge(frame.Usage)
			case "message_stop":
				stopped = true
			case "error":
				if _, err := c.Writer.Write(raw); err != nil {
					return err
				}
				c.Writer.Flush()
				c.Set("anthropic_stream_error_sent", true)
				return fmt.Errorf("upstream reported an Anthropic stream error")
			}
		}
		if _, err := c.Writer.Write(raw); err != nil {
			return err
		}
		c.Writer.Flush()
		return c.Request.Context().Err()
	})
	if err != nil {
		return nil, err
	}
	if !started || !stopped {
		return nil, fmt.Errorf("Anthropic stream ended before message_stop")
	}
	result, err := usage.usage()
	if result != nil && !finalOutput {
		result.Estimated = true
	}
	return result, err
}

type openAIUsage struct {
	Prompt     *int                       `json:"prompt_tokens"`
	Completion *int                       `json:"completion_tokens"`
	Total      *int                       `json:"total_tokens"`
	Details    *model.PromptTokensDetails `json:"prompt_tokens_details"`
	CacheHit   *int                       `json:"prompt_cache_hit_tokens"`
}

func (u *openAIUsage) usage() (*model.Usage, error) {
	if u == nil {
		return &model.Usage{Estimated: true}, nil
	}
	out := &model.Usage{Estimated: u.Prompt == nil || u.Completion == nil, PromptTokensDetails: u.Details, PromptCacheHitTokens: u.CacheHit}
	if u.Prompt != nil {
		out.PromptTokens = *u.Prompt
	}
	if u.Completion != nil {
		out.CompletionTokens = *u.Completion
	}
	out.TotalTokens = out.PromptTokens + out.CompletionTokens
	if out.PromptTokens < 0 || out.CompletionTokens < 0 || (u.Total != nil && *u.Total != out.TotalTokens) {
		return nil, fmt.Errorf("upstream returned inconsistent usage")
	}
	return out, nil
}

func nativeUsageMap(u *model.Usage) map[string]any {
	result := map[string]any{"input_tokens": u.PromptTokens, "output_tokens": u.CompletionTokens}
	if cached, known := u.CachedInput(); known {
		result["input_tokens"] = u.PromptTokens - cached
		result["cache_read_input_tokens"] = cached
		result["cache_creation_input_tokens"] = 0
	}
	return result
}

func stopReason(reason string) (string, error) {
	switch reason {
	case "stop":
		return "end_turn", nil
	case "length":
		return "max_tokens", nil
	case "tool_calls", "function_call":
		return "tool_use", nil
	case "content_filter":
		return "refusal", nil
	default:
		return "", fmt.Errorf("unsupported upstream finish_reason %q", reason)
	}
}

type openAIResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			model.Message
			Refusal string `json:"refusal"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *openAIUsage    `json:"usage"`
	Error json.RawMessage `json:"error"`
}

func HandleOpenAI(c *gin.Context, resp *http.Response, modelName string, stream bool) (*model.Usage, error) {
	defer resp.Body.Close()
	if stream {
		return handleOpenAIStream(c, resp, modelName)
	}
	var response openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	if len(response.Error) > 0 && string(response.Error) != "null" {
		return nil, fmt.Errorf("upstream returned an error instead of a completion")
	}
	if len(response.Choices) != 1 {
		return nil, fmt.Errorf("expected one upstream completion")
	}
	choice := response.Choices[0]
	if choice.Message.ReasoningContent != nil && choice.Message.ReasoningContent != "" {
		return nil, fmt.Errorf("upstream reasoning_content cannot be represented as signed Anthropic thinking blocks; disable upstream thinking or use a native Anthropic channel")
	}
	finish, err := stopReason(choice.FinishReason)
	if err != nil {
		return nil, err
	}
	usage, err := response.Usage.usage()
	if err != nil {
		return nil, err
	}
	content := make([]any, 0)
	if choice.Message.Content != nil {
		switch value := choice.Message.Content.(type) {
		case string:
		case []any:
			for _, part := range value {
				obj, ok := part.(map[string]any)
				if !ok || obj["type"] != "text" {
					return nil, fmt.Errorf("unsupported upstream response content block")
				}
				if _, ok = obj["text"].(string); !ok {
					return nil, fmt.Errorf("invalid upstream text block")
				}
			}
		default:
			return nil, fmt.Errorf("unsupported upstream response content")
		}
	}
	if text := choice.Message.StringContent(); text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	if choice.Message.Refusal != "" {
		content = append(content, map[string]any{"type": "text", "text": choice.Message.Refusal})
		finish = "refusal"
	}
	for _, call := range choice.Message.ToolCalls {
		arguments, ok := call.Function.Arguments.(string)
		if !ok {
			return nil, fmt.Errorf("upstream tool arguments must be JSON text")
		}
		var input map[string]json.RawMessage
		if json.Unmarshal([]byte(arguments), &input) != nil || input == nil {
			return nil, fmt.Errorf("upstream tool arguments must be a JSON object")
		}
		if call.Id == "" || call.Function.Name == "" {
			return nil, fmt.Errorf("upstream tool call is missing id or name")
		}
		content = append(content, map[string]any{"type": "tool_use", "id": call.Id, "name": call.Function.Name, "input": input})
	}
	if len(content) == 0 {
		content = append(content, map[string]any{"type": "text", "text": ""})
	}
	copyResponseHeaders(c, resp)
	c.JSON(resp.StatusCode, map[string]any{"id": messageID(response.ID), "type": "message", "role": "assistant", "model": modelName, "content": content, "stop_reason": finish, "stop_sequence": nil, "usage": nativeUsageMap(usage)})
	return usage, nil
}

func messageID(id string) string {
	if strings.HasPrefix(id, "msg_") {
		return id
	}
	if id == "" {
		id = "gateway"
	}
	return "msg_" + id
}
