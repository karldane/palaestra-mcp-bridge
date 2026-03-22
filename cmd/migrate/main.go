package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/mcp-bridge/mcp-bridge/internal/crypto"
	"github.com/mcp-bridge/mcp-bridge/store"
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

	return nil, fmt.Errorf("invalid key format: must be at least 16 bytes")
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

func main() {
	dbPath := flag.String("db-path", "mcp-bridge.db", "Path to SQLite database")
	encryptionKey := flag.String("encryption-key", "", "Master encryption key (or set ENCRYPTION_KEY env var)")
	verify := flag.Bool("verify", false, "Verify all secrets can be decrypted")
	status := flag.Bool("status", false, "Show migration status")
	rollback := flag.Bool("rollback", false, "Rollback encrypted to plaintext")
	dryRun := flag.Bool("dry-run", false, "Show what would be migrated without making changes")
	flag.Parse()

	key := *encryptionKey
	if key == "" {
		key = os.Getenv("ENCRYPTION_KEY")
	}
	if key == "" {
		fmt.Fprintln(os.Stderr, "Error: encryption key required (--encryption-key or ENCRYPTION_KEY)")
		os.Exit(1)
	}

	keyBytes, err := parseEncryptionKey(key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to parse encryption key: %v\n", err)
		os.Exit(1)
	}

	provider := &directProvider{key: keyBytes}
	s, err := store.NewWithProvider(*dbPath, provider)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		os.Exit(1)
	}
	defer s.Close()

	if *status {
		showStatus(s)
	} else if *verify {
		verifyMigration(s)
	} else if *rollback {
		rollbackMigration(s, *dryRun)
	} else {
		runMigration(s, *dryRun)
	}
}

type migrationStatus struct {
	total         int
	plaintextOnly int
	encryptedOnly int
	bothPresent   int
	emptyValue    int
}

func showStatus(s *store.Store) {

	var total, plaintext, encrypted, empty int
	rows, err := s.DB().Query(`
		SELECT 
			COUNT(*) as total,
			COUNT(CASE WHEN (encrypted_value IS NULL OR encrypted_value = '') AND value != '' THEN 1 END) as plaintext,
			COUNT(CASE WHEN encrypted_value IS NOT NULL AND encrypted_value != '' THEN 1 END) as encrypted,
			COUNT(CASE WHEN value = '' AND (encrypted_value IS NULL OR encrypted_value = '') THEN 1 END) as empty
		FROM user_tokens
	`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error querying status: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	if rows.Next() {
		rows.Scan(&total, &plaintext, &encrypted, &empty)
	}

	fmt.Println("=== Migration Status ===")
	fmt.Printf("Total tokens:     %d\n", total)
	fmt.Printf("Plaintext only:   %d\n", plaintext)
	fmt.Printf("Encrypted only:   %d\n", encrypted)
	fmt.Printf("Empty values:     %d\n", empty)
	fmt.Println()

	if plaintext > 0 {
		fmt.Println("⚠️  Some tokens are not encrypted. Run migration to encrypt them.")
		fmt.Println("   Usage: migrate --db-path=<path> --encryption-key=<key>")
	} else if encrypted > 0 {
		fmt.Println("✅ All tokens are encrypted.")
		fmt.Println("   Use --verify to check integrity.")
		fmt.Println("   Use --rollback to restore plaintext (if needed).")
	} else {
		fmt.Println("ℹ️  No tokens to migrate.")
	}
}

func verifyMigration(s *store.Store) {
	ctx := context.Background()

	fmt.Println("=== Verifying Migration ===")
	fmt.Println("Checking all encrypted secrets can be decrypted...")
	fmt.Println()

	rows, err := s.DB().Query(`
		SELECT user_id, backend_id, env_key, value, COALESCE(encrypted_value, '') as encrypted_value
		FROM user_tokens
	`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error querying tokens: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	success := 0
	fail := 0
	var failures []struct {
		userID, backendID, envKey string
		err                       error
	}

	for rows.Next() {
		var userID, backendID, envKey, value, encrypted string
		if err := rows.Scan(&userID, &backendID, &envKey, &value, &encrypted); err != nil {
			fmt.Fprintf(os.Stderr, "Error scanning row: %v\n", err)
			continue
		}

		if encrypted == "" {
			continue
		}

		_, err := s.GetUserTokenDecrypted(userID, backendID, envKey)
		if err != nil {
			fail++
			failures = append(failures, struct {
				userID, backendID, envKey string
				err                       error
			}{userID, backendID, envKey, err})
			fmt.Printf("❌ FAIL: %s/%s/%s - %v\n", userID, backendID, envKey, err)
		} else {
			success++
			fmt.Printf("✅ OK: %s/%s/%s\n", userID, backendID, envKey)
		}
	}

	fmt.Println()
	fmt.Printf("Verification complete: %d passed, %d failed\n", success, fail)

	if fail > 0 {
		fmt.Println("\nFailed tokens:")
		for _, f := range failures {
			fmt.Printf("  - %s/%s/%s: %v\n", f.userID, f.backendID, f.envKey, f.err)
		}
		os.Exit(1)
	}

	_ = ctx
}

func rollbackMigration(s *store.Store, dryRun bool) {
	ctx := context.Background()

	fmt.Println("=== Rolling Back Migration ===")
	if dryRun {
		fmt.Println("DRY RUN - No changes will be made")
	}
	fmt.Println()

	rows, err := s.DB().Query(`
		SELECT user_id, backend_id, env_key, value, encrypted_value
		FROM user_tokens
		WHERE encrypted_value IS NOT NULL AND encrypted_value != ''
	`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error querying tokens: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	type rollbackItem struct {
		userID, backendID, envKey, value, encrypted string
	}
	var items []rollbackItem

	for rows.Next() {
		var item rollbackItem
		if err := rows.Scan(&item.userID, &item.backendID, &item.envKey, &item.value, &item.encrypted); err != nil {
			fmt.Fprintf(os.Stderr, "Error scanning row: %v\n", err)
			continue
		}
		items = append(items, item)
	}

	if len(items) == 0 {
		fmt.Println("No encrypted tokens to rollback.")
		return
	}

	fmt.Printf("Found %d encrypted tokens to rollback\n\n", len(items))

	success := 0
	fail := 0

	for _, item := range items {
		decrypted, err := s.GetUserTokenDecrypted(item.userID, item.backendID, item.envKey)
		if err != nil {
			fail++
			fmt.Printf("❌ FAIL: %s/%s/%s - %v\n", item.userID, item.backendID, item.envKey, err)
			continue
		}

		if dryRun {
			fmt.Printf("🔍 Would rollback: %s/%s/%s = [REDACTED]\n", item.userID, item.backendID, item.envKey)
		} else {
			_, err = s.DB().Exec(`
				UPDATE user_tokens SET value = ?, encrypted_value = NULL
				WHERE user_id = ? AND backend_id = ? AND env_key = ?
			`, decrypted, item.userID, item.backendID, item.envKey)
			if err != nil {
				fail++
				fmt.Printf("❌ FAIL: %s/%s/%s - %v\n", item.userID, item.backendID, item.envKey, err)
			} else {
				success++
				fmt.Printf("✅ Rolled back: %s/%s/%s\n", item.userID, item.backendID, item.envKey)
			}
		}
	}

	fmt.Println()
	if dryRun {
		fmt.Printf("Dry run complete: %d tokens would be rolled back, %d would fail\n", success, fail)
	} else {
		fmt.Printf("Rollback complete: %d succeeded, %d failed\n", success, fail)
	}

	if fail > 0 {
		os.Exit(1)
	}

	_ = ctx
}

func runMigration(s *store.Store, dryRun bool) {
	ctx := context.Background()

	fmt.Println("=== Migrating Secrets ===")
	if dryRun {
		fmt.Println("DRY RUN - No changes will be made")
	}
	fmt.Println()

	rows, err := s.DB().Query(`
		SELECT user_id, backend_id, env_key, value
		FROM user_tokens
		WHERE (encrypted_value IS NULL OR encrypted_value = '') AND value != ''
	`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error querying tokens: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	type migrateItem struct {
		userID, backendID, envKey, value string
	}
	var items []migrateItem

	for rows.Next() {
		var item migrateItem
		if err := rows.Scan(&item.userID, &item.backendID, &item.envKey, &item.value); err != nil {
			fmt.Fprintf(os.Stderr, "Error scanning row: %v\n", err)
			continue
		}
		items = append(items, item)
	}

	if len(items) == 0 {
		fmt.Println("No plaintext tokens to migrate.")
		return
	}

	fmt.Printf("Found %d plaintext tokens to encrypt\n\n", len(items))

	success := 0
	fail := 0

	for _, item := range items {
		if dryRun {
			fmt.Printf("🔍 Would encrypt: %s/%s/%s\n", item.userID, item.backendID, item.envKey)
			success++
			continue
		}

		err := s.SetUserTokenEncrypted(item.userID, item.backendID, item.envKey, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error preparing token: %v\n", err)
			fail++
			continue
		}

		encrypted, err := s.KeyStore().EncryptSecret([]byte(item.value))
		if err != nil {
			fmt.Printf("❌ FAIL: %s/%s/%s - %v\n", item.userID, item.backendID, item.envKey, err)
			fail++
			continue
		}

		_, err = s.DB().Exec(`
			UPDATE user_tokens SET encrypted_value = ?
			WHERE user_id = ? AND backend_id = ? AND env_key = ?
		`, string(encrypted), item.userID, item.backendID, item.envKey)
		if err != nil {
			fmt.Printf("❌ FAIL: %s/%s/%s - %v\n", item.userID, item.backendID, item.envKey, err)
			fail++
			continue
		}

		success++
		fmt.Printf("✅ Encrypted: %s/%s/%s\n", item.userID, item.backendID, item.envKey)
	}

	fmt.Println()
	if dryRun {
		fmt.Printf("Dry run complete: %d tokens would be migrated, %d would fail\n", success, fail)
	} else {
		fmt.Printf("Migration complete: %d succeeded, %d failed\n", success, fail)
	}

	if fail > 0 {
		os.Exit(1)
	}

	_ = ctx
}
