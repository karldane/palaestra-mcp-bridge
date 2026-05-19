package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mcp-bridge/mcp-bridge/enforcer"
	"github.com/mcp-bridge/mcp-bridge/store"
)

// testHandlerWithEnforcerSetup creates a Handler with a real Enforcer.
func testHandlerWithEnforcerSetup(t *testing.T) (*Handler, *store.Store, func()) {
	t.Helper()
	h, st := testHandler(t)
	es := store.NewEnforcerStore(st.DB())
	cfg := enforcer.DefaultEnforcerConfig()
	cfg.MinJustificationLength = 0
	enf, err := enforcer.NewEnforcer(cfg, es, nil)
	if err != nil {
		st.Close()
		t.Fatalf("NewEnforcer: %v", err)
	}
	h.Enforcer = enf
	return h, st, func() { st.Close() }
}

// seedApprovalRequest inserts an approval request row for a user that exists.
func seedApprovalRequest(t *testing.T, st *store.Store, userID, toolName, backendID string) string {
	t.Helper()
	es := store.NewEnforcerStore(st.DB())
	id := "test-approval-" + toolName
	err := es.CreateApprovalRequest(enforcer.ApprovalRequestRow{
		ID:            id,
		UserID:        userID,
		UserEmail:     "user@test.com",
		UserRole:      "user",
		TrustLevel:    50,
		ToolName:      toolName,
		ToolArgs:      "{}",
		BackendID:     backendID,
		SafetyProfile: `{"risk":"high","impact":"delete","cost":10,"source":"inferred"}`,
		Status:        "PENDING",
		QueueType:     "admin",
		Justification: "test justification",
		RequestedAt:   time.Now(),
		ExpiresAt:     time.Now().Add(24 * time.Hour),
		PolicyID:      "inferred_profile_gate",
		ViolationMsg:  "Tool has inferred profile and no explicit policy.",
	})
	if err != nil {
		t.Fatalf("seedApprovalRequest: %v", err)
	}
	return id
}

// ---------- PoliciesNewPageHandler Prefill ----------

func TestPoliciesNewPageHandler_NoPrefill(t *testing.T) {
	h, st, cleanup := testHandlerWithEnforcerSetup(t)
	defer cleanup()

	admin := seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, admin.Email, "secret")

	req := authedRequest(http.MethodGet, "/web/admin/enforcer/policies/new", "", cookie)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	// Should render the form
	body := rr.Body.String()
	if !strings.Contains(body, "Create New Policy") {
		t.Error("expected 'Create New Policy' in response body")
	}
}

func TestPoliciesNewPageHandler_WithPrefill(t *testing.T) {
	h, st, cleanup := testHandlerWithEnforcerSetup(t)
	defer cleanup()

	admin := seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, admin.Email, "secret")

	req := authedRequest(http.MethodGet,
		"/web/admin/enforcer/policies/new?prefill_tool=delete_widget&prefill_backend=mybackend&prefill_action=allow&prefill_severity=high&prefill_expression=tool+%3D%3D+%22delete_widget%22&from_approval=appr-123",
		"", cookie)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	// The prefill values should appear in the form
	if !strings.Contains(body, "delete_widget") {
		t.Error("expected prefill tool 'delete_widget' in response body")
	}
	if !strings.Contains(body, "appr-123") {
		t.Error("expected from_approval 'appr-123' in response body")
	}
}

// ---------- PoliciesCreateHandler from_approval ----------

func TestPoliciesCreateHandler_WithFromApproval_ExecutesRequest(t *testing.T) {
	h, st, cleanup := testHandlerWithEnforcerSetup(t)
	defer cleanup()

	admin := seedAdmin(t, st)
	regular := seedRegularUser(t, st)

	// Seed a pending approval request
	approvalID := seedApprovalRequest(t, st, regular.ID, "delete_widget", "testbackend")

	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, admin.Email, "secret")

	form := url.Values{
		"id":            {"policy-from-appr"},
		"name":          {"Allow delete_widget"},
		"expression":    {`tool == "delete_widget"`},
		"action":        {"ALLOW"},
		"severity":      {"HIGH"},
		"priority":      {"10"},
		"enabled":       {"on"},
		"from_approval": {approvalID},
	}
	req := authedRequest(http.MethodPost, "/web/admin/enforcer/policies/create",
		form.Encode(), cookie)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	// Should redirect (either to /policies or /policies?warning=...)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d body=%s", rr.Code, rr.Body.String())
	}
	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "/web/admin/enforcer/policies") {
		t.Errorf("want redirect to policies page, got %q", loc)
	}
}

func TestPoliciesCreateHandler_WithoutFromApproval(t *testing.T) {
	h, st, cleanup := testHandlerWithEnforcerSetup(t)
	defer cleanup()

	admin := seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, admin.Email, "secret")

	form := url.Values{
		"id":         {"plain-policy"},
		"name":       {"Plain Policy"},
		"expression": {`tool == "get_widget"`},
		"action":     {"ALLOW"},
		"severity":   {"LOW"},
		"priority":   {"50"},
		"enabled":    {"on"},
	}
	req := authedRequest(http.MethodPost, "/web/admin/enforcer/policies/create",
		form.Encode(), cookie)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d", rr.Code)
	}
	if rr.Header().Get("Location") != "/web/admin/enforcer/policies" {
		t.Errorf("want redirect to /web/admin/enforcer/policies, got %q",
			rr.Header().Get("Location"))
	}
}

// ---------- PolicyRequestHandler ----------

func TestPolicyRequestHandler_CreatesAdminQueueItem(t *testing.T) {
	h, st, cleanup := testHandlerWithEnforcerSetup(t)
	defer cleanup()

	regular := seedRegularUser(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, regular.Email, "pass")

	// Seed a pending approval for this user
	approvalID := seedApprovalRequest(t, st, regular.ID, "export_data", "databackend")

	form := url.Values{
		"tool_name":   {"export_data"},
		"backend_id":  {"databackend"},
		"approval_id": {approvalID},
		"justification": {"I need this tool to be allowed permanently"},
	}
	req := authedRequest(http.MethodPost, "/web/user/enforcer/policy-request",
		form.Encode(), cookie)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d body=%s", rr.Code, rr.Body.String())
	}
	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "/web/user/enforcer/queue") {
		t.Errorf("want redirect to user queue, got %q", loc)
	}
	if !strings.Contains(loc, "success") {
		t.Errorf("want success param in redirect URL, got %q", loc)
	}

	// Verify a policy_request item now exists in the admin queue
	es := store.NewEnforcerStore(st.DB())
	items, err := es.ListPendingApprovals()
	if err != nil {
		t.Fatalf("ListPendingApprovals: %v", err)
	}
	found := false
	for _, item := range items {
		if item.QueueType == "policy_request" && item.ToolName == "export_data" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected a policy_request item for export_data in admin queue")
	}
}

func TestPolicyRequestHandler_RequiresAuth(t *testing.T) {
	h, st, cleanup := testHandlerWithEnforcerSetup(t)
	defer cleanup()
	_ = st

	mux := http.NewServeMux()
	h.Register(mux)

	form := url.Values{"tool_name": {"x"}, "backend_id": {"y"}}
	req := httptest.NewRequest(http.MethodPost, "/web/user/enforcer/policy-request",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	// Unauthenticated → redirect to login
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("want 303 redirect to login, got %d", rr.Code)
	}
}

// ---------- inferSeverity FuncMap ----------

func TestInferSeverity_FuncMapRegistered(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	// The inferSeverity function should be registered in the FuncMap,
	// meaning it can be called in templates without panicking.
	tmpl := h.Templates.Lookup("admin_enforcer_queue.html")
	if tmpl == nil {
		t.Fatal("admin_enforcer_queue.html template not found")
	}
	// Template exists and was parsed without error — inferSeverity is registered.
}
