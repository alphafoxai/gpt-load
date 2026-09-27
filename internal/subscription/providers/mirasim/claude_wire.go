package mirasim

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// claudeNonStreamPayload checks a Claude Messages body that was read from an
// upstream SSE response. JSON is left for the existing non-stream translators.
// An error event fails the attempt instead of becoming an empty completion.
func claudeNonStreamPayload(raw []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("Mirasim Claude response is empty")
	}
	if trimmed[0] == '{' {
		return append([]byte(nil), raw...), nil
	}
	sawData := false
	var streamErr error
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), maxCodexEventBytes)
	for scanner.Scan() {
		payload, ok := claudeDataPayload(scanner.Bytes())
		if !ok {
			continue
		}
		sawData = true
		var event struct {
			Type  string `json:"type"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &event) != nil {
			continue
		}
		if event.Type == "error" {
			streamErr = claudeStreamError(event.Error.Type, event.Error.Message)
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		return nil, fmt.Errorf("read Mirasim Claude response: %w", errScan)
	}
	if streamErr != nil {
		return nil, streamErr
	}
	if !sawData {
		return nil, fmt.Errorf("Mirasim Claude response has no events")
	}
	return append([]byte(nil), raw...), nil
}

// claudeSSEAsMessage rebuilds the non-streaming Messages JSON a native Claude
// client asked for. Other client formats keep the SSE and use their translator.
func claudeSSEAsMessage(raw []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("Mirasim Claude response is empty")
	}
	if trimmed[0] == '{' {
		return append([]byte(nil), raw...), nil
	}
	var messageID, model, stopReason string
	var usage json.RawMessage
	blocks := map[int]*claudeAggBlock{}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), maxCodexEventBytes)
	for scanner.Scan() {
		payload, ok := claudeDataPayload(scanner.Bytes())
		if !ok {
			continue
		}
		var event struct {
			Type         string          `json:"type"`
			Index        *int            `json:"index"`
			Message      json.RawMessage `json:"message"`
			ContentBlock json.RawMessage `json:"content_block"`
			Delta        json.RawMessage `json:"delta"`
			Usage        json.RawMessage `json:"usage"`
		}
		if json.Unmarshal(payload, &event) != nil {
			continue
		}
		switch event.Type {
		case "message_start":
			var started struct {
				ID    string          `json:"id"`
				Model string          `json:"model"`
				Usage json.RawMessage `json:"usage"`
			}
			_ = json.Unmarshal(event.Message, &started)
			if started.ID != "" {
				messageID = started.ID
			}
			if started.Model != "" {
				model = started.Model
			}
			usage = mergeJSONObject(usage, started.Usage)
		case "content_block_start":
			if event.Index == nil {
				continue
			}
			block := &claudeAggBlock{raw: map[string]any{}}
			if len(event.ContentBlock) > 0 && !bytes.Equal(event.ContentBlock, []byte("null")) {
				_ = json.Unmarshal(event.ContentBlock, &block.raw)
			}
			if block.raw == nil {
				block.raw = map[string]any{}
			}
			blocks[*event.Index] = block
		case "content_block_delta":
			if event.Index == nil {
				continue
			}
			block := blocks[*event.Index]
			if block == nil {
				block = &claudeAggBlock{raw: map[string]any{}}
				blocks[*event.Index] = block
			}
			var delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
			}
			_ = json.Unmarshal(event.Delta, &delta)
			switch delta.Type {
			case "text_delta":
				block.text.WriteString(delta.Text)
				block.sawText = true
			case "thinking_delta":
				block.thinking.WriteString(delta.Thinking)
				block.sawThinking = true
			case "signature_delta":
				block.signature = delta.Signature
				block.sawSignature = true
			case "input_json_delta":
				block.inputJSON.WriteString(delta.PartialJSON)
				block.sawInput = true
			}
		case "message_delta":
			var delta struct {
				StopReason string `json:"stop_reason"`
			}
			_ = json.Unmarshal(event.Delta, &delta)
			if delta.StopReason != "" {
				stopReason = delta.StopReason
			}
			usage = mergeJSONObject(usage, event.Usage)
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		return nil, fmt.Errorf("read Mirasim Claude response: %w", errScan)
	}
	indexes := make([]int, 0, len(blocks))
	for index := range blocks {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	content := make([]any, 0, len(indexes))
	for _, index := range indexes {
		content = append(content, blocks[index].object())
	}
	if usage == nil {
		usage = json.RawMessage(`{}`)
	}
	encoded, err := json.Marshal(map[string]any{
		"id": messageID, "type": "message", "role": "assistant", "model": model,
		"content": content, "stop_reason": stopReason, "usage": json.RawMessage(usage),
	})
	if err != nil {
		return nil, fmt.Errorf("encode Mirasim Claude message: %w", err)
	}
	return encoded, nil
}

type claudeAggBlock struct {
	raw          map[string]any
	text         strings.Builder
	thinking     strings.Builder
	signature    string
	inputJSON    strings.Builder
	sawText      bool
	sawThinking  bool
	sawSignature bool
	sawInput     bool
}

func (b *claudeAggBlock) object() map[string]any {
	if b.sawText {
		b.raw["text"] = b.text.String()
	}
	if b.sawThinking {
		b.raw["thinking"] = b.thinking.String()
	}
	if b.sawSignature {
		b.raw["signature"] = b.signature
	}
	if b.sawInput {
		var input any
		if json.Unmarshal([]byte(b.inputJSON.String()), &input) == nil {
			b.raw["input"] = input
		} else {
			b.raw["input"] = b.inputJSON.String()
		}
	}
	return b.raw
}

func claudeDataPayload(line []byte) ([]byte, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
		return nil, false
	}
	payload := bytes.TrimSpace(line[len("data:"):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return nil, false
	}
	return payload, true
}

func mergeJSONObject(base, extra json.RawMessage) json.RawMessage {
	extra = bytes.TrimSpace(extra)
	if len(extra) == 0 || bytes.Equal(extra, []byte("null")) {
		return base
	}
	base = bytes.TrimSpace(base)
	if len(base) == 0 || bytes.Equal(base, []byte("null")) {
		return append(json.RawMessage(nil), extra...)
	}
	var left, right map[string]json.RawMessage
	if json.Unmarshal(base, &left) != nil || json.Unmarshal(extra, &right) != nil {
		return append(json.RawMessage(nil), extra...)
	}
	if left == nil {
		left = map[string]json.RawMessage{}
	}
	for key, value := range right {
		left[key] = append(json.RawMessage(nil), value...)
	}
	merged, err := json.Marshal(left)
	if err != nil {
		return append(json.RawMessage(nil), extra...)
	}
	return merged
}

func claudeStreamError(kind, message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "upstream stream failed"
	}
	if len(message) > 4096 {
		message = message[:4096] + "..."
	}
	kind = strings.TrimSpace(kind)
	if kind != "" {
		return fmt.Errorf("Mirasim Claude stream failed (%s): %s", kind, message)
	}
	return fmt.Errorf("Mirasim Claude stream failed: %s", message)
}
