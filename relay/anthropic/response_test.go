package anthropic

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func responseContext(body string) (*gin.Context, *httptest.ResponseRecorder, *http.Response) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
	return c, w, resp
}

func TestNativeJSONUsageAndPreservation(t *testing.T) {
	body := `{"id":"msg_1","type":"message","model":"claude","role":"assistant","content":[{"type":"thinking","thinking":"plan","signature":"signed"},{"type":"text","text":"done","citations":[]}],"usage":{"input_tokens":3,"output_tokens":2,"cache_creation_input_tokens":5,"cache_read_input_tokens":7},"future_field":true}`
	c, w, resp := responseContext(body)
	u, err := HandleNative(c, resp, "claude", false)
	if err != nil {
		t.Fatal(err)
	}
	if w.Body.String() != body || u.PromptTokens != 15 || u.CompletionTokens != 2 || u.Estimated {
		t.Fatalf("incorrect native response/usage: %s %+v", w.Body.String(), u)
	}
	if cached, known := u.CachedInput(); !known || cached != 7 {
		t.Fatal("cache usage lost")
	}
}

func TestNativeStreamCumulativeUsageAndTruncation(t *testing.T) {
	body := "event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":0,\"cache_read_input_tokens\":7}}}\r\n\r\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":4,\"output_tokens\":2,\"cache_read_input_tokens\":8}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	c, w, resp := responseContext(body)
	u, err := HandleNative(c, resp, "claude", true)
	if err != nil {
		t.Fatal(err)
	}
	if w.Body.String() != body || u.PromptTokens != 12 || u.CompletionTokens != 5 {
		t.Fatalf("cumulative usage added instead of replaced: %+v", u)
	}
	c, _, resp = responseContext(strings.TrimSuffix(body, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	if _, err = HandleNative(c, resp, "claude", true); err == nil {
		t.Fatal("truncated native stream accepted")
	}
	bad := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n"
	c, w, resp = responseContext(bad)
	if _, err = HandleNative(c, resp, "claude", true); err == nil || w.Body.String() != bad || !c.GetBool("anthropic_stream_error_sent") {
		t.Fatal("native stream error was lost")
	}
}

func TestOpenAIJSONToolConversion(t *testing.T) {
	body := `{"id":"chat_1","choices":[{"index":0,"message":{"role":"assistant","content":"checking","tool_calls":[{"id":"tool1","type":"function","function":{"name":"lookup","arguments":"{\"x\":1}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13,"prompt_tokens_details":{"cached_tokens":4}}}`
	c, w, resp := responseContext(body)
	u, err := HandleOpenAI(c, resp, "alias", false)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal(w.Body.String())
	}
	if result["stop_reason"] != "tool_use" || result["model"] != "alias" || len(result["content"].([]any)) != 2 || u.TotalTokens != 13 {
		t.Fatal(w.Body.String())
	}
	native := result["usage"].(map[string]any)
	if native["input_tokens"] != float64(6) || native["cache_read_input_tokens"] != float64(4) {
		t.Fatal(native)
	}
}

func TestOpenAIStreamFragmentedTools(t *testing.T) {
	chunks := []string{
		`{"id":"c1","choices":[{"index":0,"delta":{"content":"checking"}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"t1","function":{"name":"look"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"up","arguments":"{\"q\":"}},{"index":1,"id":"t2","function":{"name":"other","arguments":"{}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"value\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":6,"total_tokens":16}}`,
	}
	var body strings.Builder
	for _, chunk := range chunks {
		fmt.Fprintf(&body, "data: %s\n\n", chunk)
	}
	body.WriteString("data: [DONE]\n\n")
	c, w, resp := responseContext(body.String())
	u, err := HandleOpenAI(c, resp, "alias", true)
	if err != nil {
		t.Fatal(err)
	}
	output := w.Body.String()
	if strings.Contains(output, "[DONE]") || !strings.Contains(output, `"name":"lookup"`) || u.TotalTokens != 16 {
		t.Fatal(output)
	}
	var types []string
	arguments := map[int]string{}
	starts := map[int]bool{}
	stops := map[int]bool{}
	err = readSSE(strings.NewReader(output), func(event string, data, raw []byte) error {
		var frame struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
			Delta struct {
				Partial string `json:"partial_json"`
			} `json:"delta"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			return err
		}
		types = append(types, frame.Type)
		switch frame.Type {
		case "content_block_start":
			starts[frame.Index] = true
		case "content_block_delta":
			arguments[frame.Index] += frame.Delta.Partial
		case "content_block_stop":
			stops[frame.Index] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if types[0] != "message_start" || types[len(types)-1] != "message_stop" || len(starts) != 3 || len(stops) != 3 || arguments[1] != `{"q":"value"}` || arguments[2] != `{}` {
		t.Fatalf("invalid native event sequence: %s", output)
	}
	c, _, resp = responseContext(strings.TrimSuffix(body.String(), "data: [DONE]\n\n"))
	if _, err = HandleOpenAI(c, resp, "alias", true); err == nil {
		t.Fatal("truncated OpenAI stream accepted")
	}
}

func TestMissingUsageRemainsEstimated(t *testing.T) {
	for _, native := range []bool{false, true} {
		body := `{"id":"c1","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
		if native {
			body = `{"type":"message","content":[{"type":"text","text":"ok"}]}`
		}
		c, _, resp := responseContext(body)
		var estimated bool
		if native {
			u, err := HandleNative(c, resp, "m", false)
			if err != nil {
				t.Fatal(err)
			}
			estimated = u.Estimated
		} else {
			u, err := HandleOpenAI(c, resp, "m", false)
			if err != nil {
				t.Fatal(err)
			}
			estimated = u.Estimated
		}
		if !estimated {
			t.Fatal("missing provider usage became authoritative")
		}
	}
}

func TestNativeStreamRequiresFinalUsage(t *testing.T) {
	start := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n"
	stop := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	c, _, resp := responseContext(start + "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" + stop)
	u, err := HandleNative(c, resp, "m", true)
	if err != nil {
		t.Fatal(err)
	}
	if !u.Estimated {
		t.Fatal("message_start output usage was treated as final")
	}
	for _, suffix := range []string{
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":2,\"output_tokens\":3}}\n\n" + stop,
		stop + "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3}}\n\n",
	} {
		c, _, resp = responseContext(start + suffix)
		if _, err = HandleNative(c, resp, "m", true); err == nil {
			t.Fatal("invalid cumulative stream accepted")
		}
	}
}

func TestOpenAIReasoningIsNotSilentlyDropped(t *testing.T) {
	for _, stream := range []bool{false, true} {
		body := `{"choices":[{"message":{"role":"assistant","content":"answer","reasoning_content":"private reasoning"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`
		if stream {
			body = "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"reasoning\"}}]}\n\ndata: [DONE]\n\n"
		}
		c, _, resp := responseContext(body)
		if _, err := HandleOpenAI(c, resp, "m", stream); err == nil {
			t.Fatal("reasoning was silently removed")
		}
	}
}
