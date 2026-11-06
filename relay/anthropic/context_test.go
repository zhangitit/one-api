package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestContextManagementValidation(t *testing.T) {
	for _, config := range []string{
		``, `null`, `{}`, `{"edits":null}`, `{"edits":[]}`,
		`{"edits":[{"type":"clear_thinking_20251015"}]}`,
		`{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}`,
		`{"edits":[{"type":"clear_thinking_20251015","keep":{"type":"all"}}]}`,
		`{"edits":[{"type":"clear_thinking_20251015","keep":{"type":"thinking_turns","value":2}}]}`,
		`{"edits":[{"type":"clear_tool_uses_20250919"}]}`,
		`{"edits":[{"type":"clear_tool_uses_20250919","trigger":{"type":"tool_uses","value":0},"keep":{"type":"tool_uses","value":0},"clear_tool_inputs":["read"],"exclude_tools":null,"clear_at_least":null}]}`,
		`{"edits":[{"type":"clear_tool_uses_20250919","trigger":{"type":"input_tokens","value":1000},"clear_tool_inputs":true,"exclude_tools":["read"],"clear_at_least":{"type":"input_tokens","value":0}}]}`,
	} {
		if err := ValidateContextManagement(json.RawMessage(config)); err != nil {
			t.Fatalf("rejected supported config %s: %v", config, err)
		}
	}
	for _, config := range []string{
		`[]`, `"invalid"`, `{"edits":"invalid"}`, `{"edits":[null]}`,
		`{"edits":[{"type":"unknown"}]}`, `{"edits":[{"type":"compact_20260112"}]}`,
		`{"edits":[{"type":"clear_thinking_20251015","keep":null}]}`,
		`{"edits":[{"type":"clear_thinking_20251015","keep":{"type":"thinking_turns","value":0}}]}`,
		`{"edits":[{"type":"clear_tool_uses_20250919","keep":{"type":"tool_uses","value":-1}}]}`,
		`{"edits":[{"type":"clear_tool_uses_20250919","trigger":{"type":"tool_uses","value":1.5}}]}`,
		`{"edits":[{"type":"clear_tool_uses_20250919","trigger":{"type":"tool_uses","value":null}}]}`,
		`{"edits":[{"type":"clear_tool_uses_20250919","clear_tool_inputs":"read"}]}`,
		`{"edits":[{"type":"clear_tool_uses_20250919","exclude_tools":[1]}]}`,
		`{"edits":[{"type":"clear_tool_uses_20250919"},{"type":"clear_thinking_20251015"}]}`,
	} {
		if err := ValidateContextManagement(json.RawMessage(config)); err == nil || !strings.Contains(err.Error(), "context_management") {
			t.Fatalf("accepted invalid config %s: %v", config, err)
		}
	}
}

func contextRequest(t *testing.T, config string, messages string) *Request {
	t.Helper()
	body := `{"model":"alias","max_tokens":64,"messages":` + messages
	if config != "" {
		body += `,"context_management":` + config
	}
	r, err := ParseRequest([]byte(body + `}`))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestContextManagementNoOpsAndNativePreservation(t *testing.T) {
	for _, config := range []string{`null`, `{}`, `{"edits":null}`, `{"edits":[]}`, `{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}`, `{"edits":[{"type":"clear_thinking_20251015","keep":{"type":"all"}}]}`} {
		r := contextRequest(t, config, `[{"role":"user","content":"hello"}]`)
		before, _ := r.Body()
		converted, err := r.ToOpenAI()
		if err != nil {
			t.Fatal(err)
		}
		after, _ := r.Body()
		encoded, _ := json.Marshal(converted)
		if string(before) != string(after) || !strings.Contains(string(after), `"context_management":`+config) || strings.Contains(string(encoded), "context_management") || converted.Messages[0].Content != "hello" {
			t.Fatalf("context config was dropped or history changed: %s => %s", after, encoded)
		}
	}
}

func TestThinkingContextEditsRespectToolContinuationTurns(t *testing.T) {
	messages := `[
		{"role":"user","content":"first task"},
		{"role":"assistant","content":[{"type":"thinking","thinking":"old thought","signature":"signed"},{"type":"tool_use","id":"t1","name":"read","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"result"}]},
		{"role":"assistant","content":[{"type":"thinking","thinking":"same turn","signature":"signed"},{"type":"text","text":"first answer"}]},
		{"role":"user","content":"new task"}
	]`
	r := contextRequest(t, `{"edits":[{"type":"clear_thinking_20251015"}]}`, messages)
	before, _ := r.Body()
	converted, err := r.ToOpenAI()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(converted)
	after, _ := r.Body()
	if strings.Contains(string(encoded), "old thought") || strings.Contains(string(encoded), "same turn") || len(converted.Messages) != 5 || len(converted.Messages[1].ToolCalls) != 1 || converted.Messages[2].ToolCallId != "t1" || string(before) != string(after) {
		t.Fatalf("thinking pruning changed visible conversation: %s", encoded)
	}
	// A trailing tool result continues the first assistant turn. keep:1 must
	// retain that thinking, which a portable OpenAI bridge cannot represent.
	continuation := `[{"role":"user","content":"first task"},{"role":"assistant","content":[{"type":"thinking","thinking":"retained","signature":"signed"},{"type":"tool_use","id":"t1","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"result"}]}]`
	for _, config := range []string{`{"edits":[{"type":"clear_thinking_20251015"}]}`, `{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}`} {
		if _, err := contextRequest(t, config, continuation).ToOpenAI(); err == nil {
			t.Fatal("retained thinking was silently dropped")
		}
	}
	// Positive N counts turns without thinking and the new turn in progress.
	if _, err := contextRequest(t, `{"edits":[{"type":"clear_thinking_20251015","keep":{"type":"thinking_turns","value":2}}]}`, messages).ToOpenAI(); err == nil {
		t.Fatal("keep:2 removed thinking from one of the two most recent turns")
	}
}

func toolContextMessages() string {
	var parts []string
	parts = append(parts, `{"role":"user","content":"inspect files"}`)
	for i, name := range []string{"protected", "read", "recent"} {
		parts = append(parts,
			fmt.Sprintf(`{"role":"assistant","content":[{"type":"tool_use","id":"t%d","name":%q,"input":{"path":"important-file.go"}}]}`, i, name),
			fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t%d","content":"long result with important original information"}]}`, i))
	}
	return `[` + strings.Join(parts, ",") + `]`
}

func TestToolContextEditsPreservePairingKeepAndExclusions(t *testing.T) {
	config := `{"edits":[{"type":"clear_tool_uses_20250919","trigger":{"type":"tool_uses","value":2},"keep":{"type":"tool_uses","value":1},"exclude_tools":["protected"],"clear_tool_inputs":["read"]}]}`
	r := contextRequest(t, config, toolContextMessages())
	before, _ := r.Body()
	converted, err := r.ToOpenAI()
	if err != nil {
		t.Fatal(err)
	}
	if len(converted.Messages) != 7 || converted.Messages[2].Content != "long result with important original information" || converted.Messages[4].Content != "[tool result cleared]" || converted.Messages[6].Content != "long result with important original information" {
		t.Fatalf("wrong tool results cleared: %+v", converted.Messages)
	}
	if converted.Messages[3].ToolCalls[0].Function.Arguments != "{}" || converted.Messages[4].ToolCallId != "t1" || converted.Messages[3].ToolCalls[0].Id != "t1" || converted.Messages[1].ToolCalls[0].Function.Arguments == "{}" || converted.Messages[5].ToolCalls[0].Function.Arguments == "{}" {
		t.Fatal("tool identities or excluded/recent inputs changed")
	}
	after, _ := r.Body()
	if string(before) != string(after) {
		t.Fatal("local edits changed native request")
	}
}

func TestTokenContextEditsAreNeverGuessed(t *testing.T) {
	for _, config := range []string{
		`{"edits":[{"type":"clear_tool_uses_20250919","keep":{"type":"tool_uses","value":0}}]}`,
		`{"edits":[{"type":"clear_tool_uses_20250919","trigger":{"type":"input_tokens","value":100000},"keep":{"type":"tool_uses","value":0}}]}`,
		`{"edits":[{"type":"clear_tool_uses_20250919","trigger":{"type":"tool_uses","value":3},"keep":{"type":"tool_uses","value":0}}]}`,
	} {
		converted, err := contextRequest(t, config, toolContextMessages()).ToOpenAI()
		if err != nil || converted.Messages[2].Content != "long result with important original information" {
			t.Fatalf("safe below-trigger no-op rejected: %s %v", config, err)
		}
	}
	for _, config := range []string{
		`{"edits":[{"type":"clear_tool_uses_20250919","trigger":{"type":"input_tokens","value":1},"keep":{"type":"tool_uses","value":0}}]}`,
		`{"edits":[{"type":"clear_tool_uses_20250919","trigger":{"type":"tool_uses","value":0},"keep":{"type":"tool_uses","value":0},"clear_at_least":{"type":"input_tokens","value":1}}]}`,
	} {
		if _, err := contextRequest(t, config, toolContextMessages()).ToOpenAI(); err == nil || !strings.Contains(err.Error(), "context_management: Extra inputs are not permitted") {
			t.Fatalf("ambiguous token rule silently ignored: %s %v", config, err)
		}
	}
	// No historical result could be cleared, so even a token-triggered rule is
	// unconditionally a no-op and does not require a provider count.
	if _, err := contextRequest(t, `{"edits":[{"type":"clear_tool_uses_20250919","trigger":{"type":"input_tokens","value":0},"keep":{"type":"tool_uses","value":0}}]}`, `[{"role":"user","content":"hello"}]`).ToOpenAI(); err != nil {
		t.Fatal(err)
	}
}

func TestContextClearingDoesNotExpandShortToolContent(t *testing.T) {
	r := contextRequest(t, `{"edits":[{"type":"clear_tool_uses_20250919","trigger":{"type":"tool_uses","value":0},"keep":{"type":"tool_uses","value":0}}]}`, `[{"role":"user","content":"task"},{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"x"}]}]`)
	messages, err := r.contextMessages()
	if err != nil {
		t.Fatal(err)
	}
	var blocks []map[string]json.RawMessage
	_ = json.Unmarshal(messages[2].Content, &blocks)
	if string(blocks[0]["content"]) != `""` {
		t.Fatal("short tool result replaced with larger marker")
	}
}

func TestBridgeDoesNotSilentlyDiscardTopLevelCompaction(t *testing.T) {
	r, err := ParseRequest([]byte(`{"model":"alias","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"compaction":{"trigger":{"type":"input_tokens","value":100000}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.ToOpenAI(); err == nil || !strings.Contains(err.Error(), "compaction requires a native Anthropic channel") {
		t.Fatalf("compaction was silently discarded: %v", err)
	}
}
