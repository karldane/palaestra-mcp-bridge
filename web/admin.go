package web

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/mcp-bridge/mcp-bridge/store"
)

func (h *Handler) AdminUsersHandler(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	users, err := h.Store.ListUsers()
	if err != nil {
		log.Printf("web: list users: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	h.render(w, "admin_users.html", pageData{
		User:    user,
		Title:   "Manage Users",
		Data:    users,
		Error:   r.URL.Query().Get("error"),
		Success: r.URL.Query().Get("success"),
	})
}

func (h *Handler) AdminUsersCreateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")
	role := r.FormValue("role")

	if email == "" || password == "" {
		http.Redirect(w, r, "/web/admin/users?error=Email+and+password+required", http.StatusSeeOther)
		return
	}
	if role != "admin" && role != "user" {
		role = "user"
	}

	u := &store.User{
		Name:     name,
		Email:    email,
		Password: password,
		Role:     role,
	}
	if err := h.Store.CreateUser(u); err != nil {
		log.Printf("web: create user: %v", err)
		http.Redirect(w, r, "/web/admin/users?error=Failed+to+create+user+(email+may+already+exist)", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/web/admin/users?success=User+created", http.StatusSeeOther)
}

func (h *Handler) AdminUsersDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	currentUser := userFromContext(r)
	userID := r.FormValue("user_id")

	if userID == currentUser.ID {
		http.Redirect(w, r, "/web/admin/users?error=Cannot+delete+yourself", http.StatusSeeOther)
		return
	}

	if err := h.Store.DeleteUser(userID); err != nil {
		log.Printf("web: delete user: %v", err)
		http.Redirect(w, r, "/web/admin/users?error=Failed+to+delete+user", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/web/admin/users?success=User+deleted", http.StatusSeeOther)
}

// PoolStatusDisplay is a display-friendly version of poolmgr.PoolStatus
type PoolStatusDisplay struct {
	BackendID     string
	UserID        string
	Command       string
	WarmCount     int
	CurrentSize   int
	MinPoolSize   int
	MaxPoolSize   int
	TotalMemory   string
	ProcessMemory []string
}

func formatMemory(bytes uint64) string {
	if bytes == 0 {
		return "--"
	}
	mb := float64(bytes) / 1024 / 1024
	if mb > 1024 {
		return fmt.Sprintf("%.2f GB", mb/1024)
	}
	return fmt.Sprintf("%.0f MB", mb)
}

func (h *Handler) AdminBackendsHandler(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)
	backends, err := h.Store.ListBackends()
	if err != nil {
		log.Printf("web: list backends: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Get pool status if pool manager is available
	var poolStatuses []PoolStatusDisplay
	if h.PoolManager != nil {
		pools := h.PoolManager.GetAllPools()
		for _, p := range pools {
			display := PoolStatusDisplay{
				BackendID:   p.BackendID,
				UserID:      p.UserID,
				Command:     p.Command,
				WarmCount:   p.WarmCount,
				CurrentSize: p.CurrentSize,
				MinPoolSize: p.MinPoolSize,
				MaxPoolSize: p.MaxPoolSize,
				TotalMemory: formatMemory(p.MemoryBytes),
			}
			for _, mem := range p.ProcessMemory {
				display.ProcessMemory = append(display.ProcessMemory, formatMemory(mem))
			}
			poolStatuses = append(poolStatuses, display)
		}
	}

	// Get global hints
	globalHints, _ := h.Store.GetSetting("global_hints")

	h.render(w, "admin_backends.html", pageData{
		User:    user,
		Title:   "Manage Backends",
		Data:    backends,
		Error:   r.URL.Query().Get("error"),
		Success: r.URL.Query().Get("success"),
		Extra: map[string]interface{}{
			"PoolStatuses": poolStatuses,
			"GlobalHints":  globalHints,
		},
	})
}

func (h *Handler) AdminBackendsCreateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := strings.TrimSpace(r.FormValue("id"))
	command := strings.TrimSpace(r.FormValue("command"))
	minPoolSizeStr := r.FormValue("min_pool_size")
	maxPoolSizeStr := r.FormValue("max_pool_size")
	toolPrefix := strings.TrimSpace(r.FormValue("tool_prefix"))
	env := strings.TrimSpace(r.FormValue("env"))
	envMappings := strings.TrimSpace(r.FormValue("env_mappings"))
	toolHints := strings.TrimSpace(r.FormValue("tool_hints"))
	enabled := r.FormValue("enabled") == "on"

	// Validate backend ID: alphanumeric, dashes, underscores, max 50 chars
	if id == "" || command == "" {
		http.Redirect(w, r, "/web/admin/backends?error=ID+and+command+required", http.StatusSeeOther)
		return
	}
	if !isValidBackendID(id) {
		http.Redirect(w, r, "/web/admin/backends?error=Invalid+backend+ID:+use+only+letters,+numbers,+dashes,+and+underscores", http.StatusSeeOther)
		return
	}

	minPoolSize := 1
	if n := parseInt(minPoolSizeStr); n > 0 {
		minPoolSize = n
	}
	maxPoolSize := minPoolSize
	if ms := parseInt(maxPoolSizeStr); ms > 0 {
		maxPoolSize = ms
	}
	if env == "" {
		env = "{}"
	}
	if envMappings == "" {
		envMappings = "{}"
	}

	b := &store.Backend{
		ID:          id,
		Command:     command,
		PoolSize:    minPoolSize,
		MinPoolSize: minPoolSize,
		MaxPoolSize: maxPoolSize,
		ToolPrefix:  toolPrefix,
		Env:         env,
		EnvMappings: envMappings,
		ToolHints:   toolHints,
		Enabled:     enabled,
	}
	if err := h.Store.CreateBackend(b); err != nil {
		log.Printf("web: create backend: %v", err)
		http.Redirect(w, r, "/web/admin/backends?error=Failed+to+create+backend+(ID+may+already+exist)", http.StatusSeeOther)
		return
	}
	h.notifyBackendChange(id)
	http.Redirect(w, r, "/web/admin/backends?success=Backend+created", http.StatusSeeOther)
}

func (h *Handler) AdminBackendsEditHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := r.FormValue("id")
	command := strings.TrimSpace(r.FormValue("command"))
	minPoolSizeStr := r.FormValue("min_pool_size")
	maxPoolSizeStr := r.FormValue("max_pool_size")
	toolPrefix := strings.TrimSpace(r.FormValue("tool_prefix"))
	env := strings.TrimSpace(r.FormValue("env"))
	envMappings := strings.TrimSpace(r.FormValue("env_mappings"))
	toolHints := strings.TrimSpace(r.FormValue("tool_hints"))
	enabled := r.FormValue("enabled") == "on"

	// Validate inputs
	if id == "" || command == "" {
		http.Redirect(w, r, "/web/admin/backends?error=ID+and+command+required", http.StatusSeeOther)
		return
	}
	if !isValidBackendID(id) {
		http.Redirect(w, r, "/web/admin/backends?error=Invalid+backend+ID", http.StatusSeeOther)
		return
	}

	minPoolSize := 1
	if n := parseInt(minPoolSizeStr); n > 0 {
		minPoolSize = n
	}
	maxPoolSize := minPoolSize
	if ms := parseInt(maxPoolSizeStr); ms > 0 {
		maxPoolSize = ms
	}
	if env == "" {
		env = "{}"
	}
	if envMappings == "" {
		envMappings = "{}"
	}

	// Get existing backend to preserve IsSystem flag
	existing, err := h.Store.GetBackend(id)
	isSystem := false
	if err == nil {
		isSystem = existing.IsSystem
	}

	b := &store.Backend{
		ID:          id,
		Command:     command,
		PoolSize:    minPoolSize,
		MinPoolSize: minPoolSize,
		MaxPoolSize: maxPoolSize,
		ToolPrefix:  toolPrefix,
		Env:         env,
		EnvMappings: envMappings,
		ToolHints:   toolHints,
		Enabled:     enabled,
		IsSystem:    isSystem,
	}
	if err := h.Store.UpdateBackend(b); err != nil {
		log.Printf("web: update backend: %v", err)
		http.Redirect(w, r, "/web/admin/backends?error=Failed+to+update+backend", http.StatusSeeOther)
		return
	}
	h.notifyBackendChange(id)
	http.Redirect(w, r, "/web/admin/backends?success=Backend+updated", http.StatusSeeOther)
}

func (h *Handler) AdminBackendsDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := r.FormValue("id")

	// Check if this is a system backend - prevent deletion
	backend, err := h.Store.GetBackend(id)
	if err == nil && backend.IsSystem {
		http.Redirect(w, r, "/web/admin/backends?error=Cannot+delete+system+backend", http.StatusSeeOther)
		return
	}

	if err := h.Store.DeleteBackend(id); err != nil {
		log.Printf("web: delete backend: %v", err)
		http.Redirect(w, r, "/web/admin/backends?error=Failed+to+delete+backend", http.StatusSeeOther)
		return
	}
	h.notifyBackendChange(id)
	http.Redirect(w, r, "/web/admin/backends?success=Backend+deleted", http.StatusSeeOther)
}

func (h *Handler) AdminBackendsProbeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.OnProbeBackend == nil {
		http.Error(w, "Probe not configured", http.StatusNotImplemented)
		return
	}

	backendID := r.FormValue("id")
	if backendID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "error",
			"message": "Missing backend id",
		})
		return
	}

	// Verify the backend exists.
	if _, err := h.Store.GetBackend(backendID); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "error",
			"message": "Backend not found: " + backendID,
		})
		return
	}

	result, err := h.OnProbeBackend(backendID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(result)
}

// notifyBackendChange invokes the OnBackendChange callback if set.
func (h *Handler) notifyBackendChange(backendID string) {
	if h.OnBackendChange != nil {
		h.OnBackendChange(backendID)
	}
}

func (h *Handler) AdminOAuthClientsHandler(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r)

	clients, err := h.Store.ListOAuthClients()
	if err != nil {
		log.Printf("web: list oauth clients: %v", err)
		clients = nil
	}

	h.render(w, "admin_oauth_clients.html", pageData{
		User:    user,
		Title:   "OAuth Clients",
		Error:   r.URL.Query().Get("error"),
		Success: r.URL.Query().Get("success"),
		Data:    clients,
	})
}

func (h *Handler) AdminOAuthClientsCreateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := userFromContext(r)

	clientName := r.FormValue("name")
	if clientName == "" {
		clientName = "OAuth Client"
	}

	// Generate client credentials
	clientID := store.GenerateID()
	clientSecret := store.GenerateID()

	// Default redirect URI (can be changed later)
	defaultRedirect := "http://127.0.0.1:19876/mcp/oauth/callback"

	client := &store.OAuthClient{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		ClientName:   clientName,
		RedirectURIs: "[\"" + defaultRedirect + "\"]",
	}

	if err := h.Store.CreateOAuthClient(client); err != nil {
		log.Printf("web: create oauth client: %v", err)
		http.Redirect(w, r, "/web/admin/oauth-clients?error=Failed+to+create+client", http.StatusSeeOther)
		return
	}

	// Re-fetch all clients for display
	clients, _ := h.Store.ListOAuthClients()

	h.render(w, "admin_oauth_clients.html", pageData{
		User:  user,
		Title: "OAuth Clients",
		Data:  clients,
		Extra: map[string]interface{}{"CreatedClient": client},
	})
}

func (h *Handler) AdminOAuthClientsDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clientID := r.FormValue("id")
	if clientID == "" {
		http.Redirect(w, r, "/web/admin/oauth-clients?error=Missing+client+ID", http.StatusSeeOther)
		return
	}

	if err := h.Store.DeleteOAuthClient(clientID); err != nil {
		log.Printf("web: delete oauth client: %v", err)
		http.Redirect(w, r, "/web/admin/oauth-clients?error=Failed+to+delete+client", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/web/admin/oauth-clients?success=Client+deleted", http.StatusSeeOther)
}

func (h *Handler) AdminSettingsGlobalHintsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	globalHints := strings.TrimSpace(r.FormValue("global_hints"))

	if err := h.Store.SetSetting("global_hints", globalHints); err != nil {
		log.Printf("web: save global hints: %v", err)
		http.Redirect(w, r, "/web/admin/backends?error=Failed+to+save+global+hints", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/web/admin/backends?success=Global+instructions+saved", http.StatusSeeOther)
}
