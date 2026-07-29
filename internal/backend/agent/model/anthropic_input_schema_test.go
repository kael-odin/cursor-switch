package modeladapter

import (
	"encoding/json"
	"testing"
)

// TestAnthropicInputSchemaNil 覆盖 N-03：无参工具 parameters 为 nil 时
// input_schema 必须序列化为对象（至少 {"type":"object","properties":{}}），
// 而非 null，否则 Anthropic Messages API 返回 400。
func TestAnthropicInputSchemaNil(t *testing.T) {
	schema := anthropicInputSchema(nil)
	b, err := json.Marshal(struct {
		InputSchema map[string]any `json:"input_schema"`
	}{InputSchema: schema})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	if got == `{"input_schema":null}` {
		t.Fatalf("input_schema serialized as null → Anthropic 400. got: %s", got)
	}
	if schema["type"] != "object" {
		t.Fatalf("want type=object, got %v (full: %s)", schema["type"], got)
	}
	if _, ok := schema["properties"]; !ok {
		t.Fatalf("want properties key present, got: %s", got)
	}
}

// TestAnthropicInputSchemaStripsStrictFields 覆盖 N-23：剥离 OpenAI strict-schema
// 专属字段 $schema / strict（Anthropic 不识别），保留 additionalProperties。
func TestAnthropicInputSchemaStripsStrictFields(t *testing.T) {
	in := map[string]any{
		"$schema":              "https://example.com/schema.json",
		"strict":               true,
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"q": map[string]any{"type": "string"}},
	}
	out := anthropicInputSchema(in)
	if _, present := out["$schema"]; present {
		t.Fatalf("$schema should be stripped, got: %#v", out)
	}
	if _, present := out["strict"]; present {
		t.Fatalf("strict should be stripped, got: %#v", out)
	}
	if v, ok := out["additionalProperties"]; !ok || v != false {
		t.Fatalf("additionalProperties should be preserved as false, got: %#v", out)
	}
	if out["type"] != "object" {
		t.Fatalf("type should be object, got: %#v", out)
	}
	// 入参不应被修改。
	if _, present := in["$schema"]; !present {
		t.Fatalf("input map must not be mutated")
	}
}
