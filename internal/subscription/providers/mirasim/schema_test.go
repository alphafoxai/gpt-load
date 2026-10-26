package mirasim

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCompleteOpenAIStrictSchemaAddsMissingRequired(t *testing.T) {
	schema := decodeSchema(t, `{
		"type":"object",
		"properties":{
			"answer":{"type":"string"},
			"impossible":{"type":"boolean"}
		},
		"required":["answer"]
	}`)
	completeOpenAIStrictSchema(schema)
	got := requiredNames(t, schema)
	if !reflect.DeepEqual(got, []string{"answer", "impossible"}) {
		t.Fatalf("required = %#v", got)
	}
	if !reflect.DeepEqual(propertyType(t, schema, "impossible"), []any{"boolean", "null"}) {
		t.Fatalf("impossible type = %#v", propertyType(t, schema, "impossible"))
	}
	if propertyType(t, schema, "answer") != "string" {
		t.Fatalf("answer type changed: %#v", propertyType(t, schema, "answer"))
	}
}

func TestCompleteOpenAIStrictSchemaKeepsSatisfiedRequired(t *testing.T) {
	schema := decodeSchema(t, `{
		"type":"object",
		"properties":{"impossible":{"type":"boolean"}},
		"required":["impossible"]
	}`)
	completeOpenAIStrictSchema(schema)
	if !reflect.DeepEqual(requiredNames(t, schema), []string{"impossible"}) {
		t.Fatalf("required = %#v", requiredNames(t, schema))
	}
	if propertyType(t, schema, "impossible") != "boolean" {
		t.Fatalf("already required field became nullable: %#v", propertyType(t, schema, "impossible"))
	}
}

func TestCompleteOpenAIStrictSchemaWalksNestedObjects(t *testing.T) {
	schema := decodeSchema(t, `{
		"type":"object",
		"properties":{
			"nested":{
				"type":"object",
				"properties":{"impossible":{"type":"string"}},
				"required":[]
			}
		},
		"required":["nested"]
	}`)
	completeOpenAIStrictSchema(schema)
	nested := schema.(map[string]any)["properties"].(map[string]any)["nested"]
	if !reflect.DeepEqual(requiredNames(t, nested), []string{"impossible"}) {
		t.Fatalf("nested required = %#v", requiredNames(t, nested))
	}
}

func decodeSchema(t *testing.T, raw string) any {
	t.Helper()
	var schema any
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		t.Fatal(err)
	}
	return schema
}

func requiredNames(t *testing.T, schema any) []string {
	t.Helper()
	node, ok := schema.(map[string]any)
	if !ok {
		t.Fatalf("schema type %T", schema)
	}
	return stringSlice(node["required"])
}

func propertyType(t *testing.T, schema any, name string) any {
	t.Helper()
	node := schema.(map[string]any)
	props := node["properties"].(map[string]any)
	field := props[name].(map[string]any)
	return field["type"]
}
