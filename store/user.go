package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/mcp-bridge/mcp-bridge/internal/crypto"
	"golang.org/x/crypto/bcrypt"
)

// User represents a user account in the system.
type User struct {
	ID           string
	Name         string
	Email        string
	Password     string // bcrypt hash (auto-hashed by CreateUser/UpdateUser)
	PasswordSalt string // salt for deriving user DEK
	Role         string // "admin" or "user"
	CreatedAt    time.Time
}

// CreateUser inserts a new user into the database.
// Password is automatically hashed if not already a bcrypt hash.
// Password salt is automatically generated.
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
	// Generate password salt for user-derived encryption
	if u.PasswordSalt == "" {
		salt, err := crypto.GenerateSalt()
		if err != nil {
			return err
		}
		u.PasswordSalt = salt
	}
	_, err := s.db.Exec(
		`INSERT INTO users (id, name, email, password, password_salt, role) VALUES (?, ?, ?, ?, ?, ?)`,
		u.ID, u.Name, u.Email, u.Password, u.PasswordSalt, u.Role,
	)
	return err
}

// GetUser retrieves a user by ID.
func (s *Store) GetUser(id string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(
		`SELECT id, name, email, password, COALESCE(password_salt, ''), role, created_at FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Name, &u.Email, &u.Password, &u.PasswordSalt, &u.Role, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// GetUserByEmail retrieves a user by email address.
func (s *Store) GetUserByEmail(email string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(
		`SELECT id, name, email, password, COALESCE(password_salt, ''), role, created_at FROM users WHERE email = ?`, email,
	).Scan(&u.ID, &u.Name, &u.Email, &u.Password, &u.PasswordSalt, &u.Role, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// ListUsers returns all users ordered by email.
func (s *Store) ListUsers() ([]*User, error) {
	rows, err := s.db.Query(
		`SELECT id, name, email, password, COALESCE(password_salt, ''), role, created_at FROM users ORDER BY email`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		u := &User{}
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.Password, &u.PasswordSalt, &u.Role, &u.CreatedAt); err != nil {
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
		`UPDATE users SET name=?, email=?, password=?, password_salt=?, role=? WHERE id=?`,
		u.Name, u.Email, u.Password, u.PasswordSalt, u.Role, u.ID,
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
	UserID         string
	BackendID      string
	EnvKey         string
	Value          string
	Encrypted      string // encrypted value (with master key or user DEK)
	EncryptedDEK   string // DEK encrypted with user password-derived key
	EncryptionType string // "legacy" (master key), "user" (user-derived key)
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
		`SELECT user_id, backend_id, env_key, value, COALESCE(encrypted_value, '') as encrypted_value,
		 COALESCE(encrypted_dek, '') as encrypted_dek, COALESCE(encryption_type, 'legacy') as encryption_type
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
		if err := rows.Scan(&t.UserID, &t.BackendID, &t.EnvKey, &t.Value, &t.Encrypted, &t.EncryptedDEK, &t.EncryptionType); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// GetAllUserTokens retrieves all tokens for a user across all backends.
func (s *Store) GetAllUserTokens(userID string) ([]*UserToken, error) {
	rows, err := s.db.Query(
		`SELECT user_id, backend_id, env_key, value, COALESCE(encrypted_value, '') as encrypted_value,
		 COALESCE(encrypted_dek, '') as encrypted_dek, COALESCE(encryption_type, 'legacy') as encryption_type
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
		if err := rows.Scan(&t.UserID, &t.BackendID, &t.EnvKey, &t.Value, &t.Encrypted, &t.EncryptedDEK, &t.EncryptionType); err != nil {
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

// SetUserTokenWithUserDEK encrypts a token using a user-derived key.
// It generates a random DEK for the token, encrypts the token value with the DEK,
// then encrypts the DEK with the user's derived key.
func (s *Store) SetUserTokenWithUserDEK(userID, backendID, envKey, tokenValue string, userDEK []byte) error {
	if userDEK == nil {
		return errors.New("user DEK is required")
	}

	tokenDEK, err := crypto.GenerateRandomKey()
	if err != nil {
		return err
	}
	defer crypto.Zeroize(tokenDEK)

	ciphertext, err := crypto.AES256GCMEncrypt(tokenDEK, []byte(tokenValue))
	if err != nil {
		return err
	}

	encryptedDEK, err := crypto.AES256GCMEncrypt(userDEK, tokenDEK)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO user_tokens (user_id, backend_id, env_key, value, encrypted_value, encrypted_dek, encryption_type)
		 VALUES (?, ?, ?, '', ?, ?, 'user')`,
		userID, backendID, envKey, string(ciphertext), string(encryptedDEK),
	)
	return err
}

// GetUserTokenDecryptedWithUserDEK retrieves and decrypts a user token using the user's derived key.
func (s *Store) GetUserTokenDecryptedWithUserDEK(userID, backendID, envKey string, userDEK []byte) (string, error) {
	if userDEK == nil {
		return "", errors.New("user DEK is required")
	}

	var value, encrypted, encryptedDEK, encryptionType string
	err := s.db.QueryRow(
		`SELECT value, COALESCE(encrypted_value, ''), COALESCE(encrypted_dek, ''), COALESCE(encryption_type, 'legacy')
		 FROM user_tokens WHERE user_id = ? AND backend_id = ? AND env_key = ?`,
		userID, backendID, envKey,
	).Scan(&value, &encrypted, &encryptedDEK, &encryptionType)

	if err == sql.ErrNoRows {
		return "", err
	}
	if err != nil {
		return "", err
	}

	if encryptionType == "user" && encryptedDEK != "" && userDEK != nil {
		dekCiphertext, err := crypto.AES256GCMDecrypt(userDEK, []byte(encryptedDEK))
		if err != nil {
			return "", err
		}
		defer crypto.Zeroize(dekCiphertext)

		plaintext, err := crypto.AES256GCMDecrypt(dekCiphertext, []byte(encrypted))
		if err != nil {
			return "", err
		}
		return string(plaintext), nil
	}

	if encrypted != "" && s.keyStore != nil {
		decrypted, err := s.keyStore.DecryptSecret([]byte(encrypted))
		if err != nil {
			return "", err
		}
		return string(decrypted), nil
	}

	return value, nil
}

// UpdateUserPasswordSalt updates a user's password salt.
func (s *Store) UpdateUserPasswordSalt(userID, salt string) error {
	_, err := s.db.Exec(`UPDATE users SET password_salt = ? WHERE id = ?`, salt, userID)
	return err
}

// GetUserPasswordSalt retrieves a user's password salt.
func (s *Store) GetUserPasswordSalt(userID string) (string, error) {
	var salt string
	err := s.db.QueryRow(`SELECT COALESCE(password_salt, '') FROM users WHERE id = ?`, userID).Scan(&salt)
	return salt, err
}
