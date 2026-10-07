package degrade

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"
)

// The captured Codex context is the first turn of a codex-tui thread on Responses
// Lite, taken from haowang02/cpa-plugin-codex-candy-eval. A bare
// {"model","input":[{"role","content"}]} request makes Codex answer in a few
// tokens and never emit the number sequence ModelTrace scores.
//
//go:embed codex_context.json
var codexContextJSON []byte

type codexContextFile struct {
	Tools            json.RawMessage `json:"tools"`
	BaseInstructions string          `json:"base_instructions"`
	Messages         []struct {
		Role    string `json:"role"`
		Content []struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"messages"`
}

func mustCodexContext() (codexContextFile, []byte) {
	var decoded codexContextFile
	if err := json.Unmarshal(codexContextJSON, &decoded); err != nil {
		panic(err)
	}
	var tools bytes.Buffer
	if err := json.Compact(&tools, decoded.Tools); err != nil {
		panic(err)
	}
	return decoded, tools.Bytes()
}

var (
	codexContext, codexTools = mustCodexContext()
	codexEnvironment         = "<environment_context>\n  <cwd>/home/user/workspace</cwd>\n  <shell>bash</shell>\n  <current_date>%s</current_date>\n  <timezone>Etc/UTC</timezone>\n</environment_context>"
)

// CodexTurn is the Responses body the CPA plugin sends for one ModelTrace prompt.
func CodexTurn(model, prompt string, now time.Time) ([]byte, error) {
	input := make([]any, 0, 2+len(codexContext.Messages)+2)
	input = append(input,
		map[string]any{"type": "additional_tools", "role": "developer", "tools": json.RawMessage(codexTools)},
		codexInputMessage("developer", [][2]string{{"model.base_instructions", codexContext.BaseInstructions}}),
	)
	for _, message := range codexContext.Messages {
		parts := make([][2]string, 0, len(message.Content))
		for _, part := range message.Content {
			parts = append(parts, [2]string{part.Kind, part.Text})
		}
		input = append(input, codexInputMessage(message.Role, parts))
	}
	input = append(input,
		codexInputMessage("user", [][2]string{{"environments.environment_context", fmt.Sprintf(codexEnvironment, now.UTC().Format(time.DateOnly))}}),
		codexInputMessage("user", [][2]string{{"user.text", prompt}}),
	)
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	encoder.SetEscapeHTML(false)
	err := encoder.Encode(map[string]any{
		"model":               model,
		"input":               input,
		"tool_choice":         "auto",
		"parallel_tool_calls": false,
		"store":               false,
		"stream":              true,
		"include":             []string{"reasoning.encrypted_content"},
		"text":                map[string]string{"verbosity": "low"},
	})
	return bytes.TrimSuffix(body.Bytes(), []byte("\n")), err
}

func codexInputMessage(role string, parts [][2]string) map[string]any {
	content := make([]map[string]string, 0, len(parts))
	kinds := make([]string, 0, len(parts))
	for _, part := range parts {
		content = append(content, map[string]string{"type": "input_text", "text": part[1]})
		kinds = append(kinds, part[0])
	}
	return map[string]any{
		"type":    "message",
		"role":    role,
		"content": content,
		"internal_chat_message_metadata_passthrough": map[string]any{"content_item_kinds": kinds},
	}
}
