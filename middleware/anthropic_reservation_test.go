package middleware

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common"
)

func TestAnthropicReservationUsesNativeOutputLimit(t *testing.T) {
	for _, limit := range []string{"0", "128"} {
		body := `{"model":"alias","max_tokens":` + limit + `,"system":[{"type":"text","text":"cached","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"read","input":{"path":"image_url is ordinary text"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":"hello"}]}]}]}`
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		prompt, output, err := prepareZeoReservation(c)
		if err != nil || prompt != int64(len(body))+1024 || (limit == "0" && output != 0) || (limit == "128" && output != 128) {
			t.Fatalf("incorrect native reservation: %d %d %v", prompt, output, err)
		}
		after, _ := common.GetRequestBody(c)
		if string(after) != body {
			t.Fatal("native reservation changed the request")
		}
	}
}

func TestAnthropicReservationRejectsUnboundedContent(t *testing.T) {
	for _, extra := range []string{
		`"max_tokens":null`, `"max_tokens":-1`, `"max_tokens":1.5`, `"max_tokens":"128"`,
		`"max_tokens":1,"tools":[{"type":"web_search_20250305","name":"web_search"}]`,
		`"max_tokens":1,"mcp_servers":[{"url":"https://example.com"}]`,
		`"max_tokens":1,"compaction":{"type":"summarize"}`,
		`"max_tokens":1,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.com/image"}}]}]`,
		`"max_tokens":1,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"document","source":{"type":"url","url":"https://example.com/a.pdf"}}]}]}]`,
	} {
		body := `{"model":"alias","messages":[{"role":"user","content":"hello"}],` + extra + `}`
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		if _, _, err := prepareZeoReservation(c); err == nil {
			t.Fatalf("accepted unbounded or invalid request %s", body)
		}
	}
}

func TestAnthropicCountDoesNotReserveGeneration(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(`{"model":"alias","messages":[{"role":"user","content":"hello"}]}`))
	prompt, output, err := prepareZeoReservation(c)
	if err != nil || prompt != 0 || output != 0 {
		t.Fatalf("count_tokens reserved quota: %d %d %v", prompt, output, err)
	}
}

func TestAnthropicContextClearingKeepsOriginalReservation(t *testing.T) {
	for _, context := range []string{
		`null`, `{}`, `{"edits":[]}`,
		`{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}`,
		`{"edits":[{"type":"clear_thinking_20251015","keep":{"type":"thinking_turns","value":1}}]}`,
		`{"edits":[{"type":"clear_tool_uses_20250919","trigger":{"type":"input_tokens","value":30000},"keep":{"type":"tool_uses","value":3}}]}`,
	} {
		body := `{"model":"alias","max_tokens":128,"messages":[{"role":"user","content":"hello"}],"context_management":` + context + `}`
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		prompt, output, err := prepareZeoReservation(c)
		if err != nil || prompt != int64(len(body))+1024 || output != 128 {
			t.Fatalf("context edit rejected or under-reserved: %d %d %v; context=%s", prompt, output, err, context)
		}
		unchanged, _ := common.GetRequestBody(c)
		if string(unchanged) != body {
			t.Fatal("context edit changed native request during reservation")
		}
	}
}

func TestAnthropicContextManagementRejectsInvalidOrExpandingEdits(t *testing.T) {
	for _, context := range []string{
		`[]`, `true`, `{"edits":"invalid"}`, `{"edits":[null]}`,
		`{"edits":[{"type":"future_edit"}]}`,
		`{"edits":[{"type":"compact_20260112","trigger":{"type":"input_tokens","value":100000}}]}`,
		`{"edits":[{"type":"clear_thinking_20251015","keep":{"type":"thinking_turns","value":-1}}]}`,
	} {
		body := `{"model":"alias","max_tokens":128,"messages":[{"role":"user","content":"hello"}],"context_management":` + context + `}`
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		if _, _, err := prepareZeoReservation(c); err == nil {
			t.Fatalf("accepted invalid or expanding edit %s", context)
		}
	}
}
