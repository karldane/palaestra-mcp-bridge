package web

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/mcp-bridge/mcp-bridge/internal/crypto"
	"github.com/mcp-bridge/mcp-bridge/poolmgr"
	"github.com/mcp-bridge/mcp-bridge/store"
)

func (h *Handler) DashboardHandler(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)

	backends, _ := h.Store.ListBackends()
	tokens, _ := h.Store.GetAllUserTokens(user.ID)

	type backendStatus struct {
		Backend        *store.Backend
		ConfiguredKeys []string
		Pool           *poolmgr.PoolStatus
	}
	var statuses []backendStatus

	var userPools []poolmgr.PoolStatus
	if h.PoolManager != nil {
		userPools = h.PoolManager.GetPoolsForUser(user.ID)
	}

	for _, b := range backends {
		if !b.Enabled {
			continue
		}
		var keys []string
		for _, t := range tokens {
			if t.BackendID == b.ID {
				keys = append(keys, t.EnvKey)
			}
		}

		var pool *poolmgr.PoolStatus
		for i := range userPools {
			if userPools[i].BackendID == b.ID {
				poolCopy := userPools[i]
				pool = &poolCopy
				break
			}
		}

		statuses = append(statuses, backendStatus{Backend: b, ConfiguredKeys: keys, Pool: pool})
	}

	var totalPools, totalWarm, totalActive int
	var totalMemory uint64
	for _, p := range userPools {
		totalPools++
		totalWarm += p.WarmCount
		totalActive += p.CurrentSize - p.WarmCount
		totalMemory += p.MemoryBytes
	}

	h.render(w, "dashboard.html", pageData{
		User:  user,
		Title: "Dashboard",
		Data:  statuses,
		Extra: map[string]interface{}{
			"TotalPools":   totalPools,
			"TotalWarm":    totalWarm,
			"TotalActive":  totalActive,
			"TotalMemory":  formatBytes(totalMemory),
			"TokenCount":   len(tokens),
			"BackendCount": len(statuses),
		},
	})
}

func formatBytes(bytes uint64) string {
	if bytes == 0 {
		return "0 B"
	}
	mb := float64(bytes) / 1024 / 1024
	if mb > 1024 {
		return fmt.Sprintf("%.1f GB", mb/1024)
	}
	return fmt.Sprintf("%.0f MB", mb)
}

func (h *Handler) TokensHandler(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	backendID := r.URL.Query().Get("backend")

	backends, _ := h.Store.ListBackends()

	// If no backend selected, show the first enabled one
	if backendID == "" {
		for _, b := range backends {
			if b.Enabled {
				backendID = b.ID
				break
			}
		}
	}

	var backend *store.Backend
	for _, b := range backends {
		if b.ID == backendID {
			backend = b
			break
		}
	}

	var tokens []*store.UserToken
	if backendID != "" {
		tokens, _ = h.Store.GetUserTokens(user.ID, backendID)
	}

	// Try to decrypt tokens using user DEK from session
	userDEK := getSessionDEK(r)
	if userDEK != nil {
		var decryptedTokens []*store.UserToken
		for _, t := range tokens {
			decrypted := *t
			if t.Encrypted != "" && (t.EncryptionType == "user" || t.EncryptedDEK != "") {
				// Try user-derived decryption first
				if val, err := h.Store.GetUserTokenDecryptedWithUserDEK(user.ID, t.BackendID, t.EnvKey, userDEK); err == nil {
					decrypted.Value = val
				} else if ks := h.Store.KeyStore(); ks != nil {
					// Fall back to master key decryption
					if val, err := h.Store.GetUserTokenDecrypted(user.ID, t.BackendID, t.EnvKey); err == nil {
						decrypted.Value = val
					}
				}
			}
			decryptedTokens = append(decryptedTokens, &decrypted)
		}
		if len(decryptedTokens) > 0 {
			tokens = decryptedTokens
		}
		defer crypto.Zeroize(userDEK)
	}

	type tokensData struct {
		Backends        []*store.Backend
		SelectedID      string
		SelectedBackend *store.Backend
		Tokens          []*store.UserToken
	}

	h.render(w, "tokens.html", pageData{
		User:    user,
		Title:   "My Tokens",
		Error:   r.URL.Query().Get("error"),
		Success: r.URL.Query().Get("success"),
		Data: tokensData{
			Backends:        backends,
			SelectedID:      backendID,
			SelectedBackend: backend,
			Tokens:          tokens,
		},
	})
}

func (h *Handler) TokensSaveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := userFromContext(r)

	backendID := r.FormValue("backend_id")
	envKey := strings.TrimSpace(r.FormValue("env_key"))
	value := r.FormValue("value")

	if backendID == "" || envKey == "" || value == "" {
		http.Redirect(w, r, "/web/tokens?backend="+backendID+"&error=All+fields+required", http.StatusSeeOther)
		return
	}

	// Try to use user-derived encryption if session has DEK
	userDEK := getSessionDEK(r)
	if userDEK != nil {
		if err := h.Store.SetUserTokenWithUserDEK(user.ID, backendID, envKey, value, userDEK); err != nil {
			log.Printf("web: set token with user DEK: %v", err)
			http.Redirect(w, r, "/web/tokens?backend="+backendID+"&error=Failed+to+save+token", http.StatusSeeOther)
			return
		}
		defer crypto.Zeroize(userDEK)
		http.Redirect(w, r, "/web/tokens?backend="+backendID+"&success=Token+saved", http.StatusSeeOther)
		return
	}

	// Fall back to master key encryption
	if ks := h.Store.KeyStore(); ks != nil {
		if encrypted, encErr := ks.EncryptSecret([]byte(value)); encErr == nil {
			if err := h.Store.SetUserTokenEncrypted(user.ID, backendID, envKey, string(encrypted)); err != nil {
				log.Printf("web: set encrypted token: %v", err)
				http.Redirect(w, r, "/web/tokens?backend="+backendID+"&error=Failed+to+save+token", http.StatusSeeOther)
				return
			}
			http.Redirect(w, r, "/web/tokens?backend="+backendID+"&success=Token+saved", http.StatusSeeOther)
			return
		}
	}

	// No encryption available — save as plaintext
	token := &store.UserToken{
		UserID:    user.ID,
		BackendID: backendID,
		EnvKey:    envKey,
		Value:     value,
	}
	if err := h.Store.SetUserToken(token); err != nil {
		log.Printf("web: set token: %v", err)
		http.Redirect(w, r, "/web/tokens?backend="+backendID+"&error=Failed+to+save+token", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/web/tokens?backend="+backendID+"&success=Token+saved", http.StatusSeeOther)
}

func (h *Handler) TokensDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := userFromContext(r)

	backendID := r.FormValue("backend_id")
	envKey := r.FormValue("env_key")

	if err := h.Store.DeleteUserToken(user.ID, backendID, envKey); err != nil {
		log.Printf("web: delete token: %v", err)
	}

	http.Redirect(w, r, "/web/tokens?backend="+backendID+"&success=Token+deleted", http.StatusSeeOther)
}

func (h *Handler) PasswordHandler(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)

	switch r.Method {
	case http.MethodGet:
		h.render(w, "password.html", pageData{User: user, Title: "Change Password"})
	case http.MethodPost:
		current := r.FormValue("current_password")
		newPw := r.FormValue("new_password")
		confirm := r.FormValue("confirm_password")

		if store.CheckPassword(user.Password, current) != nil {
			h.render(w, "password.html", pageData{User: user, Title: "Change Password", Error: "Current password is incorrect"})
			return
		}
		if newPw == "" {
			h.render(w, "password.html", pageData{User: user, Title: "Change Password", Error: "New password cannot be empty"})
			return
		}
		if newPw != confirm {
			h.render(w, "password.html", pageData{User: user, Title: "Change Password", Error: "New passwords do not match"})
			return
		}

		// Get user DEK from session to decrypt existing tokens
		userDEK := getSessionDEK(r)
		if userDEK == nil {
			h.render(w, "password.html", pageData{User: user, Title: "Change Password", Error: "Session expired, please log in again"})
			return
		}

		// Generate new salt
		newSalt, saltErr := crypto.GenerateSalt()
		if saltErr != nil {
			log.Printf("web: generate salt: %v", saltErr)
			h.render(w, "password.html", pageData{User: user, Title: "Change Password", Error: "Failed to generate new encryption key"})
			return
		}

		// Derive new user DEK from new password
		newUserDEK, dekErr := crypto.DeriveUserDEK(newPw, newSalt)
		if dekErr != nil {
			log.Printf("web: derive new DEK: %v", dekErr)
			h.render(w, "password.html", pageData{User: user, Title: "Change Password", Error: "Failed to derive new encryption key"})
			return
		}
		defer crypto.Zeroize(newUserDEK)

		// Re-encrypt all user tokens with new key
		allTokens, err := h.Store.GetAllUserTokens(user.ID)
		if err != nil {
			log.Printf("web: get all tokens: %v", err)
		} else {
			for _, t := range allTokens {
				var plaintext string

				// Try to decrypt with old user DEK
				if t.EncryptedDEK != "" && t.EncryptionType == "user" {
					if val, err := h.Store.GetUserTokenDecryptedWithUserDEK(user.ID, t.BackendID, t.EnvKey, userDEK); err == nil {
						plaintext = val
					}
				}

				// Fall back to master key
				if plaintext == "" && t.Encrypted != "" {
					if val, err := h.Store.GetUserTokenDecrypted(user.ID, t.BackendID, t.EnvKey); err == nil {
						plaintext = val
					}
				}

				// Fall back to plaintext value
				if plaintext == "" {
					plaintext = t.Value
				}

				// Re-encrypt with new user DEK
				if plaintext != "" {
					if err := h.Store.SetUserTokenWithUserDEK(user.ID, t.BackendID, t.EnvKey, plaintext, newUserDEK); err != nil {
						log.Printf("web: re-encrypt token %s/%s: %v", t.BackendID, t.EnvKey, err)
					}
				}
			}
		}

		// Clear old DEK from session
		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			if oldDEK, ok := sessionDEKStore[cookie.Value]; ok {
				crypto.Zeroize(oldDEK)
			}
			sessionDEKStore[cookie.Value] = newUserDEK
		}

		// Update user's password and salt
		user.Password = newPw
		user.PasswordSalt = newSalt
		if err := h.Store.UpdateUser(user); err != nil {
			log.Printf("web: update password: %v", err)
			h.render(w, "password.html", pageData{User: user, Title: "Change Password", Error: "Failed to update password"})
			return
		}

		defer crypto.Zeroize(userDEK)
		h.render(w, "password.html", pageData{User: user, Title: "Change Password", Success: "Password updated and tokens re-encrypted"})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) APIKeysHandler(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)

	keys, err := h.Store.ListAPIKeys(user.ID)
	if err != nil {
		log.Printf("web: list api keys: %v", err)
		keys = nil
	}

	h.render(w, "apikeys.html", pageData{
		User:    user,
		Title:   "API Keys",
		Error:   r.URL.Query().Get("error"),
		Success: r.URL.Query().Get("success"),
		Data:    keys,
	})
}

func (h *Handler) APIKeysCreateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := userFromContext(r)

	name := r.FormValue("name")
	if name == "" {
		name = "API Key"
	}

	key, hash, err := store.GenerateAPIKey()
	if err != nil {
		log.Printf("web: generate api key: %v", err)
		http.Redirect(w, r, "/web/apikeys?error=Failed+to+generate+key", http.StatusSeeOther)
		return
	}

	apiKey := &store.APIKey{
		UserID:  user.ID,
		Name:    name,
		KeyHash: hash,
	}

	if err := h.Store.CreateAPIKey(apiKey); err != nil {
		log.Printf("web: create api key: %v", err)
		http.Redirect(w, r, "/web/apikeys?error=Failed+to+save+key", http.StatusSeeOther)
		return
	}

	h.render(w, "apikeys.html", pageData{
		User:  user,
		Title: "API Keys",
		Data:  nil,
		Extra: map[string]interface{}{"GeneratedKey": key},
	})
}

func (h *Handler) APIKeysDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := userFromContext(r)

	id := r.FormValue("id")
	if id == "" {
		http.Redirect(w, r, "/web/apikeys?error=Missing+key+ID", http.StatusSeeOther)
		return
	}

	key, err := h.Store.GetAPIKeyByID(id)
	if err != nil || key.UserID != user.ID {
		http.Redirect(w, r, "/web/apikeys?error=Key+not+found", http.StatusSeeOther)
		return
	}

	if err := h.Store.DeleteAPIKey(id); err != nil {
		log.Printf("web: delete api key: %v", err)
		http.Redirect(w, r, "/web/apikeys?error=Failed+to+delete+key", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/web/apikeys?success=Key+deleted", http.StatusSeeOther)
}
