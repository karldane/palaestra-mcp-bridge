package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mcp-bridge/mcp-bridge/store"
)

// fakeMailer captures the messages passed to Send for assertions.
type fakeMailer struct {
	messages []fakeMessage
}

type fakeMessage struct {
	to      []string
	subject string
	body    string
}

func (f *fakeMailer) Send(to []string, subject, body string) error {
	f.messages = append(f.messages, fakeMessage{to: to, subject: subject, body: body})
	return nil
}

// inviteTestHandler builds a Handler pre-configured for invitation tests.
func inviteTestHandler(t *testing.T) (*Handler, *store.Store, *fakeMailer) {
	t.Helper()
	h, st := testHandler(t)
	h.Mailer = &fakeMailer{}
	h.PublicURL = "https://mcp.example.com"
	h.InviteExpiry = 7 * 24 * time.Hour
	h.AuthMode = "internal"
	h.MailerFrom = "noreply@tuskerdirect.com"
	return h, st, h.Mailer.(*fakeMailer)
}

func TestAdminInvites_RequiresAdmin(t *testing.T) {
	h, st, _ := inviteTestHandler(t)
	defer st.Close()

	seedRegularUser(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "user@test.com", "pass")

	req := authedRequest(http.MethodGet, "/web/admin/invites", "", cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestAdminInvites_ListsEmpty(t *testing.T) {
	h, st, _ := inviteTestHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	req := authedRequest(http.MethodGet, "/web/admin/invites", "", cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "No invitations yet") {
		t.Error("expected empty state message")
	}
}

func TestAdminInvites_Create_SendsEmail(t *testing.T) {
	h, st, mail := inviteTestHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	form := url.Values{
		"emails": {"karl.dane@tuskerdirect.com"},
		"name":   {"Karl Dane"},
		"role":   {"admin"},
	}
	req := authedRequest(http.MethodPost, "/web/admin/invites/create", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}
	if len(mail.messages) != 1 {
		t.Fatalf("expected 1 email, got %d", len(mail.messages))
	}
	msg := mail.messages[0]
	if len(msg.to) != 1 || msg.to[0] != "karl.dane@tuskerdirect.com" {
		t.Errorf("unexpected recipient: %v", msg.to)
	}
	if !strings.Contains(msg.body, "https://mcp.example.com/web/invite?token=inv_") {
		t.Errorf("email missing invite link: %s", msg.body)
	}
	if !strings.Contains(msg.body, "From: noreply@tuskerdirect.com") {
		t.Error("email missing From header")
	}

	invites, err := st.ListInvites()
	if err != nil {
		t.Fatalf("ListInvites: %v", err)
	}
	if len(invites) != 1 {
		t.Fatalf("expected 1 invite, got %d", len(invites))
	}
	if invites[0].Email != "karl.dane@tuskerdirect.com" {
		t.Errorf("expected invited email, got %s", invites[0].Email)
	}
	if invites[0].Role != "admin" {
		t.Errorf("expected role admin, got %s", invites[0].Role)
	}
}

func TestAdminInvites_Create_SkipsExistingUser(t *testing.T) {
	h, st, mail := inviteTestHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	seedRegularUser(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	form := url.Values{
		"emails": {"user@test.com"},
		"role":   {"user"},
	}
	req := authedRequest(http.MethodPost, "/web/admin/invites/create", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}
	if len(mail.messages) != 0 {
		t.Errorf("expected no email for existing user, got %d", len(mail.messages))
	}
}

func TestAdminInvites_Create_AllowsExistingUser_WhenFlagSet(t *testing.T) {
	h, st, mail := inviteTestHandler(t)
	defer st.Close()
	h.InviteAllowExisting = true

	seedAdmin(t, st)
	seedRegularUser(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	form := url.Values{
		"emails": {"user@test.com"},
		"name":   {"Existing User"},
		"role":   {"admin"},
	}
	req := authedRequest(http.MethodPost, "/web/admin/invites/create", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}
	if len(mail.messages) != 1 {
		t.Fatalf("expected email sent for existing user when flag set, got %d", len(mail.messages))
	}
	if mail.messages[0].to[0] != "user@test.com" {
		t.Errorf("unexpected recipient: %v", mail.messages[0].to)
	}
	invites, err := st.ListInvites()
	if err != nil {
		t.Fatalf("ListInvites: %v", err)
	}
	if len(invites) != 1 || invites[0].Email != "user@test.com" {
		t.Errorf("expected invite for existing user, got %+v", invites)
	}
}

func TestAdminInvites_Create_InvalidEmail(t *testing.T) {
	h, st, mail := inviteTestHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	form := url.Values{"emails": {"not-an-email"}}
	req := authedRequest(http.MethodPost, "/web/admin/invites/create", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}
	if !strings.Contains(w.Result().Header.Get("Location"), "error=") {
		t.Error("expected error redirect")
	}
	if len(mail.messages) != 0 {
		t.Error("expected no email for invalid address")
	}
}

func TestAdminInvites_Revoke(t *testing.T) {
	h, st, _ := inviteTestHandler(t)
	defer st.Close()

	seedAdmin(t, st)
	inv := &store.Invite{
		Email:          "revoke@test.com",
		Role:           "user",
		TokenHash:      strings.Repeat("a", 64),
		TokenExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := st.CreateInvite(inv); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	form := url.Values{"invite_id": {inv.ID}}
	req := authedRequest(http.MethodPost, "/web/admin/invites/revoke", form.Encode(), cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}
	got, err := st.GetInviteByID(inv.ID)
	if err != nil {
		t.Fatalf("GetInviteByID: %v", err)
	}
	if got.Status != store.InviteRevoked {
		t.Errorf("expected status revoked, got %s", got.Status)
	}
}

func TestInviteSignup_InvalidToken(t *testing.T) {
	h, st, _ := inviteTestHandler(t)
	defer st.Close()

	mux := http.NewServeMux()
	h.Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/web/invite?token=bad_token", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid, expired, or already used") {
		t.Error("expected error message")
	}
}

func TestInviteSignup_ValidToken_RendersForm(t *testing.T) {
	h, st, _ := inviteTestHandler(t)
	defer st.Close()

	raw, hash, err := store.GenerateInviteToken()
	if err != nil {
		t.Fatalf("GenerateInviteToken: %v", err)
	}
	inv := &store.Invite{
		Email:          "karl.dane@tuskerdirect.com",
		Name:           "Karl Dane",
		Role:           "admin",
		TokenHash:      hash,
		TokenExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := st.CreateInvite(inv); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	mux := http.NewServeMux()
	h.Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/web/invite?token="+raw, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "karl.dane@tuskerdirect.com") {
		t.Error("expected email in form")
	}
	if !strings.Contains(body, "Create Account") {
		t.Error("expected signup form")
	}
}

func TestInviteSignup_CreatesUserAndAutoLogsIn(t *testing.T) {
	h, st, _ := inviteTestHandler(t)
	defer st.Close()

	raw, hash, err := store.GenerateInviteToken()
	if err != nil {
		t.Fatalf("GenerateInviteToken: %v", err)
	}
	inv := &store.Invite{
		Email:          "newbie@test.com",
		Name:           "New Person",
		Role:           "user",
		TokenHash:      hash,
		TokenExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := st.CreateInvite(inv); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	mux := http.NewServeMux()
	h.Register(mux)

	form := url.Values{
		"email":            {"newbie@test.com"},
		"name":             {"New Person"},
		"password":         {"hunter2"},
		"password_confirm": {"hunter2"},
	}
	req := httptest.NewRequest(http.MethodPost, "/web/invite?token="+raw, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}
	loc := w.Result().Header.Get("Location")
	if !strings.HasSuffix(loc, "/web/") {
		t.Errorf("expected redirect to /web/, got %s", loc)
	}

	// Session cookie should be set (auto-login).
	var gotCookie bool
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			gotCookie = true
		}
	}
	if !gotCookie {
		t.Error("expected session cookie for auto-login")
	}

	user, err := st.GetUserByEmail("newbie@test.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if user.Role != "user" {
		t.Errorf("expected role user, got %s", user.Role)
	}

	got, err := st.GetInviteByID(inv.ID)
	if err != nil {
		t.Fatalf("GetInviteByID: %v", err)
	}
	if got.Status != store.InviteAccepted {
		t.Errorf("expected invite accepted, got %s", got.Status)
	}
}

func TestInviteSignup_EmailMismatch(t *testing.T) {
	h, st, _ := inviteTestHandler(t)
	defer st.Close()

	raw, hash, err := store.GenerateInviteToken()
	if err != nil {
		t.Fatalf("GenerateInviteToken: %v", err)
	}
	inv := &store.Invite{
		Email:          "invited@test.com",
		Role:           "user",
		TokenHash:      hash,
		TokenExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := st.CreateInvite(inv); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	mux := http.NewServeMux()
	h.Register(mux)

	form := url.Values{
		"email":            {"someone-else@test.com"},
		"password":         {"hunter2"},
		"password_confirm": {"hunter2"},
	}
	req := httptest.NewRequest(http.MethodPost, "/web/invite?token="+raw, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "does not match") {
		t.Error("expected email mismatch error")
	}
	if _, err := st.GetUserByEmail("someone-else@test.com"); err == nil {
		t.Error("user should not have been created")
	}
}

func TestInviteSignup_PasswordMismatch(t *testing.T) {
	h, st, _ := inviteTestHandler(t)
	defer st.Close()

	raw, hash, err := store.GenerateInviteToken()
	if err != nil {
		t.Fatalf("GenerateInviteToken: %v", err)
	}
	inv := &store.Invite{
		Email:          "pw@test.com",
		Role:           "user",
		TokenHash:      hash,
		TokenExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := st.CreateInvite(inv); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	mux := http.NewServeMux()
	h.Register(mux)

	form := url.Values{
		"email":            {"pw@test.com"},
		"password":         {"one"},
		"password_confirm": {"two"},
	}
	req := httptest.NewRequest(http.MethodPost, "/web/invite?token="+raw, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "do not match") {
		t.Error("expected password mismatch error")
	}
}

func TestInviteSignup_ExistingUser_BlocksByDefault(t *testing.T) {
	h, st, _ := inviteTestHandler(t)
	defer st.Close()
	h.InviteAllowExisting = false

	existing := seedRegularUser(t, st)

	raw, hash, err := store.GenerateInviteToken()
	if err != nil {
		t.Fatalf("GenerateInviteToken: %v", err)
	}
	inv := &store.Invite{
		Email:          existing.Email,
		Role:           "user",
		TokenHash:      hash,
		TokenExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := st.CreateInvite(inv); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	mux := http.NewServeMux()
	h.Register(mux)

	form := url.Values{
		"email":            {existing.Email},
		"password":         {"whatever"},
		"password_confirm": {"whatever"},
	}
	req := httptest.NewRequest(http.MethodPost, "/web/invite?token="+raw, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with error, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "already exists") {
		t.Error("expected already-exists error when flag off")
	}
	got, _ := st.GetInviteByID(inv.ID)
	if got.Status != store.InvitePending {
		t.Errorf("expected invite still pending, got %s", got.Status)
	}
}

func TestInviteSignup_ExistingUser_AutoLogins_WhenFlagSet(t *testing.T) {
	h, st, _ := inviteTestHandler(t)
	defer st.Close()
	h.InviteAllowExisting = true

	existing := seedRegularUser(t, st)

	raw, hash, err := store.GenerateInviteToken()
	if err != nil {
		t.Fatalf("GenerateInviteToken: %v", err)
	}
	inv := &store.Invite{
		Email:          existing.Email,
		Role:           "admin",
		TokenHash:      hash,
		TokenExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := st.CreateInvite(inv); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	mux := http.NewServeMux()
	h.Register(mux)

	form := url.Values{
		"email":            {existing.Email},
		"name":             {"User"},
		"password":         {"whatever"},
		"password_confirm": {"whatever"},
	}
	req := httptest.NewRequest(http.MethodPost, "/web/invite?token="+raw, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}
	if !strings.HasSuffix(w.Result().Header.Get("Location"), "/web/") {
		t.Errorf("expected redirect to /web/, got %s", w.Result().Header.Get("Location"))
	}

	var gotCookie bool
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			gotCookie = true
		}
	}
	if !gotCookie {
		t.Error("expected session cookie for auto-login")
	}

	got, err := st.GetInviteByID(inv.ID)
	if err != nil {
		t.Fatalf("GetInviteByID: %v", err)
	}
	if got.Status != store.InviteAccepted {
		t.Errorf("expected invite accepted, got %s", got.Status)
	}

	// No duplicate user created.
	users, err := st.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	matches := 0
	for _, u := range users {
		if u.Email == existing.Email {
			matches++
		}
	}
	if matches != 1 {
		t.Errorf("expected single user for %s, found %d", existing.Email, matches)
	}
}

func TestInviteRoutes_SSOMode_Returns404(t *testing.T) {
	h, st, _ := inviteTestHandler(t)
	defer st.Close()
	h.AuthMode = "sso"

	seedAdmin(t, st)
	mux := http.NewServeMux()
	h.Register(mux)
	cookie := loginCookie(t, h, mux, "admin@test.com", "secret")

	req := authedRequest(http.MethodGet, "/web/admin/invites", "", cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("admin invites: expected 404 in sso mode, got %d", w.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/web/invite?token=whatever", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	if w2.Code != http.StatusNotFound {
		t.Errorf("public invite: expected 404 in sso mode, got %d", w2.Code)
	}
}
