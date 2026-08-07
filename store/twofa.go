package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/mcp-bridge/mcp-bridge/internal/crypto"
)

// ErrNo2FA is returned when a user has no configured 2FA method.
var ErrNo2FA = errors.New("no 2FA configured for user")

// SetUser2FA persists a user's 2FA configuration. The plaintext secret (the
// TOTP shared secret) is wrapped with the user-derived DEK over the master
// KEK, mirroring SetUserTokenWithUserDEK. Both keys are required to recover
// the secret later; a master-key compromise alone does not reveal it.
func (s *Store) SetUser2FA(userID, method string, secret []byte, userDEK []byte) error {
	if userDEK == nil {
		return errors.New("user DEK is required")
	}
	if s.keyStore == nil {
		return errors.New("keystore not initialized")
	}

	masterCiphertext, err := s.keyStore.EncryptSecret(secret)
	if err != nil {
		return err
	}

	encryptedDEK, err := crypto.AES256GCMEncrypt(userDEK, masterCiphertext)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(
		`INSERT INTO user_2fa (user_id, method, secret, enabled)
		 VALUES (?, ?, ?, 1)
		 ON CONFLICT(user_id) DO UPDATE SET
		   method = excluded.method,
		   secret = excluded.secret,
		   enabled = 1,
		   updated_at = CURRENT_TIMESTAMP`,
		userID, method, string(encryptedDEK),
	)
	return err
}

// GetUser2FA reports whether a user has an enabled 2FA method and which one.
func (s *Store) GetUser2FA(userID string) (method string, enabled bool, err error) {
	var m string
	var e int
	err = s.db.QueryRow(
		`SELECT method, enabled FROM user_2fa WHERE user_id = ?`, userID,
	).Scan(&m, &e)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return m, e == 1, nil
}

// GetUser2FASecret unwraps and returns the plaintext 2FA secret for a user.
// It requires the user-derived DEK; there is no master-only fallback.
func (s *Store) GetUser2FASecret(userID string, userDEK []byte) (string, error) {
	if userDEK == nil {
		return "", errors.New("user DEK is required")
	}
	if s.keyStore == nil {
		return "", errors.New("keystore not initialized")
	}

	var encrypted string
	err := s.db.QueryRow(
		`SELECT secret FROM user_2fa WHERE user_id = ?`, userID,
	).Scan(&encrypted)
	if err == sql.ErrNoRows {
		return "", ErrNo2FA
	}
	if err != nil {
		return "", err
	}
	if encrypted == "" {
		return "", ErrNo2FA
	}

	masterCiphertext, err := crypto.AES256GCMDecrypt(userDEK, []byte(encrypted))
	if err != nil {
		return "", fmt.Errorf("user-DEK unwrap failed: %w", err)
	}
	defer crypto.Zeroize(masterCiphertext)

	plaintext, err := s.keyStore.DecryptSecret(masterCiphertext)
	if err != nil {
		return "", fmt.Errorf("master-key decrypt failed: %w", err)
	}
	defer crypto.Zeroize(plaintext)

	return string(plaintext), nil
}

// DeleteUser2FA removes a user's 2FA configuration.
func (s *Store) DeleteUser2FA(userID string) error {
	_, err := s.db.Exec(`DELETE FROM user_2fa WHERE user_id = ?`, userID)
	return err
}

// RevokeWebSessions deletes all web sessions for a user, so a lost or
// compromised device cannot ride an existing session.
func (s *Store) RevokeWebSessions(userID string) error {
	_, err := s.db.Exec(`DELETE FROM web_sessions WHERE user_id = ?`, userID)
	return err
}
