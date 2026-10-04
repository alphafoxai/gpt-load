package qoder

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const qoderSystem = "You are a Qoder agent. Use the instructions below and the tools available to you to assist the user."

var effortRank = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

func fitEffort(want string, levels []string) string {
	if want == "ultra" && !containsString(levels, want) {
		want = "max"
	}
	if len(levels) == 0 || containsString(levels, want) {
		return want
	}
	at := indexOf(effortRank, want)
	if at < 0 {
		return want
	}
	best, dist := want, len(effortRank)
	for _, level := range levels {
		i := indexOf(effortRank, level)
		if i < 0 || level == "none" {
			continue
		}
		delta := i - at
		if delta < 0 {
			delta = -delta
		}
		if delta < dist || (delta == dist && i > at) {
			best, dist = level, delta
		}
	}
	return best
}

func effortFor(asked string, model modelSpec) (bool, string) {
	want := strings.ToLower(strings.TrimSpace(asked))
	if indexOf(effortRank, want) < 0 {
		want = ""
	}
	if !model.Thinks || (want == "none" && !model.AlwaysThinks) {
		return false, ""
	}
	if len(model.Efforts) == 0 {
		return true, ""
	}
	switch want {
	case "":
		return true, model.DefaultEffort
	case "none":
		return true, model.Efforts[0]
	default:
		return true, fitEffort(want, model.Efforts)
	}
}

func windowFor(payload []byte, model modelSpec) int {
	base := model.DefaultWindow
	if base == 0 && model.MaxInput > 0 {
		base = model.MaxInput
	}
	if base == 0 {
		base = model.Context
	}
	if len(model.Windows) == 0 {
		return model.Context
	}
	need := (len(payload) + 2) / 3
	if base > 0 && need <= base {
		return base
	}
	for _, window := range model.Windows {
		if window >= need {
			return window
		}
	}
	return model.Windows[len(model.Windows)-1]
}

type openAIChat struct {
	Model               string            `json:"model"`
	Messages            []json.RawMessage `json:"messages"`
	Tools               []json.RawMessage `json:"tools"`
	ToolChoice          json.RawMessage   `json:"tool_choice"`
	MaxTokens           int               `json:"max_tokens"`
	MaxCompletionTokens int               `json:"max_completion_tokens"`
	ReasoningEffort     string            `json:"reasoning_effort"`
}

func qoderBody(chat openAIChat, raw []byte, model modelSpec, now time.Time) ([]byte, error) {
	system := systemText(chat.Messages)
	tools := []json.RawMessage{}
	if !toolChoiceIs(chat.ToolChoice, "none") {
		tools = functionTools(chat.Tools)
	}
	sysText := qoderSystem
	if system != "" {
		sysText += "\n\n" + system
	}
	if toolChoiceIs(chat.ToolChoice, "required") && len(tools) > 0 {
		sysText += "\nYou must call an available function in this response."
	}
	sys := map[string]string{"type": "text", "text": sysText}
	thinking, effort := effortFor(chat.ReasoningEffort, model)
	maxTokens := chat.MaxCompletionTokens
	if maxTokens == 0 {
		maxTokens = chat.MaxTokens
	}
	if maxTokens == 0 {
		maxTokens = 32000
	}
	params := map[string]any{"enable_thinking": thinking, "max_tokens": maxTokens}
	if effort != "" {
		params["reasoning_effort"] = effort
	}
	if window := windowFor(raw, model); window > 0 {
		params["context_length"] = window
	}
	body := map[string]any{
		"parameters": params,
		"business": map[string]any{
			"product": "app", "version": cosyVersion, "type": "agent", "id": hexID(),
			"name": "gpt-load session", "begin_at": now.UnixMilli(), "stage": "start",
		},
		"agent_id": "agent_common", "task_id": "common", "session_type": "app",
		"model_config": json.RawMessage(model.Config),
		"system":       []any{sys},
		"messages":     append([]any{map[string]any{"role": "system", "content": []any{sys}}}, qoderMessages(chat.Messages)...),
	}
	if len(tools) > 0 {
		converted := make([]any, 0, len(tools))
		for _, tool := range tools {
			var decoded struct {
				Function struct {
					Name        string          `json:"name"`
					Description string          `json:"description"`
					Parameters  json.RawMessage `json:"parameters"`
				} `json:"function"`
			}
			if json.Unmarshal(tool, &decoded) != nil || decoded.Function.Name == "" {
				continue
			}
			parameters := decoded.Function.Parameters
			if len(parameters) == 0 {
				parameters = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			converted = append(converted, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": decoded.Function.Name, "description": decoded.Function.Description, "parameters": parameters,
				},
			})
		}
		if len(converted) > 0 {
			body["tools"] = converted
		}
	}
	return json.Marshal(body)
}

func systemText(messages []json.RawMessage) string {
	var parts []string
	for _, raw := range messages {
		var message struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &message) != nil {
			continue
		}
		if message.Role != "system" && message.Role != "developer" {
			continue
		}
		if text := textOf(message.Content); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func functionTools(tools []json.RawMessage) []json.RawMessage {
	var out []json.RawMessage
	for _, raw := range tools {
		var tool struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if json.Unmarshal(raw, &tool) == nil && tool.Type == "function" && tool.Function.Name != "" {
			out = append(out, raw)
		}
	}
	return out
}

func toolChoiceIs(raw json.RawMessage, want string) bool {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text == want
	}
	return false
}

func textOf(content json.RawMessage) string {
	if len(content) == 0 || string(content) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var parts []string
	for _, block := range blocksOf(content) {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "")
}

type contentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}

func blocksOf(content json.RawMessage) []contentBlock {
	var text string
	if json.Unmarshal(content, &text) == nil {
		if text == "" {
			return nil
		}
		return []contentBlock{{Type: "text", Text: text}}
	}
	var items []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		ImageURL json.RawMessage `json:"image_url"`
	}
	if json.Unmarshal(content, &items) != nil {
		return nil
	}
	var out []contentBlock
	for _, item := range items {
		switch item.Type {
		case "text":
			if item.Text != "" {
				out = append(out, contentBlock{Type: "text", Text: item.Text})
			}
		case "image_url":
			url := imageURL(item.ImageURL)
			if url != "" {
				out = append(out, contentBlock{Type: "image_url", ImageURL: &struct {
					URL string `json:"url"`
				}{URL: url}})
			}
		}
	}
	return out
}

func imageURL(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var object struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &object) == nil {
		return object.URL
	}
	return ""
}

func qoderMessages(messages []json.RawMessage) []any {
	var out []any
	names := map[string]string{}
	var seen []contentBlock
	showSeen := func() {
		if len(seen) > 0 {
			out = append(out, map[string]any{"role": "user", "content": seen})
		}
		seen = nil
	}
	for _, raw := range messages {
		var message struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
			Name       string          `json:"name"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		}
		if json.Unmarshal(raw, &message) != nil {
			continue
		}
		if message.Role == "system" || message.Role == "developer" {
			continue
		}
		if message.Role != "user" && message.Role != "tool" {
			showSeen()
		}
		if message.Role == "tool" {
			txt := textOf(message.Content)
			var images []contentBlock
			for _, block := range blocksOf(message.Content) {
				if block.Type == "image_url" {
					images = append(images, block)
				}
			}
			if len(images) > 0 {
				of := "tool call " + message.ToolCallID
				if name := firstNonEmpty(names[message.ToolCallID], message.Name); name != "" {
					of = name + " (" + of + ")"
				}
				seen = append(append(seen, contentBlock{Type: "text", Text: "[From the result of " + of + ":]"}), images...)
				note := "[The tool returned an image; it follows in the next message.]"
				if len(images) != 1 {
					note = "[The tool returned " + itoa(len(images)) + " images; they follow in the next message.]"
				}
				if strings.TrimSpace(txt) != "" {
					txt += "\n\n" + note
				} else {
					txt += note
				}
			}
			out = append(out, map[string]any{"role": "tool", "tool_call_id": message.ToolCallID, "content": txt})
			continue
		}
		if message.Role == "assistant" && len(message.ToolCalls) > 0 {
			calls := make([]any, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				id := call.ID
				if id == "" {
					id = "call_" + hexID()
				}
				names[id] = call.Function.Name
				args := "{}"
				if len(call.Function.Arguments) > 0 && string(call.Function.Arguments) != "null" {
					var text string
					if json.Unmarshal(call.Function.Arguments, &text) == nil {
						args = text
					} else {
						args = string(call.Function.Arguments)
					}
				}
				calls = append(calls, map[string]any{
					"id": id, "type": "function",
					"function": map[string]string{"name": call.Function.Name, "arguments": args},
				})
			}
			out = append(out, map[string]any{"role": "assistant", "content": textOf(message.Content), "tool_calls": calls})
			continue
		}
		blocks := blocksOf(message.Content)
		if message.Role == "assistant" {
			blocks = textBlocks(blocks)
		}
		if len(blocks) == 0 {
			continue
		}
		if message.Role == "user" && len(seen) > 0 {
			blocks = append(seen, blocks...)
			seen = nil
		}
		out = append(out, map[string]any{"role": message.Role, "content": blocks})
	}
	showSeen()
	return out
}

func textBlocks(blocks []contentBlock) []contentBlock {
	var out []contentBlock
	for _, block := range blocks {
		if block.Type == "text" {
			out = append(out, block)
		}
	}
	return out
}

const callOpen = "<tool_call>"
const callClose = "</tool_call>"

var (
	funcPattern  = regexp.MustCompile(`<function=([^>]+)>([\s\S]*?)</function>`)
	paramPattern = regexp.MustCompile(`<parameter=([^>]+)>([\s\S]*?)</parameter>`)
)

type parsedCall struct {
	Name string
	Args string
}

func parseCall(value string) (parsedCall, bool) {
	var decoded struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(value)), &decoded) == nil && strings.TrimSpace(decoded.Name) != "" && decoded.Arguments != nil {
		args, err := json.Marshal(decoded.Arguments)
		if err == nil {
			return parsedCall{Name: strings.TrimSpace(decoded.Name), Args: string(args)}, true
		}
	}
	match := funcPattern.FindStringSubmatch(value)
	if len(match) < 3 || strings.TrimSpace(match[1]) == "" {
		return parsedCall{}, false
	}
	args := map[string]any{}
	for _, param := range paramPattern.FindAllStringSubmatch(match[2], -1) {
		key := strings.TrimSpace(param[1])
		if key == "" {
			continue
		}
		val := strings.TrimSpace(param[2])
		var parsed any
		if json.Unmarshal([]byte(val), &parsed) == nil {
			args[key] = parsed
		} else {
			args[key] = val
		}
	}
	raw, _ := json.Marshal(args)
	return parsedCall{Name: strings.TrimSpace(match[1]), Args: string(raw)}, true
}

type splitter struct {
	textBuf string
	callBuf string
	inCall  bool
	sawTool bool
}

type piece struct {
	text      string
	reasoning string
	call      *parsedCall
	callIndex int
	callID    string
	argsIndex int
	argsText  string
	stop      string
	usage     json.RawMessage
	errStatus int
	errText   string
	isErr     bool
	isArgs    bool
	isStop    bool
}

func (s *splitter) feed(input string) []piece {
	var out []piece
	for input != "" {
		if !s.inCall {
			combined := s.textBuf + input
			s.textBuf = ""
			at := strings.Index(combined, callOpen)
			if at < 0 {
				keep := 0
				for n := len(callOpen) - 1; n > 0; n-- {
					if strings.HasSuffix(combined, callOpen[:n]) {
						keep = n
						break
					}
				}
				if keep < len(combined) {
					out = append(out, piece{text: combined[:len(combined)-keep]})
				}
				s.textBuf = combined[len(combined)-keep:]
				break
			}
			if at > 0 {
				out = append(out, piece{text: combined[:at]})
			}
			input = combined[at+len(callOpen):]
			s.inCall = true
			continue
		}
		combined := s.callBuf + input
		s.callBuf = ""
		at := strings.Index(combined, callClose)
		if at < 0 {
			s.callBuf = combined
			break
		}
		if call, ok := parseCall(combined[:at]); ok {
			s.sawTool = true
			copied := call
			out = append(out, piece{call: &copied})
		} else {
			out = append(out, piece{text: callOpen + combined[:at] + callClose})
		}
		input = combined[at+len(callClose):]
		s.inCall = false
	}
	return out
}

func (s *splitter) flush() []piece {
	if s.inCall {
		text := callOpen + s.callBuf
		s.inCall = false
		s.callBuf = ""
		return []piece{{text: text}}
	}
	if s.textBuf != "" {
		text := s.textBuf
		s.textBuf = ""
		return []piece{{text: text}}
	}
	return nil
}

func readEvents(body io.Reader) []piece {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	split := splitter{}
	var out []piece
	index := -1
	native := -1
	var finish string
	var usage json.RawMessage
	flush := func() {
		for _, item := range split.flush() {
			if item.text != "" {
				out = append(out, item)
			}
		}
	}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var envelope struct {
			Body            string `json:"body"`
			StatusCodeValue *int   `json:"statusCodeValue"`
		}
		if json.Unmarshal([]byte(payload), &envelope) != nil {
			continue
		}
		if envelope.StatusCodeValue != nil && *envelope.StatusCodeValue != httpOK {
			out = append(out, piece{isErr: true, errStatus: *envelope.StatusCodeValue, errText: firstNonEmpty(envelope.Body, payload)})
			return out
		}
		if strings.TrimSpace(envelope.Body) == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					ToolCalls        []struct {
						Index    *int   `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage json.RawMessage `json:"usage"`
		}
		if json.Unmarshal([]byte(envelope.Body), &chunk) != nil {
			continue
		}
		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta
			if delta.ReasoningContent != "" {
				out = append(out, piece{reasoning: delta.ReasoningContent})
			}
			if delta.Content != "" {
				for _, item := range split.feed(delta.Content) {
					if item.call != nil {
						index++
						item.callIndex = index
						item.callID = "call_" + hexID()
						out = append(out, item)
					} else if item.text != "" {
						out = append(out, item)
					}
				}
			}
			for _, call := range delta.ToolCalls {
				at := 0
				if call.Index != nil {
					at = *call.Index
				}
				if at != native {
					native = at
					flush()
					split.sawTool = true
					index++
					out = append(out, piece{
						call:      &parsedCall{Name: call.Function.Name, Args: call.Function.Arguments},
						callIndex: index, callID: firstNonEmpty(call.ID, "call_"+hexID()),
					})
				} else if call.Function.Arguments != "" {
					out = append(out, piece{isArgs: true, argsIndex: index, argsText: call.Function.Arguments})
				}
			}
			if chunk.Choices[0].FinishReason != "" {
				finish = chunk.Choices[0].FinishReason
			}
		}
		if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
			usage = append(json.RawMessage(nil), chunk.Usage...)
		}
	}
	flush()
	stop := "stop"
	if split.sawTool {
		stop = "tool_calls"
	} else if finish == "length" {
		stop = "length"
	}
	out = append(out, piece{isStop: true, stop: stop, usage: normalizeUsage(usage)})
	return out
}

const httpOK = 200

func normalizeUsage(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		PromptDetails    *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionDetails *struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	}
	if json.Unmarshal(raw, &usage) != nil {
		return nil
	}
	out := map[string]any{
		"prompt_tokens": usage.PromptTokens, "completion_tokens": usage.CompletionTokens,
		"total_tokens": usage.PromptTokens + usage.CompletionTokens,
	}
	if usage.PromptDetails != nil && usage.PromptDetails.CachedTokens != 0 {
		out["prompt_tokens_details"] = map[string]int{"cached_tokens": usage.PromptDetails.CachedTokens}
	}
	if usage.CompletionDetails != nil && usage.CompletionDetails.ReasoningTokens != 0 {
		out["completion_tokens_details"] = map[string]int{"reasoning_tokens": usage.CompletionDetails.ReasoningTokens}
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return encoded
}

func openAICompletion(model, id string, created int64, events []piece) ([]byte, int, error) {
	for _, event := range events {
		if event.isErr {
			return nil, failureStatus(event.errStatus, event.errText), &upstreamError{status: failureStatus(event.errStatus, event.errText), summary: failureMessage(event.errStatus, event.errText)}
		}
	}
	message := map[string]any{"role": "assistant", "content": ""}
	var reasoning string
	var calls []any
	stop := "stop"
	var usage json.RawMessage
	saw := false
	for _, event := range events {
		if event.text != "" {
			saw = true
			message["content"] = message["content"].(string) + event.text
		}
		if event.reasoning != "" {
			saw = true
			reasoning += event.reasoning
		}
		if event.call != nil {
			saw = true
			calls = append(calls, map[string]any{
				"id": event.callID, "type": "function",
				"function": map[string]string{"name": event.call.Name, "arguments": event.call.Args},
			})
		}
		if event.isArgs && len(calls) > 0 {
			last := calls[len(calls)-1].(map[string]any)
			fn := last["function"].(map[string]string)
			fn["arguments"] += event.argsText
			last["function"] = fn
		}
		if event.isStop {
			stop = event.stop
			usage = event.usage
		}
	}
	if !saw {
		return nil, http.StatusBadGateway, &upstreamError{status: http.StatusBadGateway, summary: "Qoder ended without an answer"}
	}
	if reasoning != "" {
		message["reasoning_content"] = reasoning
	}
	if len(calls) > 0 {
		message["tool_calls"] = calls
	}
	body := map[string]any{
		"id": id, "object": "chat.completion", "created": created, "model": model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": stop}},
	}
	if len(usage) > 0 {
		body["usage"] = json.RawMessage(usage)
	}
	raw, err := json.Marshal(body)
	return raw, httpOK, err
}

func streamChunks(model, id string, created int64, events []piece) ([][]byte, int, error) {
	if len(events) > 0 && events[0].isErr {
		event := events[0]
		return nil, failureStatus(event.errStatus, event.errText), &upstreamError{status: failureStatus(event.errStatus, event.errText), summary: failureMessage(event.errStatus, event.errText)}
	}
	var chunks [][]byte
	chunks = append(chunks, chunkJSON(id, created, model, map[string]any{"role": "assistant", "content": ""}, nil, nil))
	for _, event := range events {
		switch {
		case event.isErr:
			raw, _ := json.Marshal(map[string]any{"error": map[string]any{"message": failureMessage(event.errStatus, event.errText), "code": failureStatus(event.errStatus, event.errText)}})
			chunks = append(chunks, raw)
			return chunks, httpOK, nil
		case event.text != "":
			chunks = append(chunks, chunkJSON(id, created, model, map[string]any{"content": event.text}, nil, nil))
		case event.reasoning != "":
			chunks = append(chunks, chunkJSON(id, created, model, map[string]any{"reasoning_content": event.reasoning}, nil, nil))
		case event.call != nil:
			chunks = append(chunks, chunkJSON(id, created, model, map[string]any{
				"tool_calls": []any{map[string]any{
					"index": event.callIndex, "id": event.callID, "type": "function",
					"function": map[string]string{"name": event.call.Name, "arguments": event.call.Args},
				}},
			}, nil, nil))
		case event.isArgs:
			chunks = append(chunks, chunkJSON(id, created, model, map[string]any{
				"tool_calls": []any{map[string]any{"index": event.argsIndex, "function": map[string]string{"arguments": event.argsText}}},
			}, nil, nil))
		case event.isStop:
			var extra map[string]any
			if len(event.usage) > 0 {
				extra = map[string]any{"usage": json.RawMessage(event.usage)}
			}
			chunks = append(chunks, chunkJSON(id, created, model, map[string]any{}, &event.stop, extra))
		}
	}
	chunks = append(chunks, []byte("[DONE]"))
	return chunks, httpOK, nil
}

func chunkJSON(id string, created int64, model string, delta map[string]any, finish *string, extra map[string]any) []byte {
	choice := map[string]any{"index": 0, "delta": delta, "finish_reason": nil}
	if finish != nil {
		choice["finish_reason"] = *finish
	}
	body := map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []any{choice},
	}
	for key, value := range extra {
		body[key] = value
	}
	raw, _ := json.Marshal(body)
	return raw
}

func failureStatus(status int, text string) int {
	if status < 400 || status > 599 {
		status = 502
	}
	message := failureMessage(status, text)
	if status == 401 || status == 403 {
		return 401
	}
	if status == 429 || strings.Contains(strings.ToLower(message), "quota") {
		return 429
	}
	return status
}

func failureMessage(status int, text string) string {
	if status == 401 || status == 403 {
		return "the sign-in lapsed — sign in again"
	}
	message := strings.TrimSpace(text)
	var decoded struct {
		Message string          `json:"message"`
		Details json.RawMessage `json:"details"`
	}
	if json.Unmarshal([]byte(text), &decoded) == nil {
		if decoded.Message != "" {
			message = decoded.Message
		}
		details := decoded.Details
		if len(details) > 0 && details[0] == '"' {
			var inner string
			if json.Unmarshal(details, &inner) == nil {
				details = json.RawMessage(inner)
			}
		}
		var wrapped struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(details, &wrapped) == nil && wrapped.Error.Message != "" {
			message += ": " + wrapped.Error.Message
		}
	}
	if message == "" {
		message = "HTTP " + itoa(status)
	}
	if status == 429 || strings.Contains(strings.ToLower(message), "quota") {
		return "usage limit reached: " + message
	}
	return message
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func indexOf(values []string, want string) int {
	for i, value := range values {
		if value == want {
			return i
		}
	}
	return -1
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func itoa(value int) string { return strconv.Itoa(value) }
