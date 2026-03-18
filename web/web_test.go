package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mcp-bridge/mcp-bridge/store"
)

// testHandler creates a Handler backed by a temp SQLite DB and the real
// templates directory. The caller must close the store when done.
func testHandler(t *testing.T) (*Handler, *store.Store) {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	st, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}

	// Resolve templates directory relative to this test file.
	// web/web_test.go -> templates/ (one level up)
	templateDir := filepath.Join("..", "templates")
	if _, err := os.Stat(filepath.Join(templateDir, "_base.html")); err != nil {
		t.Fatalf("cannot find templates dir at %s: %v", templateDir, err)
	}

	h, err := NewHandler(st, templateDir)
	if err != nil {
		st.Close()
		t.Fatalf("NewHandler: %v", err)
	}
	return h, st
}

// seedAdmin creates an admin user and returns it.
func seedAdmin(t *testing.T, st *store.Store) *store.User {
	t.Helper()
	u := &store.User{
		Name:     "Admin",
		Email:    "admin@test.com",
		Password: "secret",
		Role:     "admin",
	}
	if err := st.CreateUser(u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return u
}

// seedUser creates a regular user and returns it.
func seedRegularUser(t *testing.T, st *store.Store) *store.User {
	t.Helper()
	u := &store.User{
		Name:     "User",
		Email:    "user@test.com",
		Password: "pass",
		Role:     "user",
	}
	if err := st.CreateUser(u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return u
}

// loginCookie performs a POST login and returns the session cookie.
func loginCookie(t *testing.T, h *Handler, mux *http.ServeMux, email, password string) *http.Cookie {
	t.Helper()
	form := url.Values{"email": {email}, "password": {password}}
	req := httptest.NewRequest(http.MethodPost, "/web/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: expected 303, got %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatal("login: no session cookie set")
	return nil
}

// authedRequest creates an HTTP request with the session cookie attached.
func authedRequest(method, target string, body string, cookie *http.Cookie) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.AddCookie(cookie)
	return r
}

// ---------- Tests ----------

func TestNewHandler_ParsesTemplates(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	if h.Templates == nil {
		t.Fatal("expected non-nil Templates")
	}
	// Verify known templates are parsed
	for _, name := range []string{"login.html", "dashboard.html", "tokens.html", "password.html", "admin_users.html", "admin_backends.html"} {
		if h.Templates.Lookup(name) == nil {
			t.Errorf("missing template: %s", name)
		}
	}
}

func TestNewHandler_BadDir(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	_, err = NewHandler(st, "/nonexistent/templates")
	if err == nil {
		t.Fatal("expected error for bad template dir")
	}
}

func TestLoginPage_GET(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	mux := http.NewServeMux()
	h.Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/web/login", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Sign In") {
		t.Error("expected login page to contain 'Sign In'")
	}
}

func TestLogin_Success(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)

	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")
	if cookie.Value == "" {
		t.Fatal("expected non-empty session token")
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)

	form := url.Values{"email": {"admin@test.com"}, "password": {"wrong"}}
	req := httptest.NewRequest(http.MethodPost, "/web/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (re-rendered form), got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Invalid email or password") {
		t.Error("expected error message in response")
	}
}

func TestLogin_UnknownEmail(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	mux := http.NewServeMux()
	h.Register(mux)

	form := url.Values{"email": {"nobody@test.com"}, "password": {"x"}}
	req := httptest.NewRequest(http.MethodPost, "/web/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Invalid email or password") {
		t.Error("expected error message")
	}
}

func TestLogin_MethodNotAllowed(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	mux := http.NewServeMux()
	h.Register(mux)

	req := httptest.NewRequest(http.MethodPut, "/web/login", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestLogout(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)

	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	// Logout
	req := httptest.NewRequest(http.MethodGet, "/web/logout", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Location") != "/web/login" {
		t.Errorf("expected redirect to /web/login, got %s", resp.Header.Get("Location"))
	}

	// Verify session cookie is cleared
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName && c.MaxAge < 0 {
			return // success
		}
	}
	t.Error("expected session cookie to be cleared")
}

func TestDashboard_RequiresAuth(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	mux := http.NewServeMux()
	h.Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/web/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", w.Code)
	}
	if w.Result().Header.Get("Location") != "/web/login" {
		t.Error("expected redirect to /web/login")
	}
}

func TestDashboard_ShowsBackends(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	user := seedAdmin(t, st)
	st.CreateBackend(&store.Backend{
		ID: "test-be", Command: "echo hello", PoolSize: 1, Env: "{}", Enabled: true,
	})
	st.SetUserToken(&store.UserToken{
		UserID: user.ID, BackendID: "test-be", EnvKey: "API_KEY", Value: "secret",
	})

	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	req := authedRequest(http.MethodGet, "/web/", "", cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "test-be") {
		t.Error("expected backend ID in dashboard")
	}
	if !strings.Contains(body, "API_KEY") {
		t.Error("expected configured token key in dashboard")
	}
}

func TestDashboard_HidesDisabledBackends(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	st.CreateBackend(&store.Backend{
		ID: "disabled-be", Command: "echo", PoolSize: 1, Env: "{}", Enabled: false,
	})

	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	req := authedRequest(http.MethodGet, "/web/", "", cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if strings.Contains(w.Body.String(), "disabled-be") {
		t.Error("disabled backend should not appear on dashboard")
	}
}

// ---------- Tokens ----------

func TestTokensPage_SelectsFirstBackend(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	st.CreateBackend(&store.Backend{
		ID: "alpha", Command: "echo", PoolSize: 1, Env: "{}", Enabled: true,
	})

	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	req := authedRequest(http.MethodGet, "/web/tokens", "", cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "alpha") {
		t.Error("expected first backend to be shown")
	}
}

func TestTokensSave_And_Delete(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	user := seedAdmin(t, st)
	st.CreateBackend(&store.Backend{
		ID: "be1", Command: "echo", PoolSize: 1, Env: "{}", Enabled: true,
	})

	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	// Save a token
	form := url.Values{
		"backend_id": {"be1"},
		"env_key":    {"MY_SECRET"},
		"value":      {"12345"},
	}
	req := authedRequest(http.MethodPost, "/web/tokens/save", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("save: expected 303, got %d", w.Code)
	}

	// Verify token in DB
	tokens, _ := st.GetUserTokens(user.ID, "be1")
	if len(tokens) != 1 || tokens[0].EnvKey != "MY_SECRET" {
		t.Fatalf("expected 1 token MY_SECRET, got %v", tokens)
	}

	// Delete the token
	form = url.Values{
		"backend_id": {"be1"},
		"env_key":    {"MY_SECRET"},
	}
	req = authedRequest(http.MethodPost, "/web/tokens/delete", form.Encode(), cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("delete: expected 303, got %d", w.Code)
	}

	tokens, _ = st.GetUserTokens(user.ID, "be1")
	if len(tokens) != 0 {
		t.Fatalf("expected 0 tokens after delete, got %d", len(tokens))
	}
}

func TestTokensSave_MissingFields(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	form := url.Values{
		"backend_id": {"be1"},
		"env_key":    {""},
		"value":      {""},
	}
	req := authedRequest(http.MethodPost, "/web/tokens/save", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}
	loc := w.Result().Header.Get("Location")
	if !strings.Contains(loc, "error=") {
		t.Error("expected error in redirect location")
	}
}

func TestTokensSave_MethodNotAllowed(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	req := authedRequest(http.MethodGet, "/web/tokens/save", "", cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

// ---------- Password ----------

func TestPasswordChange_Success(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	form := url.Values{
		"current_password": {"secret"},
		"new_password":     {"newpass"},
		"confirm_password": {"newpass"},
	}
	req := authedRequest(http.MethodPost, "/web/password", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Password updated") {
		t.Error("expected success message")
	}

	// Verify password changed in DB
	u, _ := st.GetUserByEmail("admin@test.com")
	if !store.IsBcrypt(u.Password) {
		t.Errorf("expected bcrypt hash, got '%s'", u.Password)
	}
	if store.CheckPassword(u.Password, "newpass") != nil {
		t.Error("CheckPassword failed for updated password")
	}
}

func TestPasswordChange_WrongCurrent(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	form := url.Values{
		"current_password": {"wrongpass"},
		"new_password":     {"newpass"},
		"confirm_password": {"newpass"},
	}
	req := authedRequest(http.MethodPost, "/web/password", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), "Current password is incorrect") {
		t.Error("expected error about wrong current password")
	}
}

func TestPasswordChange_Mismatch(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	form := url.Values{
		"current_password": {"secret"},
		"new_password":     {"a"},
		"confirm_password": {"b"},
	}
	req := authedRequest(http.MethodPost, "/web/password", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), "do not match") {
		t.Error("expected mismatch error")
	}
}

func TestPasswordChange_EmptyNew(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	form := url.Values{
		"current_password": {"secret"},
		"new_password":     {""},
		"confirm_password": {""},
	}
	req := authedRequest(http.MethodPost, "/web/password", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), "cannot be empty") {
		t.Error("expected empty password error")
	}
}

func TestPasswordPage_GET(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	req := authedRequest(http.MethodGet, "/web/password", "", cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Change Password") {
		t.Error("expected password change form")
	}
}

// ---------- Admin: Users ----------

func TestAdminUsers_RequiresAdmin(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedRegularUser(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "user@test.com", "pass")

	req := authedRequest(http.MethodGet, "/web/admin/users", "", cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestAdminUsers_ListsUsers(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	seedRegularUser(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	req := authedRequest(http.MethodGet, "/web/admin/users", "", cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "admin@test.com") {
		t.Error("expected admin email in list")
	}
	if !strings.Contains(body, "user@test.com") {
		t.Error("expected user email in list")
	}
}

func TestAdminUsers_Create(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	form := url.Values{
		"name":     {"New User"},
		"email":    {"new@test.com"},
		"password": {"pw123"},
		"role":     {"user"},
	}
	req := authedRequest(http.MethodPost, "/web/admin/users/create", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}

	u, err := st.GetUserByEmail("new@test.com")
	if err != nil {
		t.Fatalf("new user not found: %v", err)
	}
	if u.Name != "New User" || u.Role != "user" {
		t.Errorf("unexpected user: %+v", u)
	}
}

func TestAdminUsers_Create_MissingEmail(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	form := url.Values{"name": {"X"}, "email": {""}, "password": {"pw"}, "role": {"user"}}
	req := authedRequest(http.MethodPost, "/web/admin/users/create", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	loc := w.Result().Header.Get("Location")
	if !strings.Contains(loc, "error=") {
		t.Error("expected error redirect for missing email")
	}
}

func TestAdminUsers_Create_InvalidRole(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	form := url.Values{
		"name": {"X"}, "email": {"inv@test.com"}, "password": {"pw"}, "role": {"superadmin"},
	}
	req := authedRequest(http.MethodPost, "/web/admin/users/create", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// Should default to "user" role
	u, _ := st.GetUserByEmail("inv@test.com")
	if u.Role != "user" {
		t.Errorf("expected role 'user', got '%s'", u.Role)
	}
}

func TestAdminUsers_Delete(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	target := seedRegularUser(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	form := url.Values{"user_id": {target.ID}}
	req := authedRequest(http.MethodPost, "/web/admin/users/delete", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}

	_, err := st.GetUserByEmail("user@test.com")
	if err == nil {
		t.Fatal("user should have been deleted")
	}
}

func TestAdminUsers_CannotDeleteSelf(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	admin := seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	form := url.Values{"user_id": {admin.ID}}
	req := authedRequest(http.MethodPost, "/web/admin/users/delete", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	loc := w.Result().Header.Get("Location")
	if !strings.Contains(loc, "error=") {
		t.Error("expected error when deleting self")
	}

	// Admin should still exist
	_, err := st.GetUserByEmail("admin@test.com")
	if err != nil {
		t.Fatal("admin should not have been deleted")
	}
}

func TestAdminUsers_Delete_MethodNotAllowed(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	req := authedRequest(http.MethodGet, "/web/admin/users/delete", "", cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

// ---------- Admin: Backends ----------

func TestAdminBackends_ListsBackends(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	st.CreateBackend(&store.Backend{
		ID: "be-alpha", Command: "cmd-a", PoolSize: 2, Env: "{}", Enabled: true,
	})
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	req := authedRequest(http.MethodGet, "/web/admin/backends", "", cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "be-alpha") {
		t.Error("expected backend in list")
	}
}

func TestAdminBackends_RequiresAdmin(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedRegularUser(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "user@test.com", "pass")

	req := authedRequest(http.MethodGet, "/web/admin/backends", "", cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestAdminBackends_Create(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	form := url.Values{
		"id":            {"new-be"},
		"command":       {"echo hello"},
		"min_pool_size": {"3"},
		"max_pool_size": {"5"},
		"tool_prefix":   {"pref"},
		"env":           {`{"KEY":"val"}`},
		"enabled":       {"on"},
	}
	req := authedRequest(http.MethodPost, "/web/admin/backends/create", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}

	b, err := st.GetBackend("new-be")
	if err != nil {
		t.Fatalf("backend not found: %v", err)
	}
	if b.Command != "echo hello" || b.MinPoolSize != 3 || b.MaxPoolSize != 5 || b.ToolPrefix != "pref" || !b.Enabled {
		t.Errorf("unexpected backend: %+v", b)
	}
}

func TestAdminBackends_Create_MissingFields(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	form := url.Values{"id": {""}, "command": {""}}
	req := authedRequest(http.MethodPost, "/web/admin/backends/create", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	loc := w.Result().Header.Get("Location")
	if !strings.Contains(loc, "error=") {
		t.Error("expected error redirect for missing fields")
	}
}

func TestAdminBackends_Create_DefaultValues(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	form := url.Values{
		"id":        {"def-be"},
		"command":   {"cmd"},
		"pool_size": {"0"}, // invalid -> defaults to 1
		"env":       {""},  // empty -> defaults to "{}"
		// "enabled" omitted -> false
	}
	req := authedRequest(http.MethodPost, "/web/admin/backends/create", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	b, _ := st.GetBackend("def-be")
	if b.PoolSize != 1 {
		t.Errorf("expected pool_size 1, got %d", b.PoolSize)
	}
	if b.Env != "{}" {
		t.Errorf("expected env '{}', got '%s'", b.Env)
	}
	if b.Enabled {
		t.Error("expected enabled=false when checkbox omitted")
	}
}

func TestAdminBackends_Edit(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	st.CreateBackend(&store.Backend{
		ID: "edit-be", Command: "old-cmd", PoolSize: 1, Env: "{}", Enabled: true,
	})

	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	form := url.Values{
		"id":            {"edit-be"},
		"command":       {"new-cmd"},
		"min_pool_size": {"5"},
		"max_pool_size": {"10"},
		"tool_prefix":   {"new-pref"},
		"env":           {`{"A":"B"}`},
		"enabled":       {"on"},
	}
	req := authedRequest(http.MethodPost, "/web/admin/backends/edit", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}

	b, _ := st.GetBackend("edit-be")
	if b.Command != "new-cmd" || b.MinPoolSize != 5 || b.MaxPoolSize != 10 || b.ToolPrefix != "new-pref" {
		t.Errorf("unexpected backend after edit: %+v", b)
	}
}

func TestAdminBackends_Delete(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	st.CreateBackend(&store.Backend{
		ID: "del-be", Command: "cmd", PoolSize: 1, Env: "{}", Enabled: true,
	})

	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	form := url.Values{"id": {"del-be"}}
	req := authedRequest(http.MethodPost, "/web/admin/backends/delete", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}

	_, err := st.GetBackend("del-be")
	if err == nil {
		t.Fatal("backend should have been deleted")
	}
}

func TestAdminBackends_Delete_MethodNotAllowed(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	req := authedRequest(http.MethodGet, "/web/admin/backends/delete", "", cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

// ---------- parseInt ----------

func TestParseInt(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"0", 0},
		{"1", 1},
		{"42", 42},
		{"123", 123},
		{"", 0},
		{"abc", 0},
		{"12x", 0},
	}
	for _, tc := range tests {
		got := parseInt(tc.input)
		if got != tc.want {
			t.Errorf("parseInt(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

// ---------- OnBackendChange callback ----------

func TestAdminBackends_Create_CallsOnBackendChange(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	var calledWith string
	h.OnBackendChange = func(backendID string) {
		calledWith = backendID
	}

	form := url.Values{
		"id":      {"cb-be"},
		"command": {"echo"},
		"enabled": {"on"},
	}
	req := authedRequest(http.MethodPost, "/web/admin/backends/create", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}
	if calledWith != "cb-be" {
		t.Errorf("OnBackendChange called with %q, want %q", calledWith, "cb-be")
	}
}

func TestAdminBackends_Edit_CallsOnBackendChange(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	st.CreateBackend(&store.Backend{
		ID: "edit-cb", Command: "old", PoolSize: 1, Env: "{}", Enabled: true,
	})

	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	var calledWith string
	h.OnBackendChange = func(backendID string) {
		calledWith = backendID
	}

	form := url.Values{
		"id":      {"edit-cb"},
		"command": {"new"},
		"enabled": {"on"},
	}
	req := authedRequest(http.MethodPost, "/web/admin/backends/edit", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}
	if calledWith != "edit-cb" {
		t.Errorf("OnBackendChange called with %q, want %q", calledWith, "edit-cb")
	}
}

func TestAdminBackends_Delete_CallsOnBackendChange(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	st.CreateBackend(&store.Backend{
		ID: "del-cb", Command: "cmd", PoolSize: 1, Env: "{}", Enabled: true,
	})

	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	var calledWith string
	h.OnBackendChange = func(backendID string) {
		calledWith = backendID
	}

	form := url.Values{"id": {"del-cb"}}
	req := authedRequest(http.MethodPost, "/web/admin/backends/delete", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}
	if calledWith != "del-cb" {
		t.Errorf("OnBackendChange called with %q, want %q", calledWith, "del-cb")
	}
}

func TestAdminBackends_NilCallback_DoesNotPanic(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	// OnBackendChange is nil — should not panic
	h.OnBackendChange = nil

	form := url.Values{
		"id":      {"nil-cb"},
		"command": {"echo"},
	}
	req := authedRequest(http.MethodPost, "/web/admin/backends/create", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}
}

// ---------- Admin: Probe Backend ----------

func TestAdminBackends_Probe_MethodNotAllowed(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	req := authedRequest(http.MethodGet, "/web/admin/backends/probe", "", cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestAdminBackends_Probe_NilCallback(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	// OnProbeBackend is nil — should return 501
	h.OnProbeBackend = nil

	form := url.Values{"id": {"some-be"}}
	req := authedRequest(http.MethodPost, "/web/admin/backends/probe", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", w.Code)
	}
}

func TestAdminBackends_Probe_MissingID(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	h.OnProbeBackend = func(backendID string) ([]byte, error) {
		return nil, nil
	}

	form := url.Values{"id": {""}}
	req := authedRequest(http.MethodPost, "/web/admin/backends/probe", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "error" {
		t.Errorf("expected status=error, got %s", resp["status"])
	}
}

func TestAdminBackends_Probe_BackendNotFound(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	h.OnProbeBackend = func(backendID string) ([]byte, error) {
		return nil, nil
	}

	form := url.Values{"id": {"nonexistent"}}
	req := authedRequest(http.MethodPost, "/web/admin/backends/probe", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestAdminBackends_Probe_RequiresAdmin(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedRegularUser(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "user@test.com", "pass")

	form := url.Values{"id": {"some-be"}}
	req := authedRequest(http.MethodPost, "/web/admin/backends/probe", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestAdminBackends_Probe_Success(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	st.CreateBackend(&store.Backend{
		ID: "probe-be", Command: "cat", PoolSize: 1, Env: "{}", Enabled: true,
	})

	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	// Mock the probe callback
	h.OnProbeBackend = func(backendID string) ([]byte, error) {
		result := map[string]interface{}{
			"status":      "ok",
			"message":     "MCP handshake succeeded",
			"stderr":      "",
			"duration_ms": 42,
		}
		return json.Marshal(result)
	}

	form := url.Values{"id": {"probe-be"}}
	req := authedRequest(http.MethodPost, "/web/admin/backends/probe", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", resp["status"])
	}
}

func TestAdminBackends_Probe_CallbackError(t *testing.T) {
	h, st := testHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	st.CreateBackend(&store.Backend{
		ID: "err-be", Command: "cat", PoolSize: 1, Env: "{}", Enabled: true,
	})

	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	h.OnProbeBackend = func(backendID string) ([]byte, error) {
		return nil, fmt.Errorf("probe failed internally")
	}

	form := url.Values{"id": {"err-be"}}
	req := authedRequest(http.MethodPost, "/web/admin/backends/probe", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "error" {
		t.Errorf("expected status=error, got %s", resp["status"])
	}
	if !strings.Contains(resp["message"], "probe failed") {
		t.Errorf("expected error message, got %s", resp["message"])
	}
}
