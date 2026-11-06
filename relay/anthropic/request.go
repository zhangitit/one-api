// Package anthropic implements the client-facing Anthropic Messages protocol.
// Native requests retain their original JSON fields; conversion is used only for
// channels that speak the OpenAI chat completions protocol.
package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/songquanpeng/one-api/relay/model"
)

type Request struct {
	Model     string
	MaxTokens int
	Stream    bool
	raw       map[string]json.RawMessage
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Source    struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	} `json:"source"`
}

func ParseRequest(body []byte) (*Request, error) {
	r := &Request{}
	if err := json.Unmarshal(body, &r.raw); err != nil || r.raw == nil {
		return nil, fmt.Errorf("request body must be a JSON object")
	}
	if json.Unmarshal(r.raw["model"], &r.Model) != nil || strings.TrimSpace(r.Model) == "" {
		return nil, fmt.Errorf("model is required")
	}
	if string(r.raw["max_tokens"]) == "null" || json.Unmarshal(r.raw["max_tokens"], &r.MaxTokens) != nil || r.MaxTokens < 0 {
		return nil, fmt.Errorf("max_tokens must be a nonnegative integer")
	}
	if value, ok := r.raw["stream"]; ok {
		if string(value) == "null" || json.Unmarshal(value, &r.Stream) != nil {
			return nil, fmt.Errorf("stream must be a boolean")
		}
	}
	var messages []message
	if json.Unmarshal(r.raw["messages"], &messages) != nil || len(messages) == 0 {
		return nil, fmt.Errorf("messages must be a nonempty array")
	}
	for i, m := range messages {
		if m.Role != "user" && m.Role != "assistant" && m.Role != "system" {
			return nil, fmt.Errorf("messages[%d].role must be user, assistant or system", i)
		}
		if err := validateNativeContent(m.Content, false); err != nil {
			return nil, fmt.Errorf("messages[%d].content: %w", i, err)
		}
	}
	if system, ok := r.raw["system"]; ok {
		if err := validateNativeContent(system, true); err != nil {
			return nil, fmt.Errorf("system: %w", err)
		}
	}
	return r, nil
}

func (r *Request) SetModel(name string)    { r.Model = name; r.raw["model"], _ = json.Marshal(name) }
func (r *Request) SetSystem(system string) { r.raw["system"], _ = json.Marshal(system) }
func (r *Request) Body() ([]byte, error)   { return json.Marshal(r.raw) }

func validateNativeContent(raw json.RawMessage, textOnly bool) error {
	var text string
	if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &text) == nil {
		return nil
	}
	var blocks []map[string]json.RawMessage
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &blocks) != nil {
		return fmt.Errorf("must be a string or content block array")
	}
	for _, b := range blocks {
		var kind string
		if json.Unmarshal(b["type"], &kind) != nil || kind == "" {
			return fmt.Errorf("content blocks require a type")
		}
		if textOnly && kind != "text" {
			return fmt.Errorf("system only supports text blocks")
		}
	}
	return nil
}

func parseBlocks(raw json.RawMessage) ([]block, error) {
	var text string
	if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &text) == nil {
		return []block{{Type: "text", Text: text}}, nil
	}
	var blocks []block
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &blocks) != nil {
		return nil, fmt.Errorf("must be a string or content block array")
	}
	for _, b := range blocks {
		if b.Type == "" {
			return nil, fmt.Errorf("content blocks require a type")
		}
	}
	return blocks, nil
}

func (r *Request) ToOpenAI() (*model.GeneralOpenAIRequest, error) {
	if r.MaxTokens == 0 {
		return nil, fmt.Errorf("max_tokens=0 cache prewarming requires a native Anthropic channel")
	}
	// These fields alter model behavior and have no portable chat-completions
	// equivalent. A clear error is safer than silently losing those semantics.
	for _, key := range []string{"thinking", "compaction", "container", "mcp_servers", "output_config", "output_format", "top_k"} {
		if value, ok := r.raw[key]; ok && string(value) != "null" {
			if key == "thinking" {
				var mode struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(value, &mode) == nil && mode.Type == "disabled" {
					continue
				}
			}
			return nil, fmt.Errorf("%s requires a native Anthropic channel", key)
		}
	}
	o := &model.GeneralOpenAIRequest{Model: r.Model, MaxTokens: r.MaxTokens, Stream: r.Stream}
	for key, dst := range map[string]any{"temperature": &o.Temperature, "top_p": &o.TopP} {
		if value, ok := r.raw[key]; ok {
			if err := json.Unmarshal(value, dst); err != nil {
				return nil, fmt.Errorf("invalid %s", key)
			}
		}
	}
	if value, ok := r.raw["stop_sequences"]; ok {
		var stops []string
		if json.Unmarshal(value, &stops) != nil {
			return nil, fmt.Errorf("stop_sequences must be an array of strings")
		}
		o.Stop = stops
	}
	if value, ok := r.raw["service_tier"]; ok {
		var tier string
		if json.Unmarshal(value, &tier) != nil {
			return nil, fmt.Errorf("invalid service_tier")
		}
		switch tier {
		case "auto":
		case "standard_only":
			tier = "default"
		default:
			return nil, fmt.Errorf("service_tier %q requires a native Anthropic channel", tier)
		}
		o.ServiceTier = &tier
	}
	if value, ok := r.raw["metadata"]; ok {
		var metadata struct {
			UserID string `json:"user_id"`
		}
		if json.Unmarshal(value, &metadata) != nil {
			return nil, fmt.Errorf("invalid metadata")
		}
		o.User = metadata.UserID
	}
	if system, ok := r.raw["system"]; ok {
		blocks, err := parseBlocks(system)
		if err != nil {
			return nil, fmt.Errorf("invalid system blocks for OpenAI conversion: %w", err)
		}
		var texts []string
		for _, b := range blocks {
			texts = append(texts, b.Text)
		}
		o.Messages = append(o.Messages, model.Message{Role: "system", Content: strings.Join(texts, "\n")})
	}
	messages, err := r.contextMessages()
	if err != nil {
		return nil, err
	}
	for i, m := range messages {
		blocks, err := parseBlocks(m.Content)
		if err != nil {
			return nil, fmt.Errorf("invalid message blocks for OpenAI conversion: %w", err)
		}
		var content []any
		var calls []model.Tool
		var results []model.Message
		for _, b := range blocks {
			switch b.Type {
			case "text":
				content = append(content, map[string]any{"type": "text", "text": b.Text})
			case "image":
				if m.Role != "user" {
					return nil, fmt.Errorf("images require a user message")
				}
				url, err := imageURL(b)
				if err != nil {
					return nil, err
				}
				content = append(content, map[string]any{"type": "image_url", "image_url": map[string]string{"url": url}})
			case "tool_use":
				if m.Role != "assistant" || b.ID == "" || b.Name == "" || len(b.Input) == 0 || b.Input[0] != '{' {
					return nil, fmt.Errorf("invalid tool_use block in messages[%d]", i)
				}
				calls = append(calls, model.Tool{Id: b.ID, Type: "function", Function: model.Function{Name: b.Name, Arguments: string(b.Input)}})
			case "tool_result":
				if m.Role != "user" || b.ToolUseID == "" {
					return nil, fmt.Errorf("invalid tool_result block in messages[%d]", i)
				}
				result, err := toolResult(b.Content)
				if err != nil {
					return nil, err
				}
				if b.IsError {
					encoded, _ := json.Marshal(map[string]any{"is_error": true, "content": result})
					result = string(encoded)
				}
				results = append(results, model.Message{Role: "tool", ToolCallId: b.ToolUseID, Content: result})
			default:
				return nil, fmt.Errorf("content block type %q requires a native Anthropic channel", b.Type)
			}
		}
		// Anthropic places tool results in user messages. OpenAI requires the
		// result messages immediately after the assistant's function calls.
		o.Messages = append(o.Messages, results...)
		if len(content) > 0 || len(calls) > 0 || len(results) == 0 {
			var value any = content
			if len(content) == 1 {
				if part, ok := content[0].(map[string]any); ok && part["type"] == "text" {
					value = part["text"]
				}
			}
			if len(content) == 0 {
				value = nil
			}
			o.Messages = append(o.Messages, model.Message{Role: m.Role, Content: value, ToolCalls: calls})
		}
	}
	if value, ok := r.raw["tools"]; ok {
		var tools []struct {
			Type        string          `json:"type"`
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"input_schema"`
		}
		if json.Unmarshal(value, &tools) != nil {
			return nil, fmt.Errorf("tools must be an array")
		}
		for _, tool := range tools {
			if (tool.Type != "" && tool.Type != "custom") || tool.Name == "" || len(tool.InputSchema) == 0 || tool.InputSchema[0] != '{' {
				return nil, fmt.Errorf("server tools and tools without an input_schema require a native Anthropic channel")
			}
			o.Tools = append(o.Tools, model.Tool{Type: "function", Function: model.Function{Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema}})
		}
	}
	if value, ok := r.raw["tool_choice"]; ok {
		var choice struct {
			Type            string `json:"type"`
			Name            string `json:"name"`
			DisableParallel *bool  `json:"disable_parallel_tool_use"`
		}
		if json.Unmarshal(value, &choice) != nil {
			return nil, fmt.Errorf("invalid tool_choice")
		}
		switch choice.Type {
		case "auto", "none":
			o.ToolChoice = choice.Type
		case "any":
			o.ToolChoice = "required"
		case "tool":
			if choice.Name == "" {
				return nil, fmt.Errorf("tool_choice.name is required")
			}
			o.ToolChoice = map[string]any{"type": "function", "function": map[string]string{"name": choice.Name}}
		default:
			return nil, fmt.Errorf("unsupported tool_choice type %q", choice.Type)
		}
		if choice.DisableParallel != nil {
			parallel := !*choice.DisableParallel
			o.ParallelTooCalls = &parallel
		}
	}
	if r.Stream {
		o.StreamOptions = &model.StreamOptions{IncludeUsage: true}
	}
	return o, nil
}

func imageURL(b block) (string, error) {
	switch b.Source.Type {
	case "base64":
		if b.Source.MediaType == "" || b.Source.Data == "" {
			return "", fmt.Errorf("base64 images require media_type and data")
		}
		return "data:" + b.Source.MediaType + ";base64," + b.Source.Data, nil
	case "url":
		if b.Source.URL != "" {
			return b.Source.URL, nil
		}
	}
	return "", fmt.Errorf("unsupported image source")
}

func toolResult(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return "", nil
	}
	blocks, err := parseBlocks(raw)
	if err != nil {
		return nil, fmt.Errorf("tool_result.content: %w", err)
	}
	var content []any
	var texts []string
	onlyText := true
	for _, b := range blocks {
		switch b.Type {
		case "text":
			texts = append(texts, b.Text)
			content = append(content, map[string]any{"type": "text", "text": b.Text})
		case "image":
			onlyText = false
			url, err := imageURL(b)
			if err != nil {
				return nil, err
			}
			content = append(content, map[string]any{"type": "image_url", "image_url": map[string]string{"url": url}})
		default:
			return nil, fmt.Errorf("tool_result content type %q requires a native Anthropic channel", b.Type)
		}
	}
	if onlyText {
		return strings.Join(texts, "\n"), nil
	}
	return content, nil
}
