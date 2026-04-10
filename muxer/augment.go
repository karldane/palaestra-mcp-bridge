package muxer

// AugmentSpec describes a single bridge-level parameter to inject into tool inputSchemas.
// Add new entries to the spec list to inject additional required fields into all
// non-exempt tools without any new code paths.
type AugmentSpec struct {
	// Name is the argument name injected into inputSchema.properties (e.g. "justification").
	Name string
	// Type is the JSON Schema type string (e.g. "string").
	Type string
	// Description is the human-readable description shown to LLM clients.
	Description string
	// MinLength, if > 0, is added as "minLength" in the property schema.
	MinLength int
	// Required, if true, adds the field to inputSchema.required.
	Required bool
	// ExemptBackends is the set of backend IDs for which this spec is not injected.
	ExemptBackends map[string]bool
}

// AugmentToolList injects bridge-level parameters described by specs into every tool's
// inputSchema. Tools belonging to an exempt backend are skipped for that spec.
// backendID is the ID of the backend that owns the tool being processed.
//
// This function is pure — it has no side effects and makes no DB calls.
func AugmentToolList(tools []map[string]interface{}, backendID string, specs []AugmentSpec) {
	for _, tool := range tools {
		schema, ok := tool["inputSchema"].(map[string]interface{})
		if !ok {
			schema = map[string]interface{}{"type": "object"}
			tool["inputSchema"] = schema
		}

		props, ok := schema["properties"].(map[string]interface{})
		if !ok {
			props = make(map[string]interface{})
			schema["properties"] = props
		}

		required, _ := schema["required"].([]interface{})

		for _, spec := range specs {
			if spec.ExemptBackends[backendID] {
				continue
			}

			prop := map[string]interface{}{
				"type":        spec.Type,
				"description": spec.Description,
			}
			if spec.MinLength > 0 {
				prop["minLength"] = spec.MinLength
			}
			props[spec.Name] = prop

			if spec.Required {
				// Only add if not already present
				alreadyRequired := false
				for _, r := range required {
					if r == spec.Name {
						alreadyRequired = true
						break
					}
				}
				if !alreadyRequired {
					required = append(required, spec.Name)
				}
			}
		}

		if len(required) > 0 {
			schema["required"] = required
		}
	}
}
