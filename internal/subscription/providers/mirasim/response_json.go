package mirasim

import (
	"bytes"
	"encoding/json"
)

// claudeJSONMessageAsSSE turns a non-streaming Claude Messages body into the SSE
// sequence the pinned CPA response translators aggregate. Those translators ignore
// a raw JSON message, which previously came back to Responses clients as a
// completed response with an empty output array.
func claudeJSONMessageAsSSE(body []byte) []byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' || bytes.HasPrefix(trimmed, []byte("data:")) {
		return body
	}
	var message struct {
		ID         string            `json:"id"`
		Content    []json.RawMessage `json:"content"`
		StopReason string            `json:"stop_reason"`
		Usage      json.RawMessage   `json:"usage"`
	}
	if json.Unmarshal(trimmed, &message) != nil || message.Content == nil {
		return body
	}
	usage := message.Usage
	if len(usage) == 0 {
		usage = json.RawMessage(`{}`)
	}
	var out bytes.Buffer
	write := func(event any) {
		raw, err := json.Marshal(event)
		if err != nil {
			return
		}
		out.WriteString("data: ")
		out.Write(raw)
		out.WriteByte('\n')
	}
	write(map[string]any{
		"type":    "message_start",
		"message": map[string]any{"id": message.ID, "usage": usage},
	})
	for index, block := range message.Content {
		var kind struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(block, &kind)
		write(map[string]any{
			"type":          "content_block_start",
			"index":         index,
			"content_block": block,
		})
		switch kind.Type {
		case "text":
			var text struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(block, &text)
			write(map[string]any{
				"type":  "content_block_delta",
				"index": index,
				"delta": map[string]any{"type": "text_delta", "text": text.Text},
			})
		case "thinking":
			var thinking struct {
				Thinking  string `json:"thinking"`
				Signature string `json:"signature"`
			}
			_ = json.Unmarshal(block, &thinking)
			if thinking.Thinking != "" {
				write(map[string]any{
					"type":  "content_block_delta",
					"index": index,
					"delta": map[string]any{"type": "thinking_delta", "thinking": thinking.Thinking},
				})
			}
			if thinking.Signature != "" {
				write(map[string]any{
					"type":  "content_block_delta",
					"index": index,
					"delta": map[string]any{"type": "signature_delta", "signature": thinking.Signature},
				})
			}
		case "tool_use":
			var tool struct {
				Input json.RawMessage `json:"input"`
			}
			_ = json.Unmarshal(block, &tool)
			if len(tool.Input) > 0 && string(tool.Input) != "null" {
				write(map[string]any{
					"type":  "content_block_delta",
					"index": index,
					"delta": map[string]any{"type": "input_json_delta", "partial_json": string(tool.Input)},
				})
			}
		}
		write(map[string]any{"type": "content_block_stop", "index": index})
	}
	write(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": message.StopReason},
		"usage": usage,
	})
	write(map[string]any{"type": "message_stop"})
	if out.Len() == 0 {
		return body
	}
	return out.Bytes()
}
