package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/mcp-bridge/mcp-bridge/internal/crypto"
	"github.com/mcp-bridge/mcp-bridge/store"
	"golang.org/x/term"

	_ "github.com/mattn/go-sqlite3"
)

type directProvider struct {
	key []byte
}

func (p *directProvider) GetKey(ctx context.Context) ([]byte, error) {
	key := make([]byte, len(p.key))
	copy(key, p.key)
	return key, nil
}

func (p *directProvider) KeyID() string {
	return crypto.KeyID(p.key)
}

func (p *directProvider) Close() error {
	for i := range p.key {
		p.key[i] = 0
	}
	return nil
}

func parseEncryptionKey(keyStr string) ([]byte, error) {
	keyStr = strings.TrimSpace(keyStr)

	if isHex(keyStr) {
		if len(keyStr) == 32 || len(keyStr) == 64 {
			return hex.DecodeString(keyStr)
		}
		return nil, fmt.Errorf("invalid hex key length: expected 32 or 64 characters, got %d", len(keyStr))
	}

	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(keyStr)))
	n, err := base64.StdEncoding.Decode(decoded, []byte(keyStr))
	if err == nil && n >= 16 {
		return decoded[:n], nil
	}

	if len(keyStr) >= 16 {
		return []byte(keyStr), nil
	}

	return nil, fmt.Errorf("key must be at least 16 bytes")
}

func isHex(s string) bool {
	if len(s) < 2 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

type extractedSecret struct {
	Backend  string `json:"backend"`
	EnvKey   string `json:"env_key"`
	Value    string `json:"value"`
	Method   string `json:"method"`
}

func main() {
	dbPath := flag.String("db-path", "mcp-bridge.db", "Path to SQLite database")
	encryptionKey := flag.String("encryption-key", "", "Master encryption key (or ENCRYPTION_KEY env var)")
	email := flag.String("email", "", "User email to extract secrets for")
	password := flag.String("password", "", "User password for user-derived decryption")
	jsonOutput := flag.Bool("json", false, "Output as JSON instead of formatted text")
	flag.Parse()

	if *email == "" {
		fmt.Fprintln(os.Stderr, "Error: --email is required")
		flag.Usage()
		os.Exit(1)
	}
	if *password == "" {
		fmt.Fprint(os.Stderr, "Password: ")
		p, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to read password: %v\n", err)
			os.Exit(1)
		}
		if len(p) == 0 {
			fmt.Fprintln(os.Stderr, "Error: --password is required (or provide it interactively)")
			flag.Usage()
			os.Exit(1)
		}
		s := string(p)
		password = &s
	}

	key := *encryptionKey
	if key == "" {
		key = os.Getenv("ENCRYPTION_KEY")
	}

	keyBytes, err := parseEncryptionKey(key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to parse encryption key: %v\n", err)
		os.Exit(1)
	}

	provider := &directProvider{key: keyBytes}
	s, err := store.NewWithProvider(*dbPath, provider)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open store: %v\n", err)
		os.Exit(1)
	}
	defer s.Close()

	userID, passwordSalt, err := findUser(s, *email)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error finding user %q: %v\n", *email, err)
		os.Exit(1)
	}

	var userDEK []byte
	if passwordSalt != "" {
		dek, err := crypto.DeriveUserDEK(*password, passwordSalt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to derive user key: %v\n", err)
		} else {
			userDEK = dek
		}
	}
	if userDEK == nil {
		fmt.Fprintln(os.Stderr, "Warning: user-derived decryption not available, falling back to master key only")
	}

	tokens, err := queryTokens(s, userID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error querying tokens: %v\n", err)
		os.Exit(1)
	}

	var secrets []extractedSecret
	for _, t := range tokens {
		secret := extractToken(s, t, userDEK)
		secrets = append(secrets, secret)
	}

	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(secrets)
	} else {
		for _, sec := range secrets {
			method := ""
			if sec.Method != "" {
				method = fmt.Sprintf(" [%s]", sec.Method)
			}
			fmt.Printf("%s/%s%s:\n  %s\n\n", sec.Backend, sec.EnvKey, method, sec.Value)
		}
	}
}

type tokenRow struct {
	UserID         string
	BackendID      string
	EnvKey         string
	Value          string
	Encrypted      string
	EncryptedDEK   string
	EncryptionType string
}

func findUser(s *store.Store, email string) (string, string, error) {
	db := s.DB()
	var userID, passwordSalt string
	err := db.QueryRow(
		`SELECT id, COALESCE(password_salt, '') FROM users WHERE email = ?`,
		email,
	).Scan(&userID, &passwordSalt)
	if err == sql.ErrNoRows {
		return "", "", fmt.Errorf("user not found")
	}
	if err != nil {
		return "", "", err
	}
	return userID, passwordSalt, nil
}

func queryTokens(s *store.Store, userID string) ([]tokenRow, error) {
	db := s.DB()
	rows, err := db.Query(
		`SELECT user_id, backend_id, env_key, value,
		        COALESCE(encrypted_value, '') AS encrypted_value,
		        COALESCE(encrypted_dek, '') AS encrypted_dek,
		        COALESCE(encryption_type, 'legacy') AS encryption_type
		 FROM user_tokens WHERE user_id = ?`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []tokenRow
	for rows.Next() {
		var t tokenRow
		if err := rows.Scan(&t.UserID, &t.BackendID, &t.EnvKey, &t.Value,
			&t.Encrypted, &t.EncryptedDEK, &t.EncryptionType); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

func extractToken(s *store.Store, t tokenRow, userDEK []byte) extractedSecret {
	sec := extractedSecret{
		Backend: t.BackendID,
		EnvKey:  t.EnvKey,
	}

	// Try user-derived decryption first
	if t.EncryptionType == "user" && t.EncryptedDEK != "" && userDEK != nil {
		plaintext, err := crypto.AES256GCMDecrypt(userDEK, []byte(t.EncryptedDEK))
		if err == nil {
			plaintext2, err := crypto.AES256GCMDecrypt(plaintext, []byte(t.Encrypted))
			if err == nil {
				sec.Value = string(plaintext2)
				sec.Method = "user-derived"
				return sec
			}
		}
	}

	// Try master-key envelope decryption
	if t.Encrypted != "" && s.KeyStore() != nil {
		plaintext, err := s.KeyStore().DecryptSecret([]byte(t.Encrypted))
		if err == nil {
			sec.Value = string(plaintext)
			if t.EncryptedDEK != "" {
				sec.Method = "master-key (user-derived failed)"
			} else {
				sec.Method = "master-key"
			}
			return sec
		}
	}

	// Fallback to plaintext value
	if t.Value != "" {
		sec.Value = t.Value
		sec.Method = "plaintext"
		return sec
	}

	sec.Value = fmt.Sprintf("<decryption failed: no decryptable data for %s/%s>", t.BackendID, t.EnvKey)
	sec.Method = "failed"
	return sec
}
