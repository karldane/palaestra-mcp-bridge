package muxer

import (
	"testing"
)

// makeToolWithSchema returns a tool map with a pre-populated inputSchema.
func makeToolWithSchema(name string, existingProps map[string]interface{}, existingRequired []interface{}) map[string]interface{} {
	schema := map[string]interface{}{
		"type":       "object",
		"properties": existingProps,
	}
	if len(existingRequired) > 0 {
		schema["required"] = existingRequired
	}
	return map[string]interface{}{
		"name":        name,
		"inputSchema": schema,
	}
}

// makeToolNoSchema returns a tool map without an inputSchema field.
func makeToolNoSchema(name string) map[string]interface{} {
	return map[string]interface{}{
		"name": name,
	}
}

// TestAugmentToolList_InjectsIntoExistingSchema verifies that a spec field is
// injected into a tool that already has an inputSchema.
func TestAugmentToolList_InjectsIntoExistingSchema(t *testing.T) {
	tools := []map[string]interface{}{
		makeToolWithSchema("my_tool", map[string]interface{}{
			"foo": map[string]interface{}{"type": "string"},
		}, nil),
	}

	specs := []AugmentSpec{{
		Name:        "justification",
		Type:        "string",
		Description: "why",
	}}

	AugmentToolList(tools, "backend1", specs)

	schema := tools[0]["inputSchema"].(map[string]interface{})
	props := schema["properties"].(map[string]interface{})

	if _, ok := props["justification"]; !ok {
		t.Fatal("expected 'justification' to be injected into properties")
	}
	// Original property should still be present
	if _, ok := props["foo"]; !ok {
		t.Fatal("expected existing property 'foo' to be preserved")
	}
}

// TestAugmentToolList_CreatesInputSchemaWhenAbsent verifies that when no
// inputSchema is present, one is created and populated.
func TestAugmentToolList_CreatesInputSchemaWhenAbsent(t *testing.T) {
	tools := []map[string]interface{}{
		makeToolNoSchema("bare_tool"),
	}

	specs := []AugmentSpec{{
		Name:        "justification",
		Type:        "string",
		Description: "explain yourself",
	}}

	AugmentToolList(tools, "b", specs)

	schema, ok := tools[0]["inputSchema"].(map[string]interface{})
	if !ok {
		t.Fatal("expected inputSchema to be created")
	}
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("expected properties to be created")
	}
	if _, ok := props["justification"]; !ok {
		t.Fatal("expected 'justification' to be present in created schema")
	}
}

// TestAugmentToolList_ExemptBackendSkipsInjection verifies that a tool whose
// backend is in ExemptBackends does not receive the spec field.
func TestAugmentToolList_ExemptBackendSkipsInjection(t *testing.T) {
	tools := []map[string]interface{}{
		makeToolWithSchema("exempt_tool", map[string]interface{}{}, nil),
	}

	specs := []AugmentSpec{{
		Name:           "justification",
		Type:           "string",
		Description:    "explain",
		ExemptBackends: map[string]bool{"qdrant": true},
	}}

	AugmentToolList(tools, "qdrant", specs)

	schema := tools[0]["inputSchema"].(map[string]interface{})
	props := schema["properties"].(map[string]interface{})
	if _, ok := props["justification"]; ok {
		t.Fatal("expected 'justification' NOT to be injected for exempt backend")
	}
}

// TestAugmentToolList_NonExemptBackendIsInjected verifies that a tool whose
// backend is NOT exempt does receive the injection.
func TestAugmentToolList_NonExemptBackendIsInjected(t *testing.T) {
	tools := []map[string]interface{}{
		makeToolWithSchema("normal_tool", map[string]interface{}{}, nil),
	}

	specs := []AugmentSpec{{
		Name:           "justification",
		Type:           "string",
		Description:    "explain",
		ExemptBackends: map[string]bool{"qdrant": true},
	}}

	AugmentToolList(tools, "github", specs)

	schema := tools[0]["inputSchema"].(map[string]interface{})
	props := schema["properties"].(map[string]interface{})
	if _, ok := props["justification"]; !ok {
		t.Fatal("expected 'justification' to be injected for non-exempt backend")
	}
}

// TestAugmentToolList_RequiredFieldAddedAndNotDuplicated verifies that a
// required spec field is added to the required array exactly once.
func TestAugmentToolList_RequiredFieldAddedAndNotDuplicated(t *testing.T) {
	// Start with no required array
	tools := []map[string]interface{}{
		makeToolWithSchema("t1", map[string]interface{}{}, nil),
	}

	specs := []AugmentSpec{{
		Name:     "justification",
		Type:     "string",
		Required: true,
	}}

	// Call twice to verify no duplication
	AugmentToolList(tools, "b", specs)
	AugmentToolList(tools, "b", specs)

	schema := tools[0]["inputSchema"].(map[string]interface{})
	required, _ := schema["required"].([]interface{})

	count := 0
	for _, r := range required {
		if r == "justification" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected 'justification' to appear exactly once in required, got %d", count)
	}
}

// TestAugmentToolList_RequiredFieldPreservesExisting verifies that existing
// required entries are kept when a new required field is added.
func TestAugmentToolList_RequiredFieldPreservesExisting(t *testing.T) {
	tools := []map[string]interface{}{
		makeToolWithSchema("t1", map[string]interface{}{
			"name": map[string]interface{}{"type": "string"},
		}, []interface{}{"name"}),
	}

	specs := []AugmentSpec{{
		Name:     "justification",
		Type:     "string",
		Required: true,
	}}

	AugmentToolList(tools, "b", specs)

	schema := tools[0]["inputSchema"].(map[string]interface{})
	required, _ := schema["required"].([]interface{})

	foundName := false
	foundJustification := false
	for _, r := range required {
		if r == "name" {
			foundName = true
		}
		if r == "justification" {
			foundJustification = true
		}
	}
	if !foundName {
		t.Error("expected existing required field 'name' to be preserved")
	}
	if !foundJustification {
		t.Error("expected 'justification' to be added to required")
	}
}

// TestAugmentToolList_MinLengthAppearsInPropertySchema verifies that MinLength
// is present in the property schema when > 0.
func TestAugmentToolList_MinLengthAppearsInPropertySchema(t *testing.T) {
	tools := []map[string]interface{}{
		makeToolWithSchema("t1", map[string]interface{}{}, nil),
	}

	specs := []AugmentSpec{{
		Name:      "justification",
		Type:      "string",
		MinLength: 40,
	}}

	AugmentToolList(tools, "b", specs)

	schema := tools[0]["inputSchema"].(map[string]interface{})
	props := schema["properties"].(map[string]interface{})
	prop, ok := props["justification"].(map[string]interface{})
	if !ok {
		t.Fatal("expected 'justification' property to exist")
	}
	minLen, ok := prop["minLength"].(int)
	if !ok {
		t.Fatalf("expected minLength to be int, got %T", prop["minLength"])
	}
	if minLen != 40 {
		t.Fatalf("expected minLength=40, got %d", minLen)
	}
}

// TestAugmentToolList_MinLengthZeroNotInjected verifies that when MinLength is
// 0, the minLength key is NOT added to the property schema.
func TestAugmentToolList_MinLengthZeroNotInjected(t *testing.T) {
	tools := []map[string]interface{}{
		makeToolWithSchema("t1", map[string]interface{}{}, nil),
	}

	specs := []AugmentSpec{{
		Name:      "justification",
		Type:      "string",
		MinLength: 0,
	}}

	AugmentToolList(tools, "b", specs)

	schema := tools[0]["inputSchema"].(map[string]interface{})
	props := schema["properties"].(map[string]interface{})
	prop, ok := props["justification"].(map[string]interface{})
	if !ok {
		t.Fatal("expected 'justification' property to exist")
	}
	if _, ok := prop["minLength"]; ok {
		t.Fatal("expected minLength to be absent when spec.MinLength == 0")
	}
}

// TestAugmentToolList_EmptySpecsIsNoOp verifies that passing an empty specs
// slice does not modify tools.
func TestAugmentToolList_EmptySpecsIsNoOp(t *testing.T) {
	tools := []map[string]interface{}{
		makeToolWithSchema("t1", map[string]interface{}{
			"arg": map[string]interface{}{"type": "string"},
		}, []interface{}{"arg"}),
	}

	AugmentToolList(tools, "b", nil)

	schema := tools[0]["inputSchema"].(map[string]interface{})
	props := schema["properties"].(map[string]interface{})
	if len(props) != 1 {
		t.Fatalf("expected 1 property, got %d", len(props))
	}
}
