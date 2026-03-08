package muxer

import (
	"encoding/json"
	"testing"

	"github.com/mcp-bridge/mcp-bridge/config"
	"github.com/mcp-bridge/mcp-bridge/credential"
	"github.com/mcp-bridge/mcp-bridge/poolmgr"
)

func makeTestConfig() *config.InternalConfig {
	return &config.InternalConfig{
		Server: config.ServerConfig{
			Port:     "8080",
			LogLevel: "info",
		},
		Backends: map[string]config.BackendConfig{
			"jira": {
				Command:    "cat",
				PoolSize:   1,
				ToolPrefix: "jira",
				Secrets: []config.SecretRef{
					{Name: "jira-token", EnvKey: "JIRA_API_TOKEN", Context: "user"},
				},
			},
			"confluence": {
				Command:    "cat",
				PoolSize:   1,
				ToolPrefix: "confluence",
			},
		},
	}
}

func makeSingleBackendConfig() *config.InternalConfig {
	return &config.InternalConfig{
		Server: config.ServerConfig{
			Port:     "8080",
			LogLevel: "info",
		},
		Backends: map[string]config.BackendConfig{
			"echo": {
				Command:  "cat",
				PoolSize: 1,
			},
		},
	}
}

func TestNewToolMuxer(t *testing.T) {
	cfg := makeTestConfig()
	pm := poolmgr.NewPoolManager("cat", 1)
	defer pm.ShutdownAll()
	secrets := credential.NewMockSecretStore()

	tm := NewToolMuxer(pm, secrets, cfg)
	if tm == nil {
		t.Fatal("expected non-nil ToolMuxer")
	}
}

func TestToolMuxer_GetPrefixForBackend(t *testing.T) {
	cfg := makeTestConfig()
	pm := poolmgr.NewPoolManager("cat", 1)
	defer pm.ShutdownAll()
	secrets := credential.NewMockSecretStore()

	tm := NewToolMuxer(pm, secrets, cfg)

	if p := tm.GetPrefixForBackend("jira"); p != "jira" {
		t.Errorf("expected prefix jira, got %s", p)
	}

	if p := tm.GetPrefixForBackend("confluence"); p != "confluence" {
		t.Errorf("expected prefix confluence, got %s", p)
	}

	if p := tm.GetPrefixForBackend("nonexistent"); p != "" {
		t.Errorf("expected empty prefix for nonexistent backend, got %s", p)
	}
}

func TestPoolRouter_StripPrefix(t *testing.T) {
	tests := []struct {
		name        string
		prefix      string
		shouldStrip bool
		toolName    string
		expected    string
	}{
		{
			name:        "strip underscore prefix",
			prefix:      "jira",
			shouldStrip: true,
			toolName:    "jira_create_issue",
			expected:    "create_issue",
		},
		{
			name:        "strip slash prefix",
			prefix:      "jira",
			shouldStrip: true,
			toolName:    "jira/create_issue",
			expected:    "create_issue",
		},
		{
			name:        "no strip when disabled",
			prefix:      "jira",
			shouldStrip: false,
			toolName:    "jira_create_issue",
			expected:    "jira_create_issue",
		},
		{
			name:        "no strip when no prefix",
			prefix:      "",
			shouldStrip: true,
			toolName:    "create_issue",
			expected:    "create_issue",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pr := &PoolRouter{
				Prefix:      tc.prefix,
				ShouldStrip: tc.shouldStrip,
			}

			body := makeToolsCallBody(tc.toolName)
			result := pr.StripPrefix(body)

			var parsed map[string]interface{}
			if err := json.Unmarshal(result, &parsed); err != nil {
				t.Fatalf("failed to parse result: %v", err)
			}

			params := parsed["params"].(map[string]interface{})
			tools := params["tools"].([]interface{})
			tool := tools[0].(map[string]interface{})
			name := tool["name"].(string)

			if name != tc.expected {
				t.Errorf("expected tool name %s, got %s", tc.expected, name)
			}
		})
	}
}

func TestPoolRouter_AddPrefix(t *testing.T) {
	tests := []struct {
		name     string
		prefix   string
		toolName string
		expected string
	}{
		{
			name:     "add prefix",
			prefix:   "jira",
			toolName: "create_issue",
			expected: "jira_create_issue",
		},
		{
			name:     "no prefix",
			prefix:   "",
			toolName: "create_issue",
			expected: "create_issue",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pr := &PoolRouter{
				Prefix: tc.prefix,
			}

			body := makeToolsCallBody(tc.toolName)
			result := pr.AddPrefix(body)

			var parsed map[string]interface{}
			if err := json.Unmarshal(result, &parsed); err != nil {
				t.Fatalf("failed to parse result: %v", err)
			}

			params := parsed["params"].(map[string]interface{})
			tools := params["tools"].([]interface{})
			tool := tools[0].(map[string]interface{})
			name := tool["name"].(string)

			if name != tc.expected {
				t.Errorf("expected tool name %s, got %s", tc.expected, name)
			}
		})
	}
}

func TestPoolRouter_StripPrefix_InvalidJSON(t *testing.T) {
	pr := &PoolRouter{
		Prefix:      "jira",
		ShouldStrip: true,
	}

	invalidBody := []byte(`{invalid json}`)
	result := pr.StripPrefix(invalidBody)

	// Should return original body on error
	if string(result) != string(invalidBody) {
		t.Error("expected original body returned on invalid JSON")
	}
}

func TestPoolRouter_AddPrefix_InvalidJSON(t *testing.T) {
	pr := &PoolRouter{
		Prefix: "jira",
	}

	invalidBody := []byte(`{invalid json}`)
	result := pr.AddPrefix(invalidBody)

	if string(result) != string(invalidBody) {
		t.Error("expected original body returned on invalid JSON")
	}
}

func TestToolMuxer_FindBackendSingleBackend(t *testing.T) {
	cfg := makeSingleBackendConfig()
	pm := poolmgr.NewPoolManager("cat", 1)
	defer pm.ShutdownAll()
	secrets := credential.NewMockSecretStore()

	tm := NewToolMuxer(pm, secrets, cfg)

	body := makeToolsCallBody("any_tool_name")
	_, router, err := tm.HandleToolsCall("user1", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if router.Backend != "echo" {
		t.Errorf("expected backend echo, got %s", router.Backend)
	}

	if router.ShouldStrip {
		t.Error("expected ShouldStrip false for single backend without prefix")
	}
}

func TestToolMuxer_FindBackendByPrefix(t *testing.T) {
	cfg := makeTestConfig()
	pm := poolmgr.NewPoolManager("cat", 1)
	defer pm.ShutdownAll()
	secrets := credential.NewMockSecretStore()

	tm := NewToolMuxer(pm, secrets, cfg)

	body := makeToolsCallBody("jira_create_issue")
	_, router, err := tm.HandleToolsCall("user1", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if router.Backend != "jira" {
		t.Errorf("expected backend jira, got %s", router.Backend)
	}

	if !router.ShouldStrip {
		t.Error("expected ShouldStrip true for prefixed tool")
	}
}

func TestToolMuxer_FindBackendBySlashPrefix(t *testing.T) {
	cfg := makeTestConfig()
	pm := poolmgr.NewPoolManager("cat", 1)
	defer pm.ShutdownAll()
	secrets := credential.NewMockSecretStore()

	tm := NewToolMuxer(pm, secrets, cfg)

	body := makeToolsCallBody("confluence/get_page")
	_, router, err := tm.HandleToolsCall("user1", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if router.Backend != "confluence" {
		t.Errorf("expected backend confluence, got %s", router.Backend)
	}
}

func TestToolMuxer_NoBackendFound(t *testing.T) {
	cfg := makeTestConfig()
	pm := poolmgr.NewPoolManager("cat", 1)
	defer pm.ShutdownAll()
	secrets := credential.NewMockSecretStore()

	tm := NewToolMuxer(pm, secrets, cfg)

	body := makeToolsCallBody("unknown_tool")
	_, _, err := tm.HandleToolsCall("user1", body)
	if err == nil {
		t.Error("expected error for unknown tool with multiple backends")
	}
}

func TestToolMuxer_HandleToolsCall_InvalidJSON(t *testing.T) {
	cfg := makeTestConfig()
	pm := poolmgr.NewPoolManager("cat", 1)
	defer pm.ShutdownAll()
	secrets := credential.NewMockSecretStore()

	tm := NewToolMuxer(pm, secrets, cfg)

	_, _, err := tm.HandleToolsCall("user1", []byte(`{invalid}`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestToolMuxer_HandleToolsCall_EmptyTools(t *testing.T) {
	cfg := makeTestConfig()
	pm := poolmgr.NewPoolManager("cat", 1)
	defer pm.ShutdownAll()
	secrets := credential.NewMockSecretStore()

	tm := NewToolMuxer(pm, secrets, cfg)

	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"tools":[]}}`)
	_, _, err := tm.HandleToolsCall("user1", body)
	if err == nil {
		t.Error("expected error for empty tools list")
	}
}

func TestToolMuxer_SecretInjection(t *testing.T) {
	cfg := makeTestConfig()
	pm := poolmgr.NewPoolManager("cat", 1)
	defer pm.ShutdownAll()
	secrets := credential.NewMockSecretStore()

	secrets.Set("user1", "JIRA_API_TOKEN", "test-token-123")

	tm := NewToolMuxer(pm, secrets, cfg)

	body := makeToolsCallBody("jira_create_issue")
	_, router, err := tm.HandleToolsCall("user1", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if router.Backend != "jira" {
		t.Errorf("expected backend jira, got %s", router.Backend)
	}

	if !router.Dedicated {
		t.Error("expected Dedicated=true for user pool")
	}
}

// ---------- BuildEnvForUser ----------

func TestToolMuxer_BuildEnvForUser_LegacySecrets(t *testing.T) {
	cfg := makeTestConfig()
	pm := poolmgr.NewPoolManager("cat", 1)
	defer pm.ShutdownAll()
	secrets := credential.NewMockSecretStore()

	secrets.Set("user1", "JIRA_API_TOKEN", "tok-abc")

	tm := NewToolMuxer(pm, secrets, cfg)

	env := tm.BuildEnvForUser("user1", "jira")

	// Should contain the injected token.
	found := false
	for _, e := range env {
		if e == "JIRA_API_TOKEN=tok-abc" {
			found = true
		}
	}
	if !found {
		t.Error("BuildEnvForUser did not include JIRA_API_TOKEN from legacy secrets")
	}
}

func TestToolMuxer_BuildEnvForUser_StaticEnv(t *testing.T) {
	cfg := &config.InternalConfig{
		Server: config.ServerConfig{Port: "8080"},
		Backends: map[string]config.BackendConfig{
			"jira": {
				Command:  "cat",
				PoolSize: 1,
				Env: map[string]string{
					"ATLASSIAN_DOMAIN": "myco.atlassian.net",
				},
			},
		},
	}
	pm := poolmgr.NewPoolManager("cat", 1)
	defer pm.ShutdownAll()

	tm := NewToolMuxer(pm, credential.NewMockSecretStore(), cfg)

	env := tm.BuildEnvForUser("user1", "jira")

	found := false
	for _, e := range env {
		if e == "ATLASSIAN_DOMAIN=myco.atlassian.net" {
			found = true
		}
	}
	if !found {
		t.Error("BuildEnvForUser did not include static env ATLASSIAN_DOMAIN")
	}
}

func TestToolMuxer_BuildEnvForUser_InheritsProcessEnv(t *testing.T) {
	cfg := makeSingleBackendConfig()
	pm := poolmgr.NewPoolManager("cat", 1)
	defer pm.ShutdownAll()

	tm := NewToolMuxer(pm, credential.NewMockSecretStore(), cfg)

	env := tm.BuildEnvForUser("user1", "echo")

	// Should contain at least PATH from the bridge's own env.
	found := false
	for _, e := range env {
		if len(e) > 5 && e[:5] == "PATH=" {
			found = true
		}
	}
	if !found {
		t.Error("BuildEnvForUser did not inherit PATH from bridge environment")
	}
}

func makeToolsCallBody(toolName string) []byte {
	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"tools": []map[string]interface{}{
				{
					"name":      toolName,
					"arguments": map[string]interface{}{},
				},
			},
		},
	}

	data, _ := json.Marshal(msg)
	return data
}
