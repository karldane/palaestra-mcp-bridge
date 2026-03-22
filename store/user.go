package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// User represents a user account in the system.
type User struct {
	ID        string
	Name      string
	Email     string
	Password  string // bcrypt hash (auto-hashed by CreateUser/UpdateUser)
	Role      string // "admin" or "user"
	CreatedAt time.Time
}

// CreateUser inserts a new user into the database.
// Password is automatically hashed if not already a bcrypt hash.
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

// GetUser retrieves a user by ID.
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

// GetUserByEmail retrieves a user by email address.
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

// ListUsers returns all users ordered by email.
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

// UpdateUser updates an existing user.
// Password is automatically hashed if not already a bcrypt hash.
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

// DeleteUser removes a user by ID.
func (s *Store) DeleteUser(id string) error {
	_, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

// ---------- User Tokens (per-user service credentials) ----------

// UserToken stores a user's credentials for a specific backend.
type UserToken struct {
	UserID    string
	BackendID string
	EnvKey    string
	Value     string
	Encrypted string
}

// SetUserToken creates or updates a user token.
func (s *Store) SetUserToken(t *UserToken) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO user_tokens (user_id, backend_id, env_key, value)
		 VALUES (?, ?, ?, ?)`,
		t.UserID, t.BackendID, t.EnvKey, t.Value,
	)
	return err
}

// GetUserTokens retrieves tokens for a specific user and backend.
func (s *Store) GetUserTokens(userID, backendID string) ([]*UserToken, error) {
	rows, err := s.db.Query(
		`SELECT user_id, backend_id, env_key, value, COALESCE(encrypted_value, '') as encrypted_value
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
		if err := rows.Scan(&t.UserID, &t.BackendID, &t.EnvKey, &t.Value, &t.Encrypted); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// GetAllUserTokens retrieves all tokens for a user across all backends.
func (s *Store) GetAllUserTokens(userID string) ([]*UserToken, error) {
	rows, err := s.db.Query(
		`SELECT user_id, backend_id, env_key, value, COALESCE(encrypted_value, '') as encrypted_value
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
		if err := rows.Scan(&t.UserID, &t.BackendID, &t.EnvKey, &t.Value, &t.Encrypted); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// DeleteUserToken removes a specific user token.
func (s *Store) DeleteUserToken(userID, backendID, envKey string) error {
	_, err := s.db.Exec(
		`DELETE FROM user_tokens WHERE user_id = ? AND backend_id = ? AND env_key = ?`,
		userID, backendID, envKey,
	)
	return err
}

// SetUserTokenEncrypted stores an already-encrypted secret.
func (s *Store) SetUserTokenEncrypted(userID, backendID, envKey, encryptedValue string) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO user_tokens (user_id, backend_id, env_key, value, encrypted_value)
		 VALUES (?, ?, ?, '', ?)`,
		userID, backendID, envKey, encryptedValue,
	)
	return err
}

// GetUserTokenDecrypted retrieves and decrypts a user token.
func (s *Store) GetUserTokenDecrypted(userID, backendID, envKey string) (string, error) {
	if s.keyStore == nil {
		return "", errors.New("keystore not initialized")
	}

	var value, encrypted string
	err := s.db.QueryRow(
		`SELECT value, COALESCE(encrypted_value, '') FROM user_tokens 
		 WHERE user_id = ? AND backend_id = ? AND env_key = ?`,
		userID, backendID, envKey,
	).Scan(&value, &encrypted)

	if err == sql.ErrNoRows {
		return "", err
	}
	if err != nil {
		return "", err
	}

	if encrypted != "" {
		decrypted, err := s.keyStore.DecryptSecret([]byte(encrypted))
		if err != nil {
			return "", err
		}
		return string(decrypted), nil
	}

	return value, nil
}

// GetUserTokensDecrypted retrieves all tokens for a user/backend and decrypts them.
func (s *Store) GetUserTokensDecrypted(userID, backendID string) ([]UserToken, error) {
	tokens, err := s.GetUserTokens(userID, backendID)
	if err != nil {
		return nil, err
	}

	var result []UserToken
	for _, t := range tokens {
		token := UserToken{
			UserID:    t.UserID,
			BackendID: t.BackendID,
			EnvKey:    t.EnvKey,
			Value:     t.Value,
			Encrypted: t.Encrypted,
		}

		if t.Encrypted != "" && s.keyStore != nil {
			decrypted, err := s.keyStore.DecryptSecret([]byte(t.Encrypted))
			if err == nil {
				token.Value = string(decrypted)
			}
		}

		result = append(result, token)
	}

	return result, nil
}

// HasEncryptedTokens checks if any tokens are encrypted.
func (s *Store) HasEncryptedTokens() (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM user_tokens WHERE encrypted_value IS NOT NULL AND encrypted_value != ''`,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// MigrateSecrets migrates plaintext secrets to encrypted format.
func (s *Store) MigrateSecrets(ctx context.Context) error {
	if s.keyStore == nil {
		return errors.New("keystore not initialized")
	}

	rows, err := s.db.Query(
		`SELECT user_id, backend_id, env_key, value FROM user_tokens 
		 WHERE (encrypted_value IS NULL OR encrypted_value = '') AND value != ''`,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var userID, backendID, envKey, value string
		if err := rows.Scan(&userID, &backendID, &envKey, &value); err != nil {
			return err
		}

		encrypted, err := s.keyStore.EncryptSecret([]byte(value))
		if err != nil {
			return err
		}

		_, err = s.db.Exec(
			`UPDATE user_tokens SET encrypted_value = ? WHERE user_id = ? AND backend_id = ? AND env_key = ?`,
			string(encrypted), userID, backendID, envKey,
		)
		if err != nil {
			return err
		}
	}

	return rows.Err()
}

// VerifyEncryptedSecrets verifies all secrets can be decrypted.
// Returns success count, fail count, and error.
func (s *Store) VerifyEncryptedSecrets(ctx context.Context) (int, int, error) {
	if s.keyStore == nil {
		return 0, 0, errors.New("keystore not initialized")
	}

	rows, err := s.db.Query(
		`SELECT user_id, backend_id, env_key, value, COALESCE(encrypted_value, '') FROM user_tokens`,
	)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()

	success := 0
	fail := 0

	for rows.Next() {
		var userID, backendID, envKey, value, encrypted string
		if err := rows.Scan(&userID, &backendID, &envKey, &value, &encrypted); err != nil {
			return 0, 0, err
		}

		if encrypted == "" {
			fail++
			continue
		}

		_, err := s.keyStore.DecryptSecret([]byte(encrypted))
		if err != nil {
			fail++
		} else {
			success++
		}
	}

	return success, fail, rows.Err()
}

// ---------- Password hashing ----------

// HashPassword returns a bcrypt hash of the given plaintext password.
func HashPassword(plain string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
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
		return bcrypt.ErrMismatchedHashAndPassword
	}
	return nil
}

// IsBcrypt returns true if s looks like a bcrypt hash (starts with "$2a$",
// "$2b$", or "$2y$").
func IsBcrypt(s string) bool {
	return len(s) > 4 && (s[:4] == "$2a$" || s[:4] == "$2b$" || s[:4] == "$2y$")
}
