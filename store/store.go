package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/bcrypt"
)

// Store wraps the SQLite database and provides typed CRUD operations for
// backends, users, user tokens, OAuth sessions, OAuth codes, and OAuth clients.
type Store struct {
	db *sql.DB
}

// New opens (or creates) a SQLite database at path and runs migrations.
func New(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB exposes the raw *sql.DB for advanced use (e.g. transactions).
func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// Incremental migrations for existing databases.
	// Each ALTER TABLE may fail with "duplicate column" — that is expected.
	s.db.Exec(`ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'user'`)
	s.db.Exec(`ALTER TABLE backends ADD COLUMN env_mappings TEXT NOT NULL DEFAULT '{}'`)
	s.db.Exec(`ALTER TABLE backends ADD COLUMN is_system INTEGER NOT NULL DEFAULT 0`)
	s.db.Exec(`ALTER TABLE backends ADD COLUMN min_pool_size INTEGER NOT NULL DEFAULT 1`)
	s.db.Exec(`ALTER TABLE backends ADD COLUMN max_pool_size INTEGER NOT NULL DEFAULT 0`)

	// Migration: rename token to session_id in web_sessions
	// SQLite doesn't support ALTER TABLE rename column, so we handle this in queries
	// The code below checks and migrates if needed
	var hasOldSchema int
	s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('web_sessions') WHERE name='token'`).Scan(&hasOldSchema)
	if hasOldSchema > 0 {
		s.db.Exec(`ALTER TABLE web_sessions RENAME TO web_sessions_old`)
		s.db.Exec(`
			CREATE TABLE web_sessions (
				session_id TEXT PRIMARY KEY,
				user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				expires_at DATETIME NOT NULL,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			);
			INSERT INTO web_sessions (session_id, user_id, expires_at, created_at)
			SELECT token, user_id, expires_at, created_at FROM web_sessions_old;
			DROP TABLE web_sessions_old;
		`)
	}

	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS backends (
	id          TEXT PRIMARY KEY,
	command     TEXT NOT NULL,
	pool_size   INTEGER NOT NULL DEFAULT 1,
	tool_prefix TEXT NOT NULL DEFAULT '',
	env         TEXT NOT NULL DEFAULT '{}',
	env_mappings TEXT NOT NULL DEFAULT '{}',
	enabled     INTEGER NOT NULL DEFAULT 1,
	is_system   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS users (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL DEFAULT '',
	email      TEXT NOT NULL DEFAULT '',
	password   TEXT NOT NULL DEFAULT '',
	role       TEXT NOT NULL DEFAULT 'user',
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE(email)
);

CREATE TABLE IF NOT EXISTS user_tokens (
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	backend_id TEXT NOT NULL REFERENCES backends(id) ON DELETE CASCADE,
	env_key    TEXT NOT NULL,
	value      TEXT NOT NULL,
	PRIMARY KEY (user_id, backend_id, env_key)
);

CREATE TABLE IF NOT EXISTS oauth_clients (
	client_id     TEXT PRIMARY KEY,
	client_secret TEXT NOT NULL DEFAULT '',
	redirect_uris TEXT NOT NULL DEFAULT '[]',
	client_name   TEXT NOT NULL DEFAULT '',
	created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS oauth_codes (
	code           TEXT PRIMARY KEY,
	user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	client_id      TEXT NOT NULL,
	redirect_uri   TEXT NOT NULL,
	code_challenge TEXT NOT NULL DEFAULT '',
	scope          TEXT NOT NULL DEFAULT '',
	expires_at     DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS oauth_sessions (
	access_token  TEXT PRIMARY KEY,
	refresh_token TEXT NOT NULL DEFAULT '',
	user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	client_id     TEXT NOT NULL,
	scope         TEXT NOT NULL DEFAULT '',
	expires_at    DATETIME NOT NULL,
	created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS web_sessions (
	session_id TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at DATETIME NOT NULL,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS api_keys (
	id          TEXT PRIMARY KEY,
	user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name        TEXT NOT NULL DEFAULT '',
	key_hash    TEXT NOT NULL,
	expires_at  DATETIME,
	last_used_at DATETIME,
	created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Cached capabilities for each backend (global - same tools for all users)
CREATE TABLE IF NOT EXISTS backend_capabilities (
	backend_id  TEXT PRIMARY KEY REFERENCES backends(id) ON DELETE CASCADE,
	tools       TEXT NOT NULL,
	tool_count  INTEGER NOT NULL DEFAULT 0,
	updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

// ---------- Backends ----------

type Backend struct {
	ID          string
	Command     string
	PoolSize    int
	ToolPrefix  string
	Env         string // JSON object - systemwide env vars (higher priority than user tokens)
	EnvMappings string // JSON object - maps user token keys to backend-specific keys
	Enabled     bool
	IsSystem    bool // true for mcpbridge - system backend that can't be deleted by non-admins
	MinPoolSize int  // Minimum warm processes to maintain
	MaxPoolSize int  // Maximum warm processes allowed (0 = unlimited)
}

func (s *Store) CreateBackend(b *Backend) error {
	enabled := 0
	if b.Enabled {
		enabled = 1
	}
	isSystem := 0
	if b.IsSystem {
		isSystem = 1
	}
	if b.EnvMappings == "" {
		b.EnvMappings = "{}"
	}
	if b.MinPoolSize == 0 {
		b.MinPoolSize = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO backends (id, command, pool_size, min_pool_size, max_pool_size, tool_prefix, env, env_mappings, enabled, is_system)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.Command, b.PoolSize, b.MinPoolSize, b.MaxPoolSize, b.ToolPrefix, b.Env, b.EnvMappings, enabled, isSystem,
	)
	return err
}

func (s *Store) GetBackend(id string) (*Backend, error) {
	b := &Backend{}
	var enabled, isSystem int
	err := s.db.QueryRow(
		`SELECT id, command, pool_size, min_pool_size, max_pool_size, tool_prefix, env, env_mappings, enabled, is_system FROM backends WHERE id = ?`, id,
	).Scan(&b.ID, &b.Command, &b.PoolSize, &b.MinPoolSize, &b.MaxPoolSize, &b.ToolPrefix, &b.Env, &b.EnvMappings, &enabled, &isSystem)
	if err != nil {
		return nil, err
	}
	b.Enabled = enabled != 0
	b.IsSystem = isSystem != 0
	return b, nil
}

func (s *Store) ListBackends() ([]*Backend, error) {
	rows, err := s.db.Query(
		`SELECT id, command, pool_size, min_pool_size, max_pool_size, tool_prefix, env, env_mappings, enabled, is_system FROM backends ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var backends []*Backend
	for rows.Next() {
		b := &Backend{}
		var enabled, isSystem int
		if err := rows.Scan(&b.ID, &b.Command, &b.PoolSize, &b.MinPoolSize, &b.MaxPoolSize, &b.ToolPrefix, &b.Env, &b.EnvMappings, &enabled, &isSystem); err != nil {
			return nil, err
		}
		b.Enabled = enabled != 0
		b.IsSystem = isSystem != 0
		if b.MinPoolSize == 0 {
			b.MinPoolSize = 1
		}
		backends = append(backends, b)
	}
	return backends, rows.Err()
}

func (s *Store) UpdateBackend(b *Backend) error {
	enabled := 0
	if b.Enabled {
		enabled = 1
	}
	isSystem := 0
	if b.IsSystem {
		isSystem = 1
	}
	if b.EnvMappings == "" {
		b.EnvMappings = "{}"
	}
	if b.MinPoolSize == 0 {
		b.MinPoolSize = 1
	}
	_, err := s.db.Exec(
		`UPDATE backends SET command=?, pool_size=?, min_pool_size=?, max_pool_size=?, tool_prefix=?, env=?, env_mappings=?, enabled=?, is_system=? WHERE id=?`,
		b.Command, b.PoolSize, b.MinPoolSize, b.MaxPoolSize, b.ToolPrefix, b.Env, b.EnvMappings, enabled, isSystem, b.ID,
	)
	return err
}

func (s *Store) DeleteBackend(id string) error {
	_, err := s.db.Exec(`DELETE FROM backends WHERE id = ?`, id)
	return err
}

// ---------- Backend Capabilities Cache ----------

type BackendCapabilities struct {
	BackendID string
	Tools     []map[string]interface{}
	ToolCount int
	UpdatedAt time.Time
}

func (s *Store) SetBackendCapabilities(backendID string, tools []map[string]interface{}) error {
	toolsJSON, err := json.Marshal(tools)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`
		INSERT INTO backend_capabilities (backend_id, tools, tool_count, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(backend_id) DO UPDATE SET tools = excluded.tools, tool_count = excluded.tool_count, updated_at = excluded.updated_at`,
		backendID, toolsJSON, len(tools),
	)
	return err
}

func (s *Store) GetBackendCapabilities(backendID string) (*BackendCapabilities, error) {
	var caps BackendCapabilities
	var toolsJSON []byte
	var updatedAt sql.NullTime
	err := s.db.QueryRow(
		`SELECT backend_id, tools, tool_count, updated_at FROM backend_capabilities WHERE backend_id = ?`,
		backendID,
	).Scan(&caps.BackendID, &toolsJSON, &caps.ToolCount, &updatedAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(toolsJSON, &caps.Tools); err != nil {
		return nil, err
	}
	if updatedAt.Valid {
		caps.UpdatedAt = updatedAt.Time
	}
	return &caps, nil
}

func (s *Store) GetAllBackendCapabilities() (map[string]*BackendCapabilities, error) {
	rows, err := s.db.Query(`SELECT backend_id, tools, tool_count, updated_at FROM backend_capabilities`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]*BackendCapabilities)
	for rows.Next() {
		var caps BackendCapabilities
		var toolsJSON []byte
		var updatedAt sql.NullTime
		if err := rows.Scan(&caps.BackendID, &toolsJSON, &caps.ToolCount, &updatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(toolsJSON, &caps.Tools); err != nil {
			return nil, err
		}
		if updatedAt.Valid {
			caps.UpdatedAt = updatedAt.Time
		}
		result[caps.BackendID] = &caps
	}
	return result, rows.Err()
}

func (s *Store) DeleteBackendCapabilities(backendID string) error {
	_, err := s.db.Exec(`DELETE FROM backend_capabilities WHERE backend_id = ?`, backendID)
	return err
}

// MigrateDefaultBackend migrates the "default" backend to "mcpbridge" and marks it as system.
// This is called on startup to rename the legacy default backend.
func (s *Store) MigrateDefaultBackend() error {
	// Check if "default" exists
	var exists int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM backends WHERE id = 'default'`).Scan(&exists)
	if err != nil {
		return err
	}
	if exists == 0 {
		return nil // No default backend to migrate
	}

	// Disable foreign key checks temporarily
	_, err = s.db.Exec(`PRAGMA foreign_keys = OFF`)
	if err != nil {
		return err
	}
	defer s.db.Exec(`PRAGMA foreign_keys = ON`)

	// First update user_tokens to reference the new backend ID
	_, err = s.db.Exec(
		`UPDATE user_tokens SET backend_id = 'mcpbridge' WHERE backend_id = 'default'`,
	)
	if err != nil {
		return err
	}

	// Then migrate default -> mcpbridge and mark as system
	_, err = s.db.Exec(
		`UPDATE backends SET id = 'mcpbridge', is_system = 1 WHERE id = 'default'`,
	)
	return err
}

// ---------- Users ----------

type User struct {
	ID        string
	Name      string
	Email     string
	Password  string // bcrypt hash (auto-hashed by CreateUser/UpdateUser)
	Role      string // "admin" or "user"
	CreatedAt time.Time
}

func (s *Store) CreateUser(u *User) error {
	if u.ID == "" {
		u.ID = generateID()
	}
	if u.Role == "" {
		u.Role = "user"
	}
	// Hash password if it's not already a bcrypt hash.
	if !IsBcrypt(u.Password) && u.Password != "" {
		hash, err := HashPassword(u.Password)
		if err != nil {
			return err
		}
		u.Password = hash
	}
	_, err := s.db.Exec(
		`INSERT INTO users (id, name, email, password, role) VALUES (?, ?, ?, ?, ?)`,
		u.ID, u.Name, u.Email, u.Password, u.Role,
	)
	return err
}

func (s *Store) GetUser(id string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(
		`SELECT id, name, email, password, role, created_at FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Name, &u.Email, &u.Password, &u.Role, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) GetUserByEmail(email string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(
		`SELECT id, name, email, password, role, created_at FROM users WHERE email = ?`, email,
	).Scan(&u.ID, &u.Name, &u.Email, &u.Password, &u.Role, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) ListUsers() ([]*User, error) {
	rows, err := s.db.Query(
		`SELECT id, name, email, password, role, created_at FROM users ORDER BY email`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		u := &User{}
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.Password, &u.Role, &u.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *Store) UpdateUser(u *User) error {
	// Hash password if it's not already a bcrypt hash.
	if !IsBcrypt(u.Password) && u.Password != "" {
		hash, err := HashPassword(u.Password)
		if err != nil {
			return err
		}
		u.Password = hash
	}
	_, err := s.db.Exec(
		`UPDATE users SET name=?, email=?, password=?, role=? WHERE id=?`,
		u.Name, u.Email, u.Password, u.Role, u.ID,
	)
	return err
}

func (s *Store) DeleteUser(id string) error {
	_, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

// ---------- User Tokens (per-user service credentials) ----------

type UserToken struct {
	UserID    string
	BackendID string
	EnvKey    string
	Value     string
}

func (s *Store) SetUserToken(t *UserToken) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO user_tokens (user_id, backend_id, env_key, value)
		 VALUES (?, ?, ?, ?)`,
		t.UserID, t.BackendID, t.EnvKey, t.Value,
	)
	return err
}

func (s *Store) GetUserTokens(userID, backendID string) ([]*UserToken, error) {
	rows, err := s.db.Query(
		`SELECT user_id, backend_id, env_key, value
		 FROM user_tokens WHERE user_id = ? AND backend_id = ?`,
		userID, backendID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []*UserToken
	for rows.Next() {
		t := &UserToken{}
		if err := rows.Scan(&t.UserID, &t.BackendID, &t.EnvKey, &t.Value); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

func (s *Store) GetAllUserTokens(userID string) ([]*UserToken, error) {
	rows, err := s.db.Query(
		`SELECT user_id, backend_id, env_key, value
		 FROM user_tokens WHERE user_id = ?`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []*UserToken
	for rows.Next() {
		t := &UserToken{}
		if err := rows.Scan(&t.UserID, &t.BackendID, &t.EnvKey, &t.Value); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

func (s *Store) DeleteUserToken(userID, backendID, envKey string) error {
	_, err := s.db.Exec(
		`DELETE FROM user_tokens WHERE user_id = ? AND backend_id = ? AND env_key = ?`,
		userID, backendID, envKey,
	)
	return err
}

// ---------- OAuth Clients (Dynamic Registration) ----------

type OAuthClient struct {
	ClientID     string
	ClientSecret string
	RedirectURIs string // JSON array
	ClientName   string
	CreatedAt    time.Time
}

func (s *Store) CreateOAuthClient(c *OAuthClient) error {
	if c.ClientID == "" {
		c.ClientID = generateID()
	}
	_, err := s.db.Exec(
		`INSERT INTO oauth_clients (client_id, client_secret, redirect_uris, client_name)
		 VALUES (?, ?, ?, ?)`,
		c.ClientID, c.ClientSecret, c.RedirectURIs, c.ClientName,
	)
	return err
}

func (s *Store) GetOAuthClient(clientID string) (*OAuthClient, error) {
	c := &OAuthClient{}
	err := s.db.QueryRow(
		`SELECT client_id, client_secret, redirect_uris, client_name, created_at
		 FROM oauth_clients WHERE client_id = ?`, clientID,
	).Scan(&c.ClientID, &c.ClientSecret, &c.RedirectURIs, &c.ClientName, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Store) ListOAuthClients() ([]OAuthClient, error) {
	rows, err := s.db.Query(
		`SELECT client_id, client_secret, redirect_uris, client_name, created_at FROM oauth_clients ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var clients []OAuthClient
	for rows.Next() {
		var c OAuthClient
		if err := rows.Scan(&c.ClientID, &c.ClientSecret, &c.RedirectURIs, &c.ClientName, &c.CreatedAt); err != nil {
			return nil, err
		}
		clients = append(clients, c)
	}
	return clients, nil
}

func (s *Store) DeleteOAuthClient(clientID string) error {
	_, err := s.db.Exec(`DELETE FROM oauth_clients WHERE client_id = ?`, clientID)
	return err
}

// ---------- OAuth Codes ----------

type OAuthCode struct {
	Code          string
	UserID        string
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	Scope         string
	ExpiresAt     time.Time
}

func (s *Store) CreateOAuthCode(c *OAuthCode) error {
	if c.Code == "" {
		c.Code = generateID()
	}
	_, err := s.db.Exec(
		`INSERT INTO oauth_codes (code, user_id, client_id, redirect_uri, code_challenge, scope, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.Code, c.UserID, c.ClientID, c.RedirectURI, c.CodeChallenge, c.Scope, c.ExpiresAt,
	)
	return err
}

func (s *Store) GetOAuthCode(code string) (*OAuthCode, error) {
	c := &OAuthCode{}
	err := s.db.QueryRow(
		`SELECT code, user_id, client_id, redirect_uri, code_challenge, scope, expires_at
		 FROM oauth_codes WHERE code = ?`, code,
	).Scan(&c.Code, &c.UserID, &c.ClientID, &c.RedirectURI, &c.CodeChallenge, &c.Scope, &c.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Store) DeleteOAuthCode(code string) error {
	_, err := s.db.Exec(`DELETE FROM oauth_codes WHERE code = ?`, code)
	return err
}

// ---------- OAuth Sessions (access tokens) ----------

type OAuthSession struct {
	AccessToken  string
	RefreshToken string
	UserID       string
	ClientID     string
	Scope        string
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

func (s *Store) CreateOAuthSession(sess *OAuthSession) error {
	if sess.AccessToken == "" {
		sess.AccessToken = generateID()
	}
	if sess.RefreshToken == "" {
		sess.RefreshToken = generateID()
	}
	_, err := s.db.Exec(
		`INSERT INTO oauth_sessions (access_token, refresh_token, user_id, client_id, scope, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		sess.AccessToken, sess.RefreshToken, sess.UserID, sess.ClientID, sess.Scope, sess.ExpiresAt,
	)
	return err
}

func (s *Store) GetOAuthSession(accessToken string) (*OAuthSession, error) {
	sess := &OAuthSession{}
	err := s.db.QueryRow(
		`SELECT access_token, refresh_token, user_id, client_id, scope, expires_at, created_at
		 FROM oauth_sessions WHERE access_token = ?`, accessToken,
	).Scan(&sess.AccessToken, &sess.RefreshToken, &sess.UserID, &sess.ClientID,
		&sess.Scope, &sess.ExpiresAt, &sess.CreatedAt)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

func (s *Store) GetOAuthSessionByRefresh(refreshToken string) (*OAuthSession, error) {
	sess := &OAuthSession{}
	err := s.db.QueryRow(
		`SELECT access_token, refresh_token, user_id, client_id, scope, expires_at, created_at
		 FROM oauth_sessions WHERE refresh_token = ?`, refreshToken,
	).Scan(&sess.AccessToken, &sess.RefreshToken, &sess.UserID, &sess.ClientID,
		&sess.Scope, &sess.ExpiresAt, &sess.CreatedAt)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

func (s *Store) DeleteOAuthSession(accessToken string) error {
	_, err := s.db.Exec(`DELETE FROM oauth_sessions WHERE access_token = ?`, accessToken)
	return err
}

// DeleteExpiredSessions removes sessions past their expiry.
func (s *Store) DeleteExpiredSessions() (int64, error) {
	result, err := s.db.Exec(`DELETE FROM oauth_sessions WHERE expires_at < ?`, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// DeleteExpiredCodes removes auth codes past their expiry.
func (s *Store) DeleteExpiredCodes() (int64, error) {
	result, err := s.db.Exec(`DELETE FROM oauth_codes WHERE expires_at < ?`, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ---------- Password hashing ----------

// HashPassword returns a bcrypt hash of the given plaintext password.
func HashPassword(plain string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// CheckPassword compares a stored password (which may be a bcrypt hash or
// legacy plaintext) against a plaintext candidate. Returns nil on match.
func CheckPassword(stored, plain string) error {
	if IsBcrypt(stored) {
		return bcrypt.CompareHashAndPassword([]byte(stored), []byte(plain))
	}
	// Legacy plaintext comparison.
	if stored != plain {
		return fmt.Errorf("password mismatch")
	}
	return nil
}

// IsBcrypt returns true if s looks like a bcrypt hash (starts with "$2a$",
// "$2b$", or "$2y$").
func IsBcrypt(s string) bool {
	return strings.HasPrefix(s, "$2a$") || strings.HasPrefix(s, "$2b$") || strings.HasPrefix(s, "$2y$")
}

// ---------- Helpers ----------

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// GenerateID returns a random hex string (exported for external use).
func GenerateID() string {
	return generateID()
}

func GenerateAPIKey() (key string, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	key = "mcp_" + hex.EncodeToString(b)
	hashBytes, err := bcrypt.GenerateFromPassword([]byte(key), bcrypt.DefaultCost)
	if err != nil {
		return "", "", err
	}
	return key, string(hashBytes), nil
}

func HashAPIKey(key string) (string, error) {
	hashBytes, err := bcrypt.GenerateFromPassword([]byte(key), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hashBytes), nil
}

func ValidateAPIKey(key, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(key))
	return err == nil
}

// ---------- Web Sessions (cookie-based auth for web UI) ----------

type WebSession struct {
	Token     string
	UserID    string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// ---------- API Keys ----------

type APIKey struct {
	ID         string
	UserID     string
	Name       string
	KeyHash    string
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	CreatedAt  time.Time
}

func (s *Store) CreateAPIKey(key *APIKey) error {
	if key.ID == "" {
		key.ID = generateID()
	}
	_, err := s.db.Exec(
		`INSERT INTO api_keys (id, user_id, name, key_hash, expires_at) VALUES (?, ?, ?, ?, ?)`,
		key.ID, key.UserID, key.Name, key.KeyHash, key.ExpiresAt,
	)
	return err
}

func (s *Store) GetAPIKeyByID(id string) (*APIKey, error) {
	key := &APIKey{}
	var expiresAt, lastUsedAt sql.NullTime
	err := s.db.QueryRow(
		`SELECT id, user_id, name, key_hash, expires_at, last_used_at, created_at FROM api_keys WHERE id = ?`, id,
	).Scan(&key.ID, &key.UserID, &key.Name, &key.KeyHash, &expiresAt, &lastUsedAt, &key.CreatedAt)
	if err != nil {
		return nil, err
	}
	if expiresAt.Valid {
		key.ExpiresAt = &expiresAt.Time
	}
	if lastUsedAt.Valid {
		key.LastUsedAt = &lastUsedAt.Time
	}
	return key, nil
}

func (s *Store) GetAPIKeyByHash(hash string) (*APIKey, error) {
	key := &APIKey{}
	var expiresAt, lastUsedAt sql.NullTime
	err := s.db.QueryRow(
		`SELECT id, user_id, name, key_hash, expires_at, last_used_at, created_at FROM api_keys WHERE key_hash = ?`, hash,
	).Scan(&key.ID, &key.UserID, &key.Name, &key.KeyHash, &expiresAt, &lastUsedAt, &key.CreatedAt)
	if err != nil {
		return nil, err
	}
	if expiresAt.Valid {
		key.ExpiresAt = &expiresAt.Time
	}
	if lastUsedAt.Valid {
		key.LastUsedAt = &lastUsedAt.Time
	}
	return key, nil
}

func (s *Store) ListAPIKeys(userID string) ([]APIKey, error) {
	rows, err := s.db.Query(
		`SELECT id, user_id, name, key_hash, expires_at, last_used_at, created_at FROM api_keys WHERE user_id = ? ORDER BY created_at DESC`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []APIKey
	for rows.Next() {
		var key APIKey
		var expiresAt, lastUsedAt sql.NullTime
		if err := rows.Scan(&key.ID, &key.UserID, &key.Name, &key.KeyHash, &expiresAt, &lastUsedAt, &key.CreatedAt); err != nil {
			return nil, err
		}
		if expiresAt.Valid {
			key.ExpiresAt = &expiresAt.Time
		}
		if lastUsedAt.Valid {
			key.LastUsedAt = &lastUsedAt.Time
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func (s *Store) DeleteAPIKey(id string) error {
	_, err := s.db.Exec(`DELETE FROM api_keys WHERE id = ?`, id)
	return err
}

func (s *Store) UpdateAPIKeyLastUsed(id string) error {
	_, err := s.db.Exec(`UPDATE api_keys SET last_used_at = ? WHERE id = ?`, time.Now().UTC(), id)
	return err
}

func (s *Store) ValidateAPIKey(key string) (*APIKey, error) {
	rows, err := s.db.Query(`SELECT id, key_hash FROM api_keys`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var id, keyHash string
		if err := rows.Scan(&id, &keyHash); err != nil {
			continue
		}
		if ValidateAPIKey(key, keyHash) {
			return s.GetAPIKeyByID(id)
		}
	}
	return nil, fmt.Errorf("invalid API key")
}

func (s *Store) CreateWebSession(sess *WebSession) error {
	if sess.Token == "" {
		sess.Token = generateID()
	}
	_, err := s.db.Exec(
		`INSERT INTO web_sessions (session_id, user_id, expires_at) VALUES (?, ?, ?)`,
		sess.Token, sess.UserID, sess.ExpiresAt,
	)
	return err
}

func (s *Store) GetWebSession(token string) (*WebSession, error) {
	sess := &WebSession{}
	err := s.db.QueryRow(
		`SELECT session_id, user_id, expires_at, created_at FROM web_sessions WHERE session_id = ?`, token,
	).Scan(&sess.Token, &sess.UserID, &sess.ExpiresAt, &sess.CreatedAt)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

func (s *Store) DeleteWebSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM web_sessions WHERE session_id = ?`, token)
	return err
}

func (s *Store) DeleteExpiredWebSessions() (int64, error) {
	result, err := s.db.Exec(`DELETE FROM web_sessions WHERE expires_at < ?`, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
