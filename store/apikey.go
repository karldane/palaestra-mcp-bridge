package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ---------- API Keys ----------

// APIKey represents an API key for programmatic access.
type APIKey struct {
	ID         string
	UserID     string
	Name       string
	KeyHash    string
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	CreatedAt  time.Time
}

// CreateAPIKey creates a new API key.
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

// GetAPIKeyByID retrieves an API key by ID.
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

// GetAPIKeyByHash retrieves an API key by its hash.
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

// ListAPIKeys returns all API keys for a user.
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

// DeleteAPIKey removes an API key.
func (s *Store) DeleteAPIKey(id string) error {
	_, err := s.db.Exec(`DELETE FROM api_keys WHERE id = ?`, id)
	return err
}

// UpdateAPIKeyLastUsed updates the last used timestamp for an API key.
func (s *Store) UpdateAPIKeyLastUsed(id string) error {
	_, err := s.db.Exec(`UPDATE api_keys SET last_used_at = ? WHERE id = ?`, time.Now().UTC(), id)
	return err
}

// ValidateAPIKey validates an API key and returns the associated key record.
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
		if ValidateAPIKeyHash(key, keyHash) {
			return s.GetAPIKeyByID(id)
		}
	}
	return nil, fmt.Errorf("invalid API key")
}

// GenerateAPIKey generates a new API key and its hash.
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

// HashAPIKey creates a bcrypt hash of an API key.
func HashAPIKey(key string) (string, error) {
	hashBytes, err := bcrypt.GenerateFromPassword([]byte(key), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hashBytes), nil
}

// ValidateAPIKeyHash validates an API key against its hash.
func ValidateAPIKeyHash(key, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(key))
	return err == nil
}

// ValidateAPIKey is an alias for ValidateAPIKeyHash for backward compatibility.
func ValidateAPIKey(key, hash string) bool {
	return ValidateAPIKeyHash(key, hash)
}

// ---------- Web Sessions (cookie-based auth for web UI) ----------

// WebSession represents a web UI session.
type WebSession struct {
	Token     string
	UserID    string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// CreateWebSession creates a new web session.
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

// GetWebSession retrieves a web session by token.
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

// DeleteWebSession removes a web session.
func (s *Store) DeleteWebSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM web_sessions WHERE session_id = ?`, token)
	return err
}

// DeleteExpiredWebSessions removes expired web sessions.
func (s *Store) DeleteExpiredWebSessions() (int64, error) {
	result, err := s.db.Exec(`DELETE FROM web_sessions WHERE expires_at < ?`, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
