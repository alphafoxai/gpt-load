// Package responsealias rewrites provider model identifiers back to the
// client-visible model without changing the surrounding response protocol.
package responsealias

import (
	"bytes"
	"encoding/json"
	"fmt"

	"gpt-load/internal/dialect"
	"gpt-load/internal/protocol"
)

// Needs reports whether a selected upstream model differs from the model used
// by the client.
func Needs(clientModel, upstreamModel string) bool {
	return clientModel != "" && upstreamModel != "" && clientModel != upstreamModel
}

// RewriteJSON rewrites model fields in one protocol response object.
func RewriteJSON(clientProtocol protocol.Protocol, body []byte, clientModel string) ([]byte, error) {
	rewriter, err := modelRewriter(clientProtocol)
	if err != nil {
		return nil, err
	}
	rewritten, err := rewriter.RewriteResponseModel(body, clientModel)
	if err != nil {
		return nil, fmt.Errorf("rewrite response model: %w", err)
	}
	return rewritten, nil
}

// RewriteSSE rewrites every complete SSE event in data while preserving event
// names, comments, line endings and terminal markers. A final event without a
// blank-line delimiter is also accepted because some upstream bridges emit one
// logical event per chunk without retaining the delimiter.
func RewriteSSE(clientProtocol protocol.Protocol, data []byte, clientModel string) ([]byte, error) {
	var output bytes.Buffer
	remaining := data
	for len(remaining) > 0 {
		index, delimiterLength := firstSSEDelimiter(remaining)
		if index < 0 {
			rewritten, err := rewriteSSEEvent(clientProtocol, remaining, clientModel)
			if err != nil {
				return nil, err
			}
			_, _ = output.Write(rewritten)
			break
		}
		eventEnd := index + delimiterLength
		rewritten, err := rewriteSSEEvent(clientProtocol, remaining[:eventEnd], clientModel)
		if err != nil {
			return nil, err
		}
		_, _ = output.Write(rewritten)
		remaining = remaining[eventEnd:]
	}
	return output.Bytes(), nil
}

// StripUnrewritableModel drops model identifiers from a complete SSE event that
// could not be rewritten to the client model. Invalid JSON is not forwarded.
func StripUnrewritableModel(_ protocol.Protocol, data []byte) ([]byte, bool) {
	var output bytes.Buffer
	remaining := data
	forwarded := false
	for len(remaining) > 0 {
		index, delimiterLength := firstSSEDelimiter(remaining)
		event := remaining
		if index >= 0 {
			event = remaining[:index+delimiterLength]
			remaining = remaining[index+delimiterLength:]
		} else {
			remaining = nil
		}
		stripped, ok := stripSSEEventModel(event)
		if !ok {
			continue
		}
		_, _ = output.Write(stripped)
		forwarded = true
	}
	if !forwarded {
		return nil, false
	}
	return output.Bytes(), true
}

func stripSSEEventModel(event []byte) ([]byte, bool) {
	lines := splitSSELines(event)
	dataValues := make([][]byte, 0, 1)
	firstDataLine := -1
	for index := range lines {
		if !lines[index].isData {
			continue
		}
		if firstDataLine < 0 {
			firstDataLine = index
		}
		dataValues = append(dataValues, lines[index].data)
	}
	if firstDataLine < 0 {
		return bytes.Clone(event), true
	}
	payload := bytes.Join(dataValues, []byte{'\n'})
	if len(payload) == 0 || bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		return bytes.Clone(event), true
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(payload, &object) != nil || object == nil {
		return nil, false
	}
	delete(object, "model")
	delete(object, "modelVersion")
	if raw, exists := object["response"]; exists {
		var nested map[string]json.RawMessage
		if json.Unmarshal(raw, &nested) != nil {
			delete(object, "response")
		} else {
			delete(nested, "model")
			if encoded, err := json.Marshal(nested); err == nil {
				object["response"] = encoded
			}
		}
	}
	if raw, exists := object["message"]; exists {
		var nested map[string]json.RawMessage
		if json.Unmarshal(raw, &nested) != nil {
			delete(object, "message")
		} else {
			delete(nested, "model")
			if encoded, err := json.Marshal(nested); err == nil {
				object["message"] = encoded
			}
		}
	}
	rewritten, err := json.Marshal(object)
	if err != nil {
		return nil, false
	}
	var output bytes.Buffer
	for index, line := range lines {
		switch {
		case index == firstDataLine:
			_, _ = output.WriteString("data: ")
			_, _ = output.Write(rewritten)
			_, _ = output.Write(line.terminator)
		case line.isData:
			continue
		default:
			_, _ = output.Write(line.content)
			_, _ = output.Write(line.terminator)
		}
	}
	return output.Bytes(), true
}

func modelRewriter(clientProtocol protocol.Protocol) (dialect.ModelRewriter, error) {
	switch clientProtocol {
	case protocol.OpenAICompletions:
		return dialect.NewOpenAI(), nil
	case protocol.OpenAIResponses:
		return dialect.NewOpenAIResponses(), nil
	case protocol.OpenAIImages:
		return dialect.NewOpenAIImages(), nil
	case protocol.Rerank:
		return dialect.NewRerank(), nil
	case protocol.Decisions:
		return dialect.NewDecisions(), nil
	case protocol.OpenAIEmbeddings:
		return dialect.NewOpenAIEmbeddings(), nil
	case protocol.Mistral:
		return dialect.NewMistral(), nil
	case protocol.Anthropic:
		return dialect.NewAnthropic(), nil
	case protocol.Gemini:
		return dialect.NewGemini(), nil
	case protocol.GeminiEmbeddings:
		return dialect.NewGeminiEmbeddings(), nil
	default:
		return nil, fmt.Errorf("unsupported client protocol")
	}
}

func rewriteSSEEvent(clientProtocol protocol.Protocol, event []byte, clientModel string) ([]byte, error) {
	lines := splitSSELines(event)
	dataValues := make([][]byte, 0, 1)
	firstDataLine := -1
	for index := range lines {
		if !lines[index].isData {
			continue
		}
		if firstDataLine < 0 {
			firstDataLine = index
		}
		dataValues = append(dataValues, lines[index].data)
	}
	if firstDataLine < 0 {
		return bytes.Clone(event), nil
	}
	payload := bytes.Join(dataValues, []byte{'\n'})
	if len(payload) == 0 || bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		return bytes.Clone(event), nil
	}
	if !json.Valid(payload) {
		return nil, fmt.Errorf("rewrite response model: invalid event JSON")
	}
	if !sseEventCarriesModel(payload) {
		return bytes.Clone(event), nil
	}
	rewritten, err := RewriteJSON(clientProtocol, payload, clientModel)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(rewritten, payload) {
		return bytes.Clone(event), nil
	}

	var output bytes.Buffer
	output.Grow(len(event) - len(payload) + len(rewritten))
	for index, line := range lines {
		switch {
		case index == firstDataLine:
			_, _ = output.WriteString("data: ")
			_, _ = output.Write(rewritten)
			_, _ = output.Write(line.terminator)
		case line.isData:
			continue
		default:
			_, _ = output.Write(line.content)
			_, _ = output.Write(line.terminator)
		}
	}
	return output.Bytes(), nil
}

func firstSSEDelimiter(data []byte) (int, int) {
	indexLF := bytes.Index(data, []byte("\n\n"))
	indexCRLF := bytes.Index(data, []byte("\r\n\r\n"))
	switch {
	case indexLF < 0 && indexCRLF < 0:
		return -1, 0
	case indexLF < 0:
		return indexCRLF, 4
	case indexCRLF < 0:
		return indexLF, 2
	case indexCRLF < indexLF:
		return indexCRLF, 4
	default:
		return indexLF, 2
	}
}

type sseLine struct {
	content    []byte
	terminator []byte
	isData     bool
	data       []byte
}

func splitSSELines(event []byte) []sseLine {
	lines := make([]sseLine, 0, 4)
	for start := 0; start < len(event); {
		end := start
		for end < len(event) && event[end] != '\n' && event[end] != '\r' {
			end++
		}
		terminatorEnd := end
		if terminatorEnd < len(event) {
			terminatorEnd++
			if event[end] == '\r' && terminatorEnd < len(event) && event[terminatorEnd] == '\n' {
				terminatorEnd++
			}
		}
		line := sseLine{content: event[start:end], terminator: event[end:terminatorEnd]}
		line.isData, line.data = parseSSEDataLine(line.content)
		lines = append(lines, line)
		start = terminatorEnd
	}
	return lines
}

func parseSSEDataLine(line []byte) (bool, []byte) {
	if len(line) == 0 || line[0] == ':' {
		return false, nil
	}
	separator := bytes.IndexByte(line, ':')
	field := line
	var value []byte
	if separator >= 0 {
		field = line[:separator]
		value = line[separator+1:]
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
	}
	if !bytes.Equal(field, []byte("data")) {
		return false, nil
	}
	return true, value
}

// sseEventCarriesModel reports whether a valid JSON object can carry a provider
// model id. Deltas without one pass through unchanged.
func sseEventCarriesModel(payload []byte) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(payload, &object) != nil || object == nil {
		return false
	}
	for _, field := range []string{"model", "modelVersion", "response", "message"} {
		if _, exists := object[field]; exists {
			return true
		}
	}
	return false
}
