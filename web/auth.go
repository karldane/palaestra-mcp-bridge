package web

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/mcp-bridge/mcp-bridge/internal/crypto"
	"github.com/mcp-bridge/mcp-bridge/store"
)

type contextKey string

const userContextKey contextKey = "web_user"

// sessionDEKStore stores user-derived encryption keys in memory, keyed by session token.
// This is in-memory only and cleared on logout/session expiry.
var sessionDEKStore = make(map[string][]byte)

// SetSessionDEKForTest is a test helper to manually set a session DEK.
// This should only be used in tests.
func SetSessionDEKForTest(sessionToken string, dek []byte) {
	sessionDEKStore[sessionToken] = dek
}

// ClearSessionDEKForTest clears a session DEK.
// This should only be used in tests.
func ClearSessionDEKForTest(sessionToken string) {
	if dek, ok := sessionDEKStore[sessionToken]; ok {
		crypto.Zeroize(dek)
	}
	delete(sessionDEKStore, sessionToken)
}

func (h *Handler) getSessionUser(r *http.Request) *store.User {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil
	}
	sess, err := h.Store.GetWebSession(cookie.Value)
	if err != nil {
		return nil
	}
	if time.Now().UTC().After(sess.ExpiresAt) {
		h.Store.DeleteWebSession(sess.Token)
		return nil
	}
	user, err := h.Store.GetUser(sess.UserID)
	if err != nil {
		return nil
	}
	return user
}

func (h *Handler) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := h.getSessionUser(r)
		if user == nil {
			http.Redirect(w, r, "/web/login", http.StatusSeeOther)
			return
		}
		// Store user in request context
		ctx := r.Context()
		ctx = contextWithUser(ctx, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h *Handler) requireAdmin(next http.Handler) http.Handler {
	return h.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := userFromContext(r)
		if user == nil || user.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		// Context with user is already set by requireAuth
		next.ServeHTTP(w, r)
	}))
}

func (h *Handler) LoginHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.render(w, "login.html", pageData{Title: "Sign In"})
	case http.MethodPost:
		h.loginPost(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) loginPost(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	user, err := h.Store.GetUserByEmail(email)
	if err != nil || store.CheckPassword(user.Password, password) != nil {
		h.render(w, "login.html", pageData{
			Title: "Sign In",
			Error: "Invalid email or password",
		})
		return
	}

	// Auto-upgrade plaintext password to bcrypt on successful login.
	if !store.IsBcrypt(user.Password) {
		if hash, hashErr := store.HashPassword(password); hashErr == nil {
			user.Password = hash
			h.Store.UpdateUser(user)
		}
	}

	// Generate password salt if user doesn't have one (migration for existing users)
	if user.PasswordSalt == "" {
		salt, saltErr := crypto.GenerateSalt()
		if saltErr == nil {
			user.PasswordSalt = salt
			h.Store.UpdateUser(user)
		}
	}

	// Create web session
	sess := &store.WebSession{
		UserID:    user.ID,
		ExpiresAt: time.Now().UTC().Add(sessionTTL),
	}
	if err := h.Store.CreateWebSession(sess); err != nil {
		log.Printf("web: create session: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Derive user DEK from password and store in session (in-memory only)
	if user.PasswordSalt != "" {
		userDEK, dekErr := crypto.DeriveUserDEK(password, user.PasswordSalt)
		if dekErr == nil {
			sessionDEKStore[sess.Token] = userDEK
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sess.Token,
		Path:     "/web",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
		Secure:   !isLocalhost(r),
	})

	http.Redirect(w, r, "/web/", http.StatusSeeOther)
}

func (h *Handler) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		// Clear user DEK from memory
		if dek, ok := sessionDEKStore[cookie.Value]; ok {
			for i := range dek {
				dek[i] = 0
			}
			delete(sessionDEKStore, cookie.Value)
		}
		h.Store.DeleteWebSession(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:   sessionCookieName,
		Value:  "",
		Path:   "/web",
		MaxAge: -1,
	})
	http.Redirect(w, r, "/web/login", http.StatusSeeOther)
}

func getSessionDEK(r *http.Request) []byte {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil
	}
	if dek, ok := sessionDEKStore[cookie.Value]; ok {
		return dek
	}
	return nil
}
