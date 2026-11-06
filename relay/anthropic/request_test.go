package anthropic

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeRequestPreservesExtendedFields(t *testing.T) {
	body := `{"model":"alias","max_tokens":0,"system":[{"type":"text","text":"system","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"document","source":{"type":"url","url":"https://example.com/a.pdf"}}]}],"thinking":{"type":"adaptive"},"new_field":{"a":1}}`
	r, err := ParseRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	r.SetModel("claude-mapped")
	encoded, err := r.Body()
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]json.RawMessage
	_ = json.Unmarshal(encoded, &value)
	for _, key := range []string{"thinking", "new_field", "system", "messages"} {
		if len(value[key]) == 0 {
			t.Fatalf("lost native field %s: %s", key, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"model":"claude-mapped"`) {
		t.Fatal(string(encoded))
	}
	if _, err = r.ToOpenAI(); err == nil {
		t.Fatal("nonportable request was silently converted")
	}
}

func TestRequestBridgeToolsAndResults(t *testing.T) {
	body := `{"model":"qwen","max_tokens":100,"stream":true,"system":[{"type":"text","text":"first"},{"type":"text","text":"second"}],"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"text","text":"checking"},{"type":"tool_use","id":"t1","name":"lookup","input":{"x":1}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"result"}]},{"type":"text","text":"continue"}]}],"tools":[{"name":"lookup","input_schema":{"type":"object","additionalProperties":false,"properties":{"n":{"const":9007199254740993}}}}],"tool_choice":{"type":"any","disable_parallel_tool_use":true},"stop_sequences":["END"]}`
	r, err := ParseRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	o, err := r.ToOpenAI()
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Messages) != 5 || o.Messages[0].Content != "first\nsecond" || o.Messages[2].ToolCalls[0].Id != "t1" || o.Messages[3].Role != "tool" || o.Messages[3].Content != "result" || o.Messages[4].Content != "continue" {
		t.Fatalf("incorrect message conversion: %#v", o.Messages)
	}
	if o.ToolChoice != "required" || o.ParallelTooCalls == nil || *o.ParallelTooCalls || o.StreamOptions == nil || !o.StreamOptions.IncludeUsage {
		t.Fatal("incorrect tool or stream settings")
	}
	encoded, _ := json.Marshal(o)
	if !strings.Contains(string(encoded), `9007199254740993`) || !strings.Contains(string(encoded), `"additionalProperties":false`) {
		t.Fatalf("tool schema was corrupted: %s", encoded)
	}
}

func TestRequestBridgeImagesAndErrors(t *testing.T) {
	r, err := ParseRequest([]byte(`{"model":"m","max_tokens":5,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	o, err := r.ToOpenAI()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(o)
	if !strings.Contains(string(encoded), "data:image/png;base64,aGVsbG8=") {
		t.Fatal(string(encoded))
	}
	for _, body := range []string{`null`, `[]`, `{"model":"m","messages":[],"max_tokens":1}`, `{"model":"m","max_tokens":-1,"messages":[{"role":"user","content":"x"}]}`, `{"model":"m","max_tokens":null,"messages":[{"role":"user","content":"x"}]}`, `{"model":"m","max_tokens":1,"stream":null,"messages":[{"role":"user","content":"x"}]}`} {
		if _, err := ParseRequest([]byte(body)); err == nil {
			t.Fatalf("accepted malformed request %s", body)
		}
	}
}
