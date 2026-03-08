package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
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
	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS backends (
	id         TEXT PRIMARY KEY,
	command    TEXT NOT NULL,
	pool_size  INTEGER NOT NULL DEFAULT 1,
	tool_prefix TEXT NOT NULL DEFAULT '',
	env        TEXT NOT NULL DEFAULT '{}',
	enabled    INTEGER NOT NULL DEFAULT 1
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
	token      TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at DATETIME NOT NULL,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`

// ---------- Backends ----------

type Backend struct {
	ID         string
	Command    string
	PoolSize   int
	ToolPrefix string
	Env        string // JSON object
	Enabled    bool
}

func (s *Store) CreateBackend(b *Backend) error {
	enabled := 0
	if b.Enabled {
		enabled = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO backends (id, command, pool_size, tool_prefix, env, enabled)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		b.ID, b.Command, b.PoolSize, b.ToolPrefix, b.Env, enabled,
	)
	return err
}

func (s *Store) GetBackend(id string) (*Backend, error) {
	b := &Backend{}
	var enabled int
	err := s.db.QueryRow(
		`SELECT id, command, pool_size, tool_prefix, env, enabled FROM backends WHERE id = ?`, id,
	).Scan(&b.ID, &b.Command, &b.PoolSize, &b.ToolPrefix, &b.Env, &enabled)
	if err != nil {
		return nil, err
	}
	b.Enabled = enabled != 0
	return b, nil
}

func (s *Store) ListBackends() ([]*Backend, error) {
	rows, err := s.db.Query(
		`SELECT id, command, pool_size, tool_prefix, env, enabled FROM backends ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var backends []*Backend
	for rows.Next() {
		b := &Backend{}
		var enabled int
		if err := rows.Scan(&b.ID, &b.Command, &b.PoolSize, &b.ToolPrefix, &b.Env, &enabled); err != nil {
			return nil, err
		}
		b.Enabled = enabled != 0
		backends = append(backends, b)
	}
	return backends, rows.Err()
}

func (s *Store) UpdateBackend(b *Backend) error {
	enabled := 0
	if b.Enabled {
		enabled = 1
	}
	_, err := s.db.Exec(
		`UPDATE backends SET command=?, pool_size=?, tool_prefix=?, env=?, enabled=? WHERE id=?`,
		b.Command, b.PoolSize, b.ToolPrefix, b.Env, enabled, b.ID,
	)
	return err
}

func (s *Store) DeleteBackend(id string) error {
	_, err := s.db.Exec(`DELETE FROM backends WHERE id = ?`, id)
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

// ---------- Web Sessions (cookie-based auth for web UI) ----------

type WebSession struct {
	Token     string
	UserID    string
	ExpiresAt time.Time
	CreatedAt time.Time
}

func (s *Store) CreateWebSession(sess *WebSession) error {
	if sess.Token == "" {
		sess.Token = generateID()
	}
	_, err := s.db.Exec(
		`INSERT INTO web_sessions (token, user_id, expires_at) VALUES (?, ?, ?)`,
		sess.Token, sess.UserID, sess.ExpiresAt,
	)
	return err
}

func (s *Store) GetWebSession(token string) (*WebSession, error) {
	sess := &WebSession{}
	err := s.db.QueryRow(
		`SELECT token, user_id, expires_at, created_at FROM web_sessions WHERE token = ?`, token,
	).Scan(&sess.Token, &sess.UserID, &sess.ExpiresAt, &sess.CreatedAt)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

func (s *Store) DeleteWebSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM web_sessions WHERE token = ?`, token)
	return err
}

func (s *Store) DeleteExpiredWebSessions() (int64, error) {
	result, err := s.db.Exec(`DELETE FROM web_sessions WHERE expires_at < ?`, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
