package shared

import (
	"testing"
)

func TestIsSystemTool_known(t *testing.T) {
	if !IsSystemTool("mcpbridge_ping") {
		t.Error("expected mcpbridge_ping to be a system tool")
	}
	if !IsSystemTool("mcpbridge_0_README") {
		t.Error("expected mcpbridge_0_README to be a system tool")
	}
	if !IsSystemTool("mcpbridge_version") {
		t.Error("expected mcpbridge_version to be a system tool")
	}
	if !IsSystemTool("mcpbridge_list_backends") {
		t.Error("expected mcpbridge_list_backends to be a system tool")
	}
	if !IsSystemTool("mcpbridge_refresh_tools") {
		t.Error("expected mcpbridge_refresh_tools to be a system tool")
	}
	if !IsSystemTool("mcpbridge_pool_status") {
		t.Error("expected mcpbridge_pool_status to be a system tool")
	}
	if !IsSystemTool("mcpbridge_capabilities") {
		t.Error("expected mcpbridge_capabilities to be a system tool")
	}
	if !IsSystemTool("mcpbridge_approval_status") {
		t.Error("expected mcpbridge_approval_status to be a system tool")
	}
}

func TestIsSystemTool_unknown(t *testing.T) {
	if IsSystemTool("some_backend_tool") {
		t.Error("expected some_backend_tool NOT to be a system tool")
	}
	if IsSystemTool("") {
		t.Error("expected empty string NOT to be a system tool")
	}
	if IsSystemTool("mcpbridge") {
		t.Error("expected 'mcpbridge' without suffix NOT to be a system tool")
	}
}

func TestSystemToolsAsMap_count(t *testing.T) {
	result := SystemToolsAsMap()
	if len(result) != len(SystemTools) {
		t.Errorf("expected %d tools, got %d", len(SystemTools), len(result))
	}
}

func TestSystemToolsAsMap_keys(t *testing.T) {
	result := SystemToolsAsMap()
	for i, tool := range result {
		if tool["name"] != SystemTools[i].Name {
			t.Errorf("tool[%d] name = %v, want %s", i, tool["name"], SystemTools[i].Name)
		}
		if tool["description"] != SystemTools[i].Description {
			t.Errorf("tool[%d] description mismatch", i)
		}
		if tool["inputSchema"] == nil {
			t.Errorf("tool[%d] inputSchema is nil", i)
		}
	}
}

func TestSystemToolsAsMap_roundtrip(t *testing.T) {
	result := SystemToolsAsMap()
	for _, tool := range result {
		name, ok := tool["name"].(string)
		if !ok {
			t.Errorf("expected name to be string, got %T", tool["name"])
		}
		if !IsSystemTool(name) {
			t.Errorf("%q should be identified as system tool via IsSystemTool", name)
		}
	}
}

func TestSystemToolNames_populated(t *testing.T) {
	if len(SystemToolNames) != len(SystemTools) {
		t.Errorf("SystemToolNames has %d entries, expected %d", len(SystemToolNames), len(SystemTools))
	}
	for _, st := range SystemTools {
		if !SystemToolNames[st.Name] {
			t.Errorf("SystemToolNames missing entry for %s", st.Name)
		}
	}
}
