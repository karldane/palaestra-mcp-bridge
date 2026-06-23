package shared

import (
	"testing"
)

func TestParseArgsMap_standardMap(t *testing.T) {
	input := map[string]interface{}{
		"content": "test fact",
		"tags":    []interface{}{"go", "testing"},
	}
	result, err := ParseArgsMap(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["content"] != "test fact" {
		t.Errorf("content: got %v, want 'test fact'", result["content"])
	}
}

func TestParseArgsMap_jsonString(t *testing.T) {
	input := `{"content": "test fact", "confidence": 0.95}`
	result, err := ParseArgsMap(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["content"] != "test fact" {
		t.Errorf("content: got %v, want 'test fact'", result["content"])
	}
	if result["confidence"] != 0.95 {
		t.Errorf("confidence: got %v, want 0.95", result["confidence"])
	}
}

func TestParseArgsMap_invalidJSONString(t *testing.T) {
	input := `{"content": broken json`
	_, err := ParseArgsMap(input)
	if err == nil {
		t.Fatal("expected error for invalid JSON string, got nil")
	}
}

func TestParseArgsMap_nil(t *testing.T) {
	_, err := ParseArgsMap(nil)
	if err == nil {
		t.Fatal("expected error for nil input, got nil")
	}
}

func TestParseArgsMap_wrongType_int(t *testing.T) {
	_, err := ParseArgsMap(42)
	if err == nil {
		t.Fatal("expected error for int input, got nil")
	}
}

func TestParseArgsMap_wrongType_bool(t *testing.T) {
	_, err := ParseArgsMap(true)
	if err == nil {
		t.Fatal("expected error for bool input, got nil")
	}
}

func TestParseArgsMap_emptyMap(t *testing.T) {
	input := map[string]interface{}{}
	result, err := ParseArgsMap(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty map, got %v", result)
	}
}

func TestParseArgsMap_nestedObject(t *testing.T) {
	input := map[string]interface{}{
		"filter": map[string]interface{}{
			"memory_type": "semantic",
			"tags":        []interface{}{"important"},
		},
	}
	result, err := ParseArgsMap(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	filter, ok := result["filter"].(map[string]interface{})
	if !ok {
		t.Fatal("filter is not a map")
	}
	if filter["memory_type"] != "semantic" {
		t.Errorf("filter.memory_type: got %v, want 'semantic'", filter["memory_type"])
	}
}

func TestParseArgsMap_jsonStringNested(t *testing.T) {
	input := `{"query": "test", "limit": 5, "tags": ["important", "urgent"]}`
	result, err := ParseArgsMap(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["query"] != "test" {
		t.Errorf("query: got %v, want 'test'", result["query"])
	}
	if result["limit"] != float64(5) {
		t.Errorf("limit: got %v, want 5", result["limit"])
	}
}
