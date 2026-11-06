package middleware

import (
	"encoding/json"
	"fmt"
)

// The existing ZeoNexus reservation uses serialized bytes as a conservative
// text-token bound. Remote/binary content and server-executed tools cannot use
// that bound: a short URL may expand into an arbitrarily large document.
func validateAnthropicReservation(request map[string]json.RawMessage) error {
	for _, key := range []string{"mcp_servers", "container", "context_management"} {
		if value, exists := request[key]; exists && string(value) != "null" {
			return fmt.Errorf("此网关暂不支持无法预估预算的 Anthropic %s", key)
		}
	}
	var tools []struct {
		Type string `json:"type"`
	}
	if value, exists := request["tools"]; exists {
		if err := json.Unmarshal(value, &tools); err != nil {
			return fmt.Errorf("tools 必须为数组")
		}
		for _, tool := range tools {
			if tool.Type != "" && tool.Type != "custom" {
				return fmt.Errorf("此网关暂不支持无法预估预算的服务端工具：%s", tool.Type)
			}
		}
	}
	var messages []struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(request["messages"], &messages); err != nil || len(messages) == 0 {
		return fmt.Errorf("messages 必须为非空数组")
	}
	for _, message := range messages {
		if err := validateAnthropicTextContent(message.Content); err != nil {
			return err
		}
	}
	if system, exists := request["system"]; exists {
		return validateAnthropicTextContent(system)
	}
	return nil
}

func validateAnthropicTextContent(content json.RawMessage) error {
	var text string
	if json.Unmarshal(content, &text) == nil && string(content) != "null" {
		return nil
	}
	var blocks []struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(content, &blocks) != nil || string(content) == "null" {
		return fmt.Errorf("消息 content 必须为字符串或内容块数组")
	}
	for _, block := range blocks {
		switch block.Type {
		case "text", "tool_use", "thinking", "redacted_thinking":
		case "tool_result":
			if len(block.Content) > 0 {
				if err := validateAnthropicTextContent(block.Content); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("此网关暂不支持无法预估预算的内容块：%s", block.Type)
		}
	}
	return nil
}
