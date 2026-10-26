package mirasim

import "sort"

var schemaMapKeywords = [...]string{
	"$defs",
	"definitions",
	"patternProperties",
	"dependentSchemas",
	"dependencies",
}

var schemaValueKeywords = [...]string{
	"items",
	"prefixItems",
	"contains",
	"additionalProperties",
	"propertyNames",
	"unevaluatedProperties",
	"unevaluatedItems",
	"additionalItems",
	"contentSchema",
	"anyOf",
	"oneOf",
	"allOf",
	"not",
	"if",
	"then",
	"else",
}

// applyCodexStructuredOutput 让 Responses json_schema 满足 OpenAI Structured
// Outputs：properties 里的每个字段都必须出现在 required 中。Mira 的 gpt-*
// 上游按这个规则拒请求，缺一项就会报 Missing '<name>'。
func applyCodexStructuredOutput(payload map[string]any) {
	text, _ := payload["text"].(map[string]any)
	if text == nil {
		return
	}
	format, _ := text["format"].(map[string]any)
	if format == nil {
		return
	}
	if typeOf(format["type"]) != "json_schema" {
		return
	}
	completeOpenAIStrictSchema(format["schema"])
}

func completeOpenAIStrictSchema(schema any) {
	switch node := schema.(type) {
	case []any:
		for _, child := range node {
			completeOpenAIStrictSchema(child)
		}
	case map[string]any:
		if props, ok := node["properties"].(map[string]any); ok {
			present := make(map[string]struct{})
			required := stringSlice(node["required"])
			for _, name := range required {
				present[name] = struct{}{}
			}
			missing := make([]string, 0)
			for name, sub := range props {
				completeOpenAIStrictSchema(sub)
				if _, ok := present[name]; ok {
					continue
				}
				missing = append(missing, name)
				props[name] = makeSchemaNullable(sub)
			}
			if len(missing) > 0 {
				sort.Strings(missing)
				node["required"] = appendStrings(required, missing)
			}
		}
		for _, key := range schemaMapKeywords {
			child, ok := node[key].(map[string]any)
			if !ok {
				continue
			}
			for name, sub := range child {
				completeOpenAIStrictSchema(sub)
				child[name] = sub
			}
		}
		for _, key := range schemaValueKeywords {
			completeOpenAIStrictSchema(node[key])
		}
	}
}

func makeSchemaNullable(schema any) any {
	node, ok := schema.(map[string]any)
	if !ok {
		return schema
	}
	switch typed := node["type"].(type) {
	case string:
		if typed != "null" {
			node["type"] = []any{typed, "null"}
		}
	case []any:
		for _, item := range typed {
			if typeOf(item) == "null" {
				return node
			}
		}
		node["type"] = append(append([]any{}, typed...), "null")
	}
	return node
}

func typeOf(value any) string {
	text, _ := value.(string)
	return text
}

func stringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok || text == "" {
			continue
		}
		out = append(out, text)
	}
	return out
}

func appendStrings(existing []string, extra []string) []any {
	out := make([]any, 0, len(existing)+len(extra))
	for _, name := range existing {
		out = append(out, name)
	}
	for _, name := range extra {
		out = append(out, name)
	}
	return out
}
