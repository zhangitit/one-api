package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const (
	clearThinking = "clear_thinking_20251015"
	clearTools    = "clear_tool_uses_20250919"
)

type contextEdit struct {
	kind            string
	keep            int64
	keepAll         bool
	triggerType     string
	trigger         int64
	clearAtLeast    int64
	exclude         map[string]bool
	clearInputs     bool
	clearInputNames map[string]bool
}

// ValidateContextManagement accepts edits that only remove existing input.
// The original, unedited request remains the gateway's conservative budget.
// Compaction generates additional model output and needs separate accounting.
func ValidateContextManagement(raw json.RawMessage) error {
	_, err := parseContextManagement(raw)
	return err
}

func isJSONNull(raw json.RawMessage) bool {
	return len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func unsupportedContext(reason string) error {
	// Claude Code uses this provider-compatible rejection to retry without an
	// unsupported capability. Keep the field name and prefix stable.
	return fmt.Errorf("context_management: Extra inputs are not permitted: %s", reason)
}

func parseContextManagement(raw json.RawMessage) ([]contextEdit, error) {
	if isJSONNull(raw) {
		return nil, nil
	}
	var config map[string]json.RawMessage
	if json.Unmarshal(raw, &config) != nil || config == nil {
		return nil, fmt.Errorf("context_management must be an object or null")
	}
	for key := range config {
		if key != "edits" {
			return nil, unsupportedContext("unsupported option " + key)
		}
	}
	if isJSONNull(config["edits"]) {
		return nil, nil
	}
	var edits []map[string]json.RawMessage
	if json.Unmarshal(config["edits"], &edits) != nil {
		return nil, fmt.Errorf("context_management.edits must be an array")
	}
	parsed := make([]contextEdit, 0, len(edits))
	sawTools := false
	for _, fields := range edits {
		var kind string
		if json.Unmarshal(fields["type"], &kind) != nil || kind == "" {
			return nil, fmt.Errorf("context_management.edits require a type")
		}
		edit := contextEdit{kind: kind}
		switch kind {
		case clearThinking:
			if sawTools {
				return nil, fmt.Errorf("context_management thinking edits must precede tool edits")
			}
			if err := contextFields(fields, "type", "keep"); err != nil {
				return nil, err
			}
			edit.keep = 1
			if value, exists := fields["keep"]; exists {
				var all string
				if json.Unmarshal(value, &all) == nil && all == "all" {
					edit.keepAll = true
				} else {
					var keep map[string]json.RawMessage
					if json.Unmarshal(value, &keep) != nil || keep == nil {
						return nil, fmt.Errorf("context_management thinking keep must be all or a thinking_turns object")
					}
					var typ string
					_ = json.Unmarshal(keep["type"], &typ)
					if typ == "all" {
						if err := contextFields(keep, "type"); err != nil {
							return nil, err
						}
						edit.keepAll = true
					} else {
						var err error
						edit.keep, err = contextCount(value, "thinking_turns", true)
						if err != nil {
							return nil, err
						}
					}
				}
			}
		case clearTools:
			sawTools = true
			if err := contextFields(fields, "type", "trigger", "keep", "clear_at_least", "exclude_tools", "clear_tool_inputs"); err != nil {
				return nil, err
			}
			edit.keep, edit.triggerType, edit.trigger = 3, "input_tokens", 100000
			if value, exists := fields["keep"]; exists {
				var err error
				edit.keep, err = contextCount(value, "tool_uses", false)
				if err != nil {
					return nil, err
				}
			}
			if value, exists := fields["trigger"]; exists {
				var trigger struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(value, &trigger) != nil || (trigger.Type != "input_tokens" && trigger.Type != "tool_uses") {
					return nil, fmt.Errorf("context_management tool trigger must count input_tokens or tool_uses")
				}
				edit.triggerType = trigger.Type
				var err error
				edit.trigger, err = contextCount(value, trigger.Type, false)
				if err != nil {
					return nil, err
				}
			}
			if value := fields["clear_at_least"]; !isJSONNull(value) {
				var err error
				edit.clearAtLeast, err = contextCount(value, "input_tokens", false)
				if err != nil {
					return nil, err
				}
			}
			var err error
			edit.exclude, err = contextNames(fields["exclude_tools"])
			if err != nil {
				return nil, err
			}
			if value := fields["clear_tool_inputs"]; !isJSONNull(value) {
				if json.Unmarshal(value, &edit.clearInputs) != nil {
					edit.clearInputNames, err = contextNames(value)
					if err != nil {
						return nil, err
					}
				}
			}
		default:
			return nil, unsupportedContext("edit " + kind + " requires native Anthropic support and compatible usage accounting")
		}
		parsed = append(parsed, edit)
	}
	return parsed, nil
}

func contextFields(fields map[string]json.RawMessage, allowed ...string) error {
	for key := range fields {
		found := false
		for _, candidate := range allowed {
			if key == candidate {
				found = true
				break
			}
		}
		if !found {
			return unsupportedContext("unsupported edit option " + key)
		}
	}
	return nil
}

func contextCount(raw json.RawMessage, kind string, positive bool) (int64, error) {
	var count map[string]json.RawMessage
	if json.Unmarshal(raw, &count) != nil || count == nil {
		return 0, fmt.Errorf("context_management %s must be a count object", kind)
	}
	if err := contextFields(count, "type", "value"); err != nil {
		return 0, err
	}
	var typ string
	var value int64
	if json.Unmarshal(count["type"], &typ) != nil || typ != kind || isJSONNull(count["value"]) || json.Unmarshal(count["value"], &value) != nil || value < 0 || (positive && value == 0) {
		return 0, fmt.Errorf("context_management %s requires a valid integer value", kind)
	}
	return value, nil
}

func contextNames(raw json.RawMessage) (map[string]bool, error) {
	if isJSONNull(raw) {
		return nil, nil
	}
	var names []string
	if json.Unmarshal(raw, &names) != nil {
		return nil, fmt.Errorf("context_management tool names must be an array of strings")
	}
	result := make(map[string]bool, len(names))
	for _, name := range names {
		result[name] = true
	}
	return result, nil
}

// contextMessages edits a copy for the OpenAI bridge. Native Body always keeps
// the caller's original history and context_management configuration intact.
func (r *Request) contextMessages() ([]message, error) {
	edits, err := parseContextManagement(r.raw["context_management"])
	if err != nil {
		return nil, err
	}
	var messages []message
	if err = json.Unmarshal(r.raw["messages"], &messages); err != nil {
		return nil, err
	}
	for _, edit := range edits {
		if edit.kind == clearThinking {
			messages, err = clearThinkingMessages(messages, edit)
		} else {
			body, _ := r.Body()
			messages, err = clearToolMessages(messages, edit, int64(len(body))+1024)
		}
		if err != nil {
			return nil, err
		}
	}
	return messages, nil
}

func rawBlocks(raw json.RawMessage) ([]map[string]json.RawMessage, bool) {
	var blocks []map[string]json.RawMessage
	err := json.Unmarshal(raw, &blocks)
	return blocks, err == nil && !isJSONNull(raw)
}

func clearThinkingMessages(messages []message, edit contextEdit) ([]message, error) {
	if edit.keepAll {
		return messages, nil
	}
	turns := make([]int64, len(messages))
	var current int64
	for i, msg := range messages {
		if msg.Role == "user" {
			blocks, ok := rawBlocks(msg.Content)
			toolResultsOnly := ok && len(blocks) > 0
			for _, block := range blocks {
				var kind string
				_ = json.Unmarshal(block["type"], &kind)
				toolResultsOnly = toolResultsOnly && kind == "tool_result"
			}
			if !toolResultsOnly {
				current++
			}
		}
		turns[i] = current
	}
	result := make([]message, 0, len(messages))
	for i, msg := range messages {
		blocks, ok := rawBlocks(msg.Content)
		if msg.Role != "assistant" || !ok || current-turns[i] < edit.keep {
			result = append(result, msg)
			continue
		}
		kept := make([]map[string]json.RawMessage, 0, len(blocks))
		for _, block := range blocks {
			var kind string
			_ = json.Unmarshal(block["type"], &kind)
			if kind != "thinking" && kind != "redacted_thinking" {
				kept = append(kept, block)
			}
		}
		if len(kept) == len(blocks) {
			result = append(result, msg)
			continue
		}
		if len(kept) > 0 {
			msg.Content, _ = json.Marshal(kept)
			result = append(result, msg)
		}
	}
	return result, nil
}

func clearToolMessages(messages []message, edit contextEdit, promptBound int64) ([]message, error) {
	type call struct {
		name  string
		order int
	}
	calls := make(map[string]call)
	count := 0
	for _, msg := range messages {
		if msg.Role != "assistant" {
			continue
		}
		blocks, _ := rawBlocks(msg.Content)
		for _, block := range blocks {
			var kind, id, name string
			_ = json.Unmarshal(block["type"], &kind)
			_ = json.Unmarshal(block["id"], &id)
			_ = json.Unmarshal(block["name"], &name)
			if kind == "tool_use" {
				calls[id] = call{name, count}
				count++
			}
		}
	}
	eligible := make(map[string]bool)
	for _, msg := range messages {
		if msg.Role != "user" {
			continue
		}
		blocks, _ := rawBlocks(msg.Content)
		for _, block := range blocks {
			var kind, id string
			_ = json.Unmarshal(block["type"], &kind)
			_ = json.Unmarshal(block["tool_use_id"], &id)
			if call, ok := calls[id]; kind == "tool_result" && ok && int64(count-call.order) > edit.keep && !edit.exclude[call.name] {
				eligible[id] = true
			}
		}
	}
	if len(eligible) == 0 {
		return messages, nil
	}
	if edit.triggerType == "input_tokens" {
		if promptBound <= edit.trigger {
			return messages, nil
		}
		return nil, unsupportedContext("input_tokens-triggered tool clearing requires an exact native Anthropic token count")
	}
	if int64(count) <= edit.trigger {
		return messages, nil
	}
	if edit.clearAtLeast > 0 {
		return nil, unsupportedContext("clear_at_least requires an exact native Anthropic token count")
	}
	result := append([]message(nil), messages...)
	for i, msg := range result {
		blocks, ok := rawBlocks(msg.Content)
		if !ok {
			continue
		}
		changed := false
		for _, block := range blocks {
			var kind, id string
			_ = json.Unmarshal(block["type"], &kind)
			if kind == "tool_use" && msg.Role == "assistant" {
				_ = json.Unmarshal(block["id"], &id)
				if eligible[id] && (edit.clearInputs || edit.clearInputNames[calls[id].name]) {
					block["input"] = json.RawMessage(`{}`)
					changed = true
				}
			}
			if kind == "tool_result" && msg.Role == "user" {
				_ = json.Unmarshal(block["tool_use_id"], &id)
				if eligible[id] {
					marker := json.RawMessage(`"[tool result cleared]"`)
					if len(marker) > len(block["content"]) {
						marker = json.RawMessage(`""`)
					}
					if len(block["content"]) > 0 {
						block["content"] = marker
						changed = true
					}
				}
			}
		}
		if changed {
			result[i].Content, _ = json.Marshal(blocks)
		}
	}
	return result, nil
}
