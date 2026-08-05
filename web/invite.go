package web

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mcp-bridge/mcp-bridge/internal/crypto"
	"github.com/mcp-bridge/mcp-bridge/internal/mailer"
	"github.com/mcp-bridge/mcp-bridge/store"
)

var errInvalidInvite = errors.New("invalid invitation")

// invitationsEnabled reports whether the invitation flow is available in the
// current auth mode. It only applies to internal (username/password) auth.
func (h *Handler) invitationsEnabled() bool {
	return !strings.EqualFold(h.AuthMode, "sso")
}

func (h *Handler) AdminInvitesHandler(w http.ResponseWriter, r *http.Request) {
	if !h.invitationsEnabled() {
		http.NotFound(w, r)
		return
	}
	user := userFromContext(r)
	invites, err := h.Store.ListInvites()
	if err != nil {
		log.Printf("web: list invites: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	h.render(w, "admin_invites.html", pageData{
		User:    user,
		Title:   "Invitations",
		Data:    invites,
		Error:   r.URL.Query().Get("error"),
		Success: r.URL.Query().Get("success"),
		Extra: map[string]interface{}{
			"InviteExpiryDays": int(h.InviteExpiry.Hours() / 24),
		},
	})
}

func (h *Handler) AdminInvitesCreateHandler(w http.ResponseWriter, r *http.Request) {
	if !h.invitationsEnabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.Mailer == nil {
		http.Redirect(w, r, "/web/admin/invites?error=Email+sending+is+not+configured", http.StatusSeeOther)
		return
	}

	emails := r.FormValue("emails")
	name := strings.TrimSpace(r.FormValue("name"))
	role := strings.TrimSpace(r.FormValue("role"))
	if role == "" {
		role = "user"
	}

	recipients, err := mailer.ParseRecipients(emails)
	if err != nil {
		http.Redirect(w, r, "/web/admin/invites?error="+urlQueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	inviter := userFromContext(r)
	var sent, skipped []string
	for _, email := range recipients {
		email = strings.ToLower(email)

		// Skip existing users (unless explicitly allowed) and existing pending
		// invitations.
		if !h.InviteAllowExisting {
			if _, err := h.Store.GetUserByEmail(email); err == nil {
				skipped = append(skipped, email)
				continue
			}
		}
		if !h.emailNotPending(email) {
			skipped = append(skipped, email)
			continue
		}

		rawToken, tokenHash, err := store.GenerateInviteToken()
		if err != nil {
			log.Printf("web: generate invite token: %v", err)
			continue
		}

		inv := &store.Invite{
			Email:          email,
			Name:           name,
			Role:           role,
			TokenHash:      tokenHash,
			TokenExpiresAt: time.Now().UTC().Add(h.inviteExpiry()),
			InvitedBy:      inviter.ID,
		}
		if err := h.Store.CreateInvite(inv); err != nil {
			log.Printf("web: create invite for %s: %v", email, err)
			continue
		}

		inviteURL := strings.TrimRight(h.PublicURL, "/") + "/web/invite?token=" + rawToken
		msg, err := mailer.BuildInviteEmail(h.mailerFrom(), email, name, inviteURL, h.inviteExpiry())
		if err != nil {
			log.Printf("web: build invite email for %s: %v", email, err)
			continue
		}
		if err := h.Mailer.Send([]string{email}, "Invitation to mcp-bridge", msg); err != nil {
			log.Printf("web: send invite email to %s: %v", email, err)
			continue
		}
		sent = append(sent, email)
	}

	if len(sent) == 0 {
		http.Redirect(w, r, "/web/admin/invites?error=No+invitations+were+sent", http.StatusSeeOther)
		return
	}
	success := "Invitations sent: " + strings.Join(sent, ", ")
	if len(skipped) > 0 {
		success += " (skipped: " + strings.Join(skipped, ", ") + ")"
	}
	http.Redirect(w, r, "/web/admin/invites?success="+urlQueryEscape(success), http.StatusSeeOther)
}

func (h *Handler) emailNotPending(email string) bool {
	invites, err := h.Store.ListInvites()
	if err != nil {
		return true
	}
	for _, inv := range invites {
		if inv.Email == email && inv.Status == store.InvitePending {
			return false
		}
	}
	return true
}

func (h *Handler) inviteExpiry() time.Duration {
	if h.InviteExpiry <= 0 {
		return 7 * 24 * time.Hour
	}
	return h.InviteExpiry
}

func (h *Handler) mailerFrom() string {
	if h.MailerFrom != "" {
		return h.MailerFrom
	}
	return "noreply@localhost"
}

func (h *Handler) AdminInvitesRevokeHandler(w http.ResponseWriter, r *http.Request) {
	if !h.invitationsEnabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	inviteID := r.FormValue("invite_id")
	if inviteID == "" {
		http.Redirect(w, r, "/web/admin/invites?error=Missing+invite", http.StatusSeeOther)
		return
	}
	if err := h.Store.RevokeInvite(inviteID); err != nil {
		log.Printf("web: revoke invite %s: %v", inviteID, err)
		http.Redirect(w, r, "/web/admin/invites?error=Failed+to+revoke+invite", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/web/admin/invites?success=Invitation+revoked", http.StatusSeeOther)
}

func (h *Handler) InviteSignupHandler(w http.ResponseWriter, r *http.Request) {
	if !h.invitationsEnabled() {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.inviteSignupGet(w, r)
	case http.MethodPost:
		h.inviteSignupPost(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) inviteSignupGet(w http.ResponseWriter, r *http.Request) {
	inv, err := h.lookupValidInvite(r)
	if err != nil {
		h.render(w, "invite.html", pageData{
			Title: "Invitation",
			Error: "This invitation link is invalid, expired, or already used.",
		})
		return
	}
	h.render(w, "invite.html", pageData{
		Title: "Set up your account",
		Extra: map[string]interface{}{
			"Token": r.URL.Query().Get("token"),
			"Email": inv.Email,
			"Name":  inv.Name,
		},
	})
}

func (h *Handler) inviteSignupPost(w http.ResponseWriter, r *http.Request) {
	inv, err := h.lookupValidInvite(r)
	if err != nil {
		h.render(w, "invite.html", pageData{
			Title: "Invitation",
			Error: "This invitation link is invalid, expired, or already used.",
		})
		return
	}

	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	name := strings.TrimSpace(r.FormValue("name"))
	password := r.FormValue("password")
	confirm := r.FormValue("password_confirm")
	token := r.URL.Query().Get("token")

	if email == "" || password == "" {
		h.render(w, "invite.html", pageData{
			Title: "Set up your account",
			Error: "Email and password are required.",
			Extra: map[string]interface{}{"Token": token, "Email": email, "Name": name},
		})
		return
	}
	if password != confirm {
		h.render(w, "invite.html", pageData{
			Title: "Set up your account",
			Error: "Passwords do not match.",
			Extra: map[string]interface{}{"Token": token, "Email": email, "Name": name},
		})
		return
	}
	if email != inv.Email {
		h.render(w, "invite.html", pageData{
			Title: "Set up your account",
			Error: "The email does not match the invited address.",
			Extra: map[string]interface{}{"Token": token, "Email": email, "Name": name},
		})
		return
	}

	if existing, err := h.Store.GetUserByEmail(email); err == nil {
		if !h.InviteAllowExisting {
			h.render(w, "invite.html", pageData{
				Title: "Set up your account",
				Error: "An account with this email already exists.",
				Extra: map[string]interface{}{"Token": token, "Email": email, "Name": name},
			})
			return
		}
		// An existing account is allowed. Treat the invite as a login grant:
		// mark it accepted against the existing account and log that account in.
		if err := h.Store.MarkInviteAccepted(inv.ID, existing.ID); err != nil {
			log.Printf("web: mark invite accepted for existing user: %v", err)
		}
		h.autoLogin(w, r, existing, "")
		http.Redirect(w, r, "/web/", http.StatusSeeOther)
		return
	}

	u := &store.User{
		Name:     name,
		Email:    email,
		Password: password,
		Role:     inv.Role,
	}
	if err := h.Store.CreateUser(u); err != nil {
		log.Printf("web: create invited user: %v", err)
		h.render(w, "invite.html", pageData{
			Title: "Set up your account",
			Error: "Failed to create account. Please try again.",
			Extra: map[string]interface{}{"Token": token, "Email": email, "Name": name},
		})
		return
	}

	if err := h.Store.MarkInviteAccepted(inv.ID, u.ID); err != nil {
		log.Printf("web: mark invite accepted: %v", err)
	}

	h.autoLogin(w, r, u, password)
	http.Redirect(w, r, "/web/", http.StatusSeeOther)
}

// autoLogin creates a web session and sets the session cookie, mirroring the
// login flow (derive user DEK from the password and cache it in memory).
func (h *Handler) autoLogin(w http.ResponseWriter, r *http.Request, u *store.User, password string) {
	sess := &store.WebSession{
		UserID:    u.ID,
		ExpiresAt: time.Now().UTC().Add(sessionTTL),
	}
	if err := h.Store.CreateWebSession(sess); err != nil {
		log.Printf("web: create session for invited user: %v", err)
		return
	}
	if u.PasswordSalt != "" {
		userDEK, dekErr := crypto.DeriveUserDEK(password, u.PasswordSalt)
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
}

// lookupValidInvite resolves the invitation for the current request, verifying
// the token, status, and expiry.
func (h *Handler) lookupValidInvite(r *http.Request) (*store.Invite, error) {
	token := r.URL.Query().Get("token")
	if token == "" {
		return nil, errInvalidInvite
	}
	hash, err := store.HashInviteToken(token)
	if err != nil {
		return nil, errInvalidInvite
	}
	inv, err := h.Store.GetInviteByTokenHash(hash)
	if err != nil {
		return nil, errInvalidInvite
	}
	if inv.Status != store.InvitePending {
		return nil, errInvalidInvite
	}
	if inv.Expired(time.Now().UTC()) {
		return nil, errInvalidInvite
	}
	return inv, nil
}

func urlQueryEscape(s string) string {
	return url.QueryEscape(s)
}
