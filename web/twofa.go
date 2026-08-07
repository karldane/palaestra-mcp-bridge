package web

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/mcp-bridge/mcp-bridge/internal/crypto"
	"github.com/mcp-bridge/mcp-bridge/shared"
	"github.com/mcp-bridge/mcp-bridge/store"
)

const (
	// pendingCookieName carries a short-lived login-in-progress marker issued
	// after password verification but before 2FA completes.
	pendingCookieName = "mcp_pending"
	// pendingLoginTTL bounds how long a verified email+password login may sit
	// awaiting the 2FA step before it must restart.
	pendingLoginTTL = 5 * 60 // seconds
	// setupCookieName holds the transient enrollment secret between the GET
	// (show secret/QR) and POST (confirm code) steps.
	setupCookieName = "mcp_setup"
)

var errNotAuthFor2FA = errors.New("not authenticated for 2FA")

// pendingLogin holds a login whose password/email have been verified but whose
// 2FA step has not yet completed.
type pendingLogin struct {
	UserID    string
	Method    string
	DEK       []byte
	ExpiresAt int64 // unix seconds
}

// pendingSetup holds a freshly generated 2FA enrollment secret for the brief
// interval between showing it and confirming a code. DEK is needed so the
// confirmed secret can be wrapped with the user key.
type pendingSetup struct {
	UserID     string
	Method     string
	Secret     string
	OtpauthURL string
	DEK        []byte
	Forced     bool // true when completing a login-in-progress (enrollment at login)
	ExpiresAt  int64
}

// pendingLogins and pendingSetups are in-memory only, mirroring the existing
// sessionDEKStore pattern. They are cleared on use, expiry, and logout.
var (
	pendingLogins = make(map[string]*pendingLogin)
	pendingSetups = make(map[string]*pendingSetup)
)

func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func setCookie(w http.ResponseWriter, name, value string, maxAge int, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/web",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
		Secure:   !isLocalhost(r),
	})
}

func clearCookie(w http.ResponseWriter, name string, r *http.Request) {
	setCookie(w, name, "", -1, r)
}

func cookieValue(r *http.Request, name string) string {
	if c, err := r.Cookie(name); err == nil {
		return c.Value
	}
	return ""
}

func (h *Handler) getPending(r *http.Request) *pendingLogin {
	t := cookieValue(r, pendingCookieName)
	if t == "" {
		return nil
	}
	p, ok := pendingLogins[t]
	if !ok {
		return nil
	}
	if time.Now().Unix() > p.ExpiresAt {
		delete(pendingLogins, t)
		return nil
	}
	return p
}

// startPendingLogin records a pending login (verified password) and issues the
// pending cookie. DEK is preserved across the 2FA step so no re-derivation is
// needed after verification.
func (h *Handler) startPendingLogin(w http.ResponseWriter, r *http.Request, user *store.User, userDEK []byte, method string) {
	token, err := randomToken()
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	pendingLogins[token] = &pendingLogin{
		UserID:    user.ID,
		Method:    method,
		DEK:       userDEK,
		ExpiresAt: time.Now().Unix() + pendingLoginTTL,
	}
	setCookie(w, pendingCookieName, token, pendingLoginTTL, r)
}

// completeLogin creates the web session, caches the DEK, and sets the session
// cookie. Used after password-only login (no 2FA), and after 2FA verification
// or enrollment completion.
func (h *Handler) completeLogin(w http.ResponseWriter, r *http.Request, user *store.User, userDEK []byte) {
	sess := &store.WebSession{
		UserID:    user.ID,
		ExpiresAt: time.Now().UTC().Add(sessionTTL),
	}
	if err := h.Store.CreateWebSession(sess); err != nil {
		log.Printf("web: create session: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if userDEK != nil {
		sessionDEKStore[sess.Token] = userDEK
	}
	setCookie(w, sessionCookieName, sess.Token, int(sessionTTL.Seconds()), r)
}

// twoFACreds resolves the user ID and DEK for a 2FA operation, from either a
// pending login or an established authenticated session. The public setup route
// is reached both during a forced login and from the settings page, so both
// sources are consulted.
func (h *Handler) twoFACreds(r *http.Request) (string, []byte, error) {
	if p := h.getPending(r); p != nil {
		return p.UserID, p.DEK, nil
	}
	user := userFromContext(r)
	if user == nil {
		user = h.getSessionUser(r)
	}
	if user == nil {
		return "", nil, errNotAuthFor2FA
	}
	return user.ID, getSessionDEK(r), nil
}

// ---------- Challenge: /web/login/2fa ----------

func (h *Handler) Login2FAHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.login2FAGet(w, r)
	case http.MethodPost:
		h.login2FAPost(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) login2FAGet(w http.ResponseWriter, r *http.Request) {
	p := h.getPending(r)
	if p == nil {
		http.Redirect(w, r, "/web/login", http.StatusSeeOther)
		return
	}
	if p.Method == "" {
		http.Redirect(w, r, "/web/setup-2fa", http.StatusSeeOther)
		return
	}
	h.render(w, "login_2fa.html", pageData{
		Title: "Verify Sign-In",
		Extra: map[string]interface{}{"Bypass": h.TwoFABypass},
	})
}

func (h *Handler) login2FAPost(w http.ResponseWriter, r *http.Request) {
	p := h.getPending(r)
	if p == nil {
		http.Redirect(w, r, "/web/login", http.StatusSeeOther)
		return
	}
	code := strings.TrimSpace(r.FormValue("code"))

	// Testing-only bypass: allow a valid pending login through without a code.
	// Never enabled in production.
	if h.TwoFABypass {
		if user, err := h.Store.GetUser(p.UserID); err == nil {
			t := cookieValue(r, pendingCookieName)
			clearCookie(w, pendingCookieName, r)
			if t != "" {
				delete(pendingLogins, t)
			}
			h.completeLogin(w, r, user, p.DEK)
			return
		}
	}

	if p.Method == "" {
		http.Redirect(w, r, "/web/setup-2fa", http.StatusSeeOther)
		return
	}
	if err := h.TwoFA.VerifyLogin(p.UserID, p.Method, code, p.DEK); err != nil {
		h.render(w, "login_2fa.html", pageData{
			Title: "Verify Sign-In",
			Error: "Invalid code. Please try again.",
			Extra: map[string]interface{}{"Bypass": h.TwoFABypass},
		})
		return
	}
	user, err := h.Store.GetUser(p.UserID)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	t := cookieValue(r, pendingCookieName)
	clearCookie(w, pendingCookieName, r)
	if t != "" {
		delete(pendingLogins, t)
	}
	h.completeLogin(w, r, user, p.DEK)
	http.Redirect(w, r, "/web/", http.StatusSeeOther)
}

// ---------- Enrollment: /web/setup-2fa ----------

func (h *Handler) Setup2FAHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.setup2FAGet(w, r)
	case http.MethodPost:
		h.setup2FAPost(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) setup2FAGet(w http.ResponseWriter, r *http.Request) {
	methodID := r.URL.Query().Get("method")
	if methodID == "" {
		if len(h.TwoFAMethods) > 0 {
			methodID = h.TwoFAMethods[0]
		}
	}
	if methodID == "" {
		http.Error(w, "No 2FA methods configured", http.StatusBadRequest)
		return
	}

	userID, dek, err := h.twoFACreds(r)
	if err != nil {
		http.Redirect(w, r, "/web/login", http.StatusSeeOther)
		return
	}
	user, err := h.Store.GetUser(userID)
	if err != nil {
		http.Redirect(w, r, "/web/login", http.StatusSeeOther)
		return
	}

	res, err := h.TwoFA.Setup(user.Email, methodID)
	if err != nil {
		log.Printf("web: 2fa setup: %v", err)
		http.Error(w, "Failed to start setup", http.StatusInternalServerError)
		return
	}

	token, err := randomToken()
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	forced := h.getPending(r) != nil
	pendingSetups[token] = &pendingSetup{
		UserID:     user.ID,
		Method:     methodID,
		Secret:     res.Secret,
		OtpauthURL: res.OtpauthURL,
		DEK:        dek,
		Forced:     forced,
		ExpiresAt:  time.Now().Unix() + pendingLoginTTL,
	}
	setCookie(w, setupCookieName, token, pendingLoginTTL, r)

	qrPNG, qrErr := qrcode.Encode(res.OtpauthURL, qrcode.Medium, 220)
	var qrDataURI string
	if qrErr == nil {
		qrDataURI = "data:image/png;base64," + base64.StdEncoding.EncodeToString(qrPNG)
	}

	h.render(w, "setup_2fa.html", pageData{
		Title: "Set up two-factor authentication",
		Data: setupData{
			Method:     methodID,
			Secret:     res.Secret,
			OtpauthURL: res.OtpauthURL,
			QRDataURI:  template.URL(qrDataURI),
			Forced:     forced,
			Methods:    duplicateStrings(h.TwoFAMethods),
		},
	})
}

func (h *Handler) setup2FAPost(w http.ResponseWriter, r *http.Request) {
	if shared.IsDebugEnabled() {
		// Freeform debug envelope: enough to diagnose a failed enrollment
		// without ever logging the secret or the submitted code.
		shared.Debugf("web setup-2fa POST: setupCookie=%t pending=%t userAgent=%q",
			rToken(r) != "", h.getPending(r) != nil, r.UserAgent())
	}
	token := rToken(r)
	if token == "" {
		shared.Debugf("web setup-2fa POST: no setup cookie -> redirect login")
		http.Redirect(w, r, "/web/login", http.StatusSeeOther)
		return
	}
	ps, ok := pendingSetups[token]
	if !ok {
		shared.Debugf("web setup-2fa POST: no pendingSetup for token -> redirect login")
		http.Redirect(w, r, "/web/login", http.StatusSeeOther)
		return
	}
	if time.Now().Unix() > ps.ExpiresAt {
		delete(pendingSetups, token)
		shared.Debugf("web setup-2fa POST: pending setup expired (now=%d exp=%d) -> redirect login", time.Now().Unix(), ps.ExpiresAt)
		http.Redirect(w, r, "/web/login", http.StatusSeeOther)
		return
	}

	code := strings.TrimSpace(r.FormValue("code"))
	shared.Debugf("web setup-2fa POST: user=%s method=%s forced=%t codeLen=%d secretLen=%d",
		ps.UserID, ps.Method, ps.Forced, len(code), len(ps.Secret))

	secretBytes := []byte(ps.Secret)
	defer crypto.Zeroize(secretBytes)

	if err := h.TwoFA.Enable(ps.UserID, ps.Method, secretBytes, code, ps.DEK); err != nil {
		shared.Debugf("web setup-2fa POST: Enable failed: %v", err)
		h.render(w, "setup_2fa.html", pageData{
			Title: "Set up two-factor authentication",
			Error: "Invalid code. The code you entered does not match your authenticator. Please try again.",
			Data: setupData{
				Method:  ps.Method,
				Forced:  ps.Forced,
				Methods: duplicateStrings(h.TwoFAMethods),
				Secret:  ps.Secret,
			},
		})
		return
	}
	shared.Debugf("web setup-2fa POST: 2FA enabled for user=%s", ps.UserID)

	user, err := h.Store.GetUser(ps.UserID)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	clearCookie(w, setupCookieName, r)
	delete(pendingSetups, token)

	if ps.Forced {
		// Enrolled as part of a login: clear the pending marker and log in.
		pt := cookieValue(r, pendingCookieName)
		clearCookie(w, pendingCookieName, r)
		if pt != "" {
			delete(pendingLogins, pt)
		}
		h.completeLogin(w, r, user, ps.DEK)
		http.Redirect(w, r, "/web/", http.StatusSeeOther)
		return
	}

	// Reconfigured from an authenticated session: redirect to settings.
	http.Redirect(w, r, "/web/2fa?success=2FA+enabled", http.StatusSeeOther)
}

// ---------- Settings: /web/2fa ----------

func (h *Handler) TwoFASettingsHandler(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)

	switch r.Method {
	case http.MethodPost:
		h.twoFADisablePost(w, r, user)
		return
	case http.MethodGet:
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	has, method, _ := h.Store.GetUser2FA(user.ID)
	h.render(w, "2fa.html", pageData{
		User:    user,
		Title:   "Two-Factor Authentication",
		Error:   r.URL.Query().Get("error"),
		Success: r.URL.Query().Get("success"),
		Data: map[string]interface{}{
			"Has2FA":   has,
			"Method":   method,
			"Methods":  h.TwoFAMethods,
			"Required": h.TwoFA != nil && h.TwoFA.Required(),
		},
	})
}

func (h *Handler) twoFADisablePost(w http.ResponseWriter, r *http.Request, user *store.User) {
	if h.TwoFA == nil {
		http.Redirect(w, r, "/web/2fa?error=2FA+not+available", http.StatusSeeOther)
		return
	}
	if r.FormValue("action") != "disable" {
		http.Redirect(w, r, "/web/2fa", http.StatusSeeOther)
		return
	}
	if err := h.TwoFA.Delete(user.ID); err != nil {
		log.Printf("web: disable 2FA for %s: %v", user.ID, err)
		http.Redirect(w, r, "/web/2fa?error=Failed+to+disable+2FA", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/web/2fa?success=2FA+disabled", http.StatusSeeOther)
}

// ---------- Admin reset: /web/admin/users/2fa-reset ----------

func (h *Handler) AdminUsers2FAResetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID := r.FormValue("user_id")
	if userID == "" {
		http.Redirect(w, r, "/web/admin/users?error=Missing+user", http.StatusSeeOther)
		return
	}
	if err := h.TwoFA.Delete(userID); err != nil {
		log.Printf("web: reset 2FA for %s: %v", userID, err)
		http.Redirect(w, r, "/web/admin/users?error=Failed+to+reset+2FA", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/web/admin/users?success=2FA+reset", http.StatusSeeOther)
}

// ---------- helpers ----------

func rToken(r *http.Request) string {
	return cookieValue(r, setupCookieName)
}

func duplicateStrings(s []string) []string {
	out := make([]string, len(s))
	copy(out, s)
	return out
}

// setupData is the template payload for the enrollment page.
type setupData struct {
	Method     string
	Secret     string
	OtpauthURL string
	// QRDataURI is a data:image/png;base64 URI generated internally and not
	// derived from user input, so it may be exempted from html/template URL
	// sanitization (which otherwise rewrites data: URLs to #ZgotmplZ).
	QRDataURI template.URL
	Forced    bool
	Methods   []string
}
