package shared

import (
	"encoding/json"
	"fmt"
)

// ParseArgsMap attempts to parse a value as map[string]interface{}.
// It handles:
//   - map[string]interface{}: the standard case — returned as-is
//   - string: a JSON-encoded object (some MCP clients double-encode nested
//     objects).  Unmarshalled into map[string]interface{} transparently.
//   - nil: returns an error explaining the field is missing.
//   - any other type: returns an error with the actual type so callers can
//     report a clear diagnostic to the end user.
func ParseArgsMap(v interface{}) (map[string]interface{}, error) {
	switch val := v.(type) {
	case map[string]interface{}:
		return val, nil
	case string:
		var result map[string]interface{}
		if err := json.Unmarshal([]byte(val), &result); err != nil {
			return nil, fmt.Errorf("params was a JSON string but could not be parsed: %w", err)
		}
		return result, nil
	case nil:
		return nil, fmt.Errorf("params is missing (nil)")
	default:
		return nil, fmt.Errorf("params has unexpected type %T (expected a JSON object)", v)
	}
}
