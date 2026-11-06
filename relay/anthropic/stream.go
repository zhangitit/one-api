package anthropic

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/relay/model"
)

type toolDelta struct {
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type streamChunk struct {
	ID      string `json:"id"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content          *string     `json:"content"`
			ReasoningContent *string     `json:"reasoning_content"`
			Refusal          *string     `json:"refusal"`
			ToolCalls        []toolDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *openAIUsage    `json:"usage"`
	Error json.RawMessage `json:"error"`
}

type streamingTool struct {
	id, name, args string
	index          int
	started        bool
}

func writeEvent(c *gin.Context, event string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	c.Writer.Flush()
	return c.Request.Context().Err()
}

func handleOpenAIStream(c *gin.Context, resp *http.Response, modelName string) (*model.Usage, error) {
	streamHeaders(c, resp)
	started, done, finished := false, false, false
	refusal := false
	finish := ""
	var usage *openAIUsage
	nextIndex := 0
	textIndex := -1
	textClosed := false
	var trailingText strings.Builder
	tools := map[int]*streamingTool{}
	var toolOrder []int
	var activeTool *streamingTool
	start := func(id string) error {
		if started {
			return nil
		}
		started = true
		return writeEvent(c, "message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": messageID(id), "type": "message", "role": "assistant", "model": modelName, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 0, "output_tokens": 0}}})
	}
	startTool := func(tool *streamingTool) error {
		if tool.started {
			return nil
		}
		if tool.id == "" || tool.name == "" {
			return fmt.Errorf("upstream tool call is missing id or name")
		}
		tool.index = nextIndex
		nextIndex++
		tool.started = true
		if textIndex >= 0 && !textClosed {
			if err := writeEvent(c, "content_block_stop", map[string]any{"type": "content_block_stop", "index": textIndex}); err != nil {
				return err
			}
			textClosed = true
		}
		if err := writeEvent(c, "content_block_start", map[string]any{"type": "content_block_start", "index": tool.index, "content_block": map[string]any{"type": "tool_use", "id": tool.id, "name": tool.name, "input": map[string]any{}}}); err != nil {
			return err
		}
		return nil
	}
	err := readSSE(resp.Body, func(event string, data, raw []byte) error {
		if len(data) == 0 {
			return nil
		}
		if string(data) == "[DONE]" {
			done = true
			return nil
		}
		if done {
			return fmt.Errorf("upstream sent data after stream termination")
		}
		var chunk streamChunk
		if json.Unmarshal(data, &chunk) != nil {
			return fmt.Errorf("invalid upstream chat completion SSE JSON")
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return fmt.Errorf("upstream reported a streaming error")
		}
		if err := start(chunk.ID); err != nil {
			return err
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				return fmt.Errorf("upstream returned multiple completions")
			}
			if choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "" {
				return fmt.Errorf("upstream reasoning_content requires a native Anthropic channel or disabled upstream thinking")
			}
			if choice.Delta.Refusal != nil && *choice.Delta.Refusal != "" {
				refusal = true
				text := *choice.Delta.Refusal
				if choice.Delta.Content != nil {
					text = *choice.Delta.Content + text
				}
				choice.Delta.Content = &text
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				var err error
				finish, err = stopReason(*choice.FinishReason)
				if err != nil {
					return err
				}
				finished = true
			}
			if choice.Delta.Content != nil && *choice.Delta.Content != "" {
				if activeTool != nil || textClosed {
					trailingText.WriteString(*choice.Delta.Content)
				} else {
					if textIndex < 0 {
						textIndex = nextIndex
						nextIndex++
						if err := writeEvent(c, "content_block_start", map[string]any{"type": "content_block_start", "index": textIndex, "content_block": map[string]any{"type": "text", "text": ""}}); err != nil {
							return err
						}
					}
					if err := writeEvent(c, "content_block_delta", map[string]any{"type": "content_block_delta", "index": textIndex, "delta": map[string]any{"type": "text_delta", "text": *choice.Delta.Content}}); err != nil {
						return err
					}
				}
			}
			for _, part := range choice.Delta.ToolCalls {
				if part.Index == nil || *part.Index < 0 {
					return fmt.Errorf("upstream tool delta is missing index")
				}
				tool := tools[*part.Index]
				if tool == nil {
					tool = &streamingTool{}
					tools[*part.Index] = tool
					toolOrder = append(toolOrder, *part.Index)
				}
				if tool.started {
					if (part.ID != "" && part.ID != tool.id) || (part.Function.Name != "" && part.Function.Name != tool.name) {
						return fmt.Errorf("upstream changed tool metadata after arguments started")
					}
					part.ID = ""
					part.Function.Name = ""
				}
				tool.id += part.ID
				tool.name += part.Function.Name
				tool.args += part.Function.Arguments
				arguments := part.Function.Arguments
				if activeTool == nil && tool.id != "" && tool.name != "" && tool.args != "" {
					if err := startTool(tool); err != nil {
						return err
					}
					activeTool = tool
					arguments = tool.args
				}
				if tool == activeTool && arguments != "" {
					if err := writeEvent(c, "content_block_delta", map[string]any{"type": "content_block_delta", "index": tool.index, "delta": map[string]any{"type": "input_json_delta", "partial_json": arguments}}); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !done || !finished || !started {
		return nil, fmt.Errorf("upstream stream ended before its completion marker")
	}
	if refusal {
		finish = "refusal"
	}
	result, err := usage.usage()
	if err != nil {
		return nil, err
	}
	// A parallel OpenAI tool stream can interleave calls. Anthropic content
	// blocks are sequential: stream the first call, then replay buffered calls.
	if activeTool != nil {
		for i, key := range toolOrder {
			if tools[key] == activeTool {
				toolOrder = append([]int{key}, append(toolOrder[:i], toolOrder[i+1:]...)...)
				break
			}
		}
	}
	for _, key := range toolOrder {
		tool := tools[key]
		if tool.args == "" {
			tool.args = "{}"
		}
		var input map[string]any
		if json.Unmarshal([]byte(tool.args), &input) != nil || input == nil {
			return nil, fmt.Errorf("upstream ended with invalid tool JSON arguments")
		}
		if !tool.started {
			if err := startTool(tool); err != nil {
				return nil, err
			}
			if err := writeEvent(c, "content_block_delta", map[string]any{"type": "content_block_delta", "index": tool.index, "delta": map[string]any{"type": "input_json_delta", "partial_json": tool.args}}); err != nil {
				return nil, err
			}
		}
		if err := writeEvent(c, "content_block_stop", map[string]any{"type": "content_block_stop", "index": tool.index}); err != nil {
			return nil, err
		}
	}
	if textIndex >= 0 && !textClosed {
		if err := writeEvent(c, "content_block_stop", map[string]any{"type": "content_block_stop", "index": textIndex}); err != nil {
			return nil, err
		}
	}
	if trailingText.Len() > 0 {
		index := nextIndex
		for _, event := range []struct {
			name  string
			value any
		}{
			{"content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]string{"type": "text", "text": ""}}},
			{"content_block_delta", map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]string{"type": "text_delta", "text": trailingText.String()}}},
			{"content_block_stop", map[string]any{"type": "content_block_stop", "index": index}},
		} {
			if err := writeEvent(c, event.name, event.value); err != nil {
				return nil, err
			}
		}
	}
	if err := writeEvent(c, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": finish, "stop_sequence": nil}, "usage": nativeUsageMap(result)}); err != nil {
		return nil, err
	}
	if err := writeEvent(c, "message_stop", map[string]string{"type": "message_stop"}); err != nil {
		return nil, err
	}
	return result, nil
}
