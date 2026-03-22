package crypto

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const (
	testKeyHex    = "test-master-key-32-bytes-long!"
	testPlaintext = "Hello, World!"
)

var (
	testKeyBytes = []byte(testKeyHex)
)

type mockKEKProvider struct {
	key      []byte
	keyID    string
	closeErr error
}

func (m *mockKEKProvider) GetKey(ctx context.Context) ([]byte, error) {
	if m.key == nil {
		return nil, errors.New("key not set")
	}
	return m.key, nil
}

func (m *mockKEKProvider) KeyID() string {
	return m.keyID
}

func (m *mockKEKProvider) Close() error {
	return m.closeErr
}

func newMockKEKProvider(key []byte, keyID string) *mockKEKProvider {
	return &mockKEKProvider{
		key:   key,
		keyID: keyID,
	}
}

func TestKEKProviderInterface(t *testing.T) {
	var provider KEKProvider = newMockKEKProvider(testKeyBytes, "test-key-id")
	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestEnvVarProvider_GetKey_FromEnv(t *testing.T) {
	testKey := "0123456789abcdef0123456789abcdef"
	os.Setenv("ENCRYPTION_KEY", testKey)
	defer os.Unsetenv("ENCRYPTION_KEY")

	provider := NewEnvVarProvider("", "")

	key, err := provider.GetKey(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedKey, err := hex.DecodeString(testKey)
	if err != nil {
		t.Fatalf("failed to decode expected key: %v", err)
	}

	if !bytes.Equal(key, expectedKey) {
		t.Errorf("expected key %x, got %x", expectedKey, key)
	}
}

func TestEnvVarProvider_GetKey_FromEnv_Base64(t *testing.T) {
	rawKey := make([]byte, 32)
	for i := range rawKey {
		rawKey[i] = byte(i)
	}
	encodedKey := base64.StdEncoding.EncodeToString(rawKey)

	os.Setenv("ENCRYPTION_KEY", encodedKey)
	defer os.Unsetenv("ENCRYPTION_KEY")

	provider := NewEnvVarProvider("", "")

	key, err := provider.GetKey(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !bytes.Equal(key, rawKey) {
		t.Errorf("expected key %x, got %x", rawKey, key)
	}
}

func TestEnvVarProvider_GetKey_FromFile(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "encryption.key")

	testKey := "fedcba9876543210fedcba9876543210"
	if err := os.WriteFile(keyFile, []byte(testKey), 0600); err != nil {
		t.Fatalf("failed to write key file: %v", err)
	}

	os.Setenv("ENCRYPTION_KEY_FILE", keyFile)
	defer os.Unsetenv("ENCRYPTION_KEY_FILE")
	os.Unsetenv("ENCRYPTION_KEY")

	provider := NewEnvVarProvider("", "")

	key, err := provider.GetKey(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedKey, err := hex.DecodeString(testKey)
	if err != nil {
		t.Fatalf("failed to decode expected key: %v", err)
	}

	if !bytes.Equal(key, expectedKey) {
		t.Errorf("expected key %x, got %x", expectedKey, key)
	}
}

func TestEnvVarProvider_GetKey_FromFile_Base64(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "encryption.key")

	rawKey := make([]byte, 32)
	for i := range rawKey {
		rawKey[i] = byte(31 - i)
	}
	encodedKey := base64.StdEncoding.EncodeToString(rawKey)

	if err := os.WriteFile(keyFile, []byte(encodedKey), 0600); err != nil {
		t.Fatalf("failed to write key file: %v", err)
	}

	os.Setenv("ENCRYPTION_KEY_FILE", keyFile)
	defer os.Unsetenv("ENCRYPTION_KEY_FILE")
	os.Unsetenv("ENCRYPTION_KEY")

	provider := NewEnvVarProvider("", "")

	key, err := provider.GetKey(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !bytes.Equal(key, rawKey) {
		t.Errorf("expected key %x, got %x", rawKey, key)
	}
}

func TestEnvVarProvider_GetKey_NotSet(t *testing.T) {
	os.Unsetenv("ENCRYPTION_KEY")
	os.Unsetenv("ENCRYPTION_KEY_FILE")

	provider := NewEnvVarProvider("", "")

	_, err := provider.GetKey(context.Background())
	if err == nil {
		t.Error("expected error when no key is set")
	}
}

func TestEnvVarProvider_GetKey_FileNotFound(t *testing.T) {
	os.Setenv("ENCRYPTION_KEY_FILE", "/nonexistent/path/to/key")
	defer os.Unsetenv("ENCRYPTION_KEY_FILE")
	os.Unsetenv("ENCRYPTION_KEY")

	provider := NewEnvVarProvider("", "")

	_, err := provider.GetKey(context.Background())
	if err == nil {
		t.Error("expected error when key file not found")
	}
}

func TestEnvVarProvider_GetKey_InvalidHex(t *testing.T) {
	os.Setenv("ENCRYPTION_KEY", "not-a-valid-hex-key")
	defer os.Unsetenv("ENCRYPTION_KEY")

	provider := NewEnvVarProvider("", "")

	_, err := provider.GetKey(context.Background())
	if err == nil {
		t.Error("expected error for invalid hex key")
	}
}

func TestEnvVarProvider_GetKey_TooShort(t *testing.T) {
	os.Setenv("ENCRYPTION_KEY", "0123456789abcdef")
	defer os.Unsetenv("ENCRYPTION_KEY")

	provider := NewEnvVarProvider("", "")

	_, err := provider.GetKey(context.Background())
	if err == nil {
		t.Error("expected error for key too short")
	}
}

func TestEnvVarProvider_GetKey_TooLong(t *testing.T) {
	os.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123")
	defer os.Unsetenv("ENCRYPTION_KEY")

	provider := NewEnvVarProvider("", "")

	_, err := provider.GetKey(context.Background())
	if err == nil {
		t.Error("expected error for key too long")
	}
}

func TestEnvVarProvider_KeyID(t *testing.T) {
	os.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	defer os.Unsetenv("ENCRYPTION_KEY")

	provider := NewEnvVarProvider("", "")

	keyID := provider.KeyID()
	if keyID == "" {
		t.Error("expected non-empty key ID")
	}
}

func TestEnvVarProvider_Close(t *testing.T) {
	provider := NewEnvVarProvider("", "")

	err := provider.Close()
	if err != nil {
		t.Errorf("unexpected error on close: %v", err)
	}
}

func TestK8sSecretProvider_GetKey(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	keyFile := filepath.Join(dir, "encryption.key")

	testKey := "abcdef0123456789abcdef0123456789"
	if err := os.WriteFile(tokenFile, []byte("test-token"), 0600); err != nil {
		t.Fatalf("failed to write token file: %v", err)
	}
	if err := os.WriteFile(keyFile, []byte(testKey), 0600); err != nil {
		t.Fatalf("failed to write key file: %v", err)
	}

	provider, err := NewK8sSecretProvider(dir, "")
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	key, err := provider.GetKey(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedKey, err := hex.DecodeString(testKey)
	if err != nil {
		t.Fatalf("failed to decode expected key: %v", err)
	}

	if !bytes.Equal(key, expectedKey) {
		t.Errorf("expected key %x, got %x", expectedKey, key)
	}
}

func TestK8sSecretProvider_GetKey_FileNotFound(t *testing.T) {
	dir := t.TempDir()

	_, err := NewK8sSecretProvider(dir, "")
	if err != nil {
		t.Fatalf("expected no error creating provider, got: %v", err)
	}

	provider, _ := NewK8sSecretProvider(dir, "")
	_, err = provider.GetKey(context.Background())
	if err == nil {
		t.Error("expected error when key file not found")
	}
}

func TestK8sSecretProvider_KeyID(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	keyFile := filepath.Join(dir, "encryption.key")

	if err := os.WriteFile(tokenFile, []byte("test-token"), 0600); err != nil {
		t.Fatalf("failed to write token file: %v", err)
	}
	if err := os.WriteFile(keyFile, []byte("0123456789abcdef0123456789abcdef"), 0600); err != nil {
		t.Fatalf("failed to write key file: %v", err)
	}

	provider, err := NewK8sSecretProvider(dir, "")
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	keyID := provider.KeyID()
	if keyID == "" {
		t.Error("expected non-empty key ID")
	}
}

func TestK8sSecretProvider_Close(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	keyFile := filepath.Join(dir, "encryption.key")

	if err := os.WriteFile(tokenFile, []byte("test-token"), 0600); err != nil {
		t.Fatalf("failed to write token file: %v", err)
	}
	if err := os.WriteFile(keyFile, []byte("0123456789abcdef0123456789abcdef"), 0600); err != nil {
		t.Fatalf("failed to write key file: %v", err)
	}

	provider, err := NewK8sSecretProvider(dir, "")
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	err = provider.Close()
	if err != nil {
		t.Errorf("unexpected error on close: %v", err)
	}
}

func TestAES256GCM_Encrypt_KnownVector(t *testing.T) {
	key := make([]byte, 32)
	copy(key, testKeyHex)

	plaintext := []byte(testPlaintext)

	ciphertext, err := AES256GCMEncrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	if len(ciphertext) < 28 {
		t.Errorf("ciphertext too short: %d bytes", len(ciphertext))
	}
}

func TestAES256GCM_Decrypt_KnownVector(t *testing.T) {
	key := make([]byte, 32)
	copy(key, testKeyHex)

	plaintext := []byte(testPlaintext)

	ciphertext, err := AES256GCMEncrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	decrypted, err := AES256GCMDecrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("expected %s, got %s", plaintext, decrypted)
	}
}

func TestAES256GCM_EncryptDecrypt_RoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}

	testCases := []struct {
		name      string
		plaintext []byte
	}{
		{"empty", []byte{}},
		{"short", []byte("hello")},
		{"normal", []byte(testPlaintext)},
		{"long", bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz"), 100)},
		{"binary", []byte{0x00, 0xFF, 0x01, 0x02, 0xFE, 0xFD}},
		{"special", []byte("!@#$%^&*()_+-=[]{}|;':\",./<>?")},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ciphertext, err := AES256GCMEncrypt(key, tc.plaintext)
			if err != nil {
				t.Fatalf("encrypt failed: %v", err)
			}

			decrypted, err := AES256GCMDecrypt(key, ciphertext)
			if err != nil {
				t.Fatalf("decrypt failed: %v", err)
			}

			if !bytes.Equal(decrypted, tc.plaintext) {
				t.Errorf("round trip failed: expected %x, got %x", tc.plaintext, decrypted)
			}
		})
	}
}

func TestAES256GCM_InvalidKey_TooShort(t *testing.T) {
	key := make([]byte, 16)
	plaintext := []byte(testPlaintext)

	_, err := AES256GCMEncrypt(key, plaintext)
	if err == nil {
		t.Error("expected error for key too short")
	}
}

func TestAES256GCM_InvalidKey_TooLong(t *testing.T) {
	key := make([]byte, 48)
	plaintext := []byte(testPlaintext)

	_, err := AES256GCMEncrypt(key, plaintext)
	if err == nil {
		t.Error("expected error for key too long")
	}
}

func TestAES256GCM_InvalidCiphertext(t *testing.T) {
	key := make([]byte, 32)
	copy(key, testKeyHex)

	_, err := AES256GCMDecrypt(key, []byte("too short"))
	if err == nil {
		t.Error("expected error for ciphertext too short")
	}
}

func TestAES256GCM_Decrypt_WrongKey(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	for i := range key1 {
		key1[i] = byte(i)
		key2[i] = byte(31 - i)
	}

	plaintext := []byte(testPlaintext)

	ciphertext, err := AES256GCMEncrypt(key1, plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	_, err = AES256GCMDecrypt(key2, ciphertext)
	if err == nil {
		t.Error("expected error when decrypting with wrong key")
	}
}

func TestAES256GCM_DifferentCiphertexts_SamePlaintext(t *testing.T) {
	key := make([]byte, 32)
	copy(key, testKeyHex)

	plaintext := []byte(testPlaintext)

	ct1, err := AES256GCMEncrypt(key, plaintext)
	if err != nil {
		t.Fatalf("first encrypt failed: %v", err)
	}

	ct2, err := AES256GCMEncrypt(key, plaintext)
	if err != nil {
		t.Fatalf("second encrypt failed: %v", err)
	}

	if bytes.Equal(ct1, ct2) {
		t.Error("ciphertexts should be different due to random nonce")
	}

	decrypted1, err := AES256GCMDecrypt(key, ct1)
	if err != nil {
		t.Fatalf("decrypt 1 failed: %v", err)
	}

	decrypted2, err := AES256GCMDecrypt(key, ct2)
	if err != nil {
		t.Fatalf("decrypt 2 failed: %v", err)
	}

	if !bytes.Equal(decrypted1, decrypted2) {
		t.Error("both should decrypt to same plaintext")
	}
}

func TestAES256GCM_NonceReuse(t *testing.T) {
	key := make([]byte, 32)
	copy(key, testKeyHex)

	plaintext := []byte(testPlaintext)

	ciphertext, err := AES256GCMEncrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	decrypted1, err := AES256GCMDecrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("first decrypt failed: %v", err)
	}

	decrypted2, err := AES256GCMDecrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("second decrypt failed: %v", err)
	}

	if !bytes.Equal(decrypted1, decrypted2) {
		t.Error("same ciphertext should decrypt to same plaintext")
	}
}

func TestAES256GCM_TamperedCiphertext(t *testing.T) {
	key := make([]byte, 32)
	copy(key, testKeyHex)

	plaintext := []byte(testPlaintext)

	ciphertext, err := AES256GCMEncrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	tampered := make([]byte, len(ciphertext))
	copy(tampered, ciphertext)
	tampered[len(tampered)-1] ^= 0xFF

	_, err = AES256GCMDecrypt(key, tampered)
	if err == nil {
		t.Error("expected error for tampered ciphertext")
	}
}

func TestEnvelopeEncryptor_Interface(t *testing.T) {
	provider := newMockKEKProvider(testKeyBytes, "test-key")
	var _ KEKProvider = provider
}

func TestNewEnvelopeEncryptor(t *testing.T) {
	provider := newMockKEKProvider(testKeyBytes, "test-key")
	enc := NewEnvelopeEncryptor(provider)
	if enc == nil {
		t.Fatal("expected non-nil encryptor")
	}
}

func TestEnvelopeEncryptor_Encrypt_Decrypt_RoundTrip(t *testing.T) {
	provider := newMockKEKProvider(testKeyBytes, "test-key")
	enc := NewEnvelopeEncryptor(provider)

	plaintext := []byte(testPlaintext)

	ciphertext, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	if len(ciphertext) < 40 {
		t.Errorf("ciphertext too short: %d bytes", len(ciphertext))
	}

	decrypted, err := enc.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("expected %s, got %s", plaintext, decrypted)
	}
}

func TestEnvelopeEncryptor_DifferentCiphertexts_SamePlaintext(t *testing.T) {
	provider := newMockKEKProvider(testKeyBytes, "test-key")
	enc := NewEnvelopeEncryptor(provider)

	plaintext := []byte(testPlaintext)

	ct1, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("first encrypt failed: %v", err)
	}

	ct2, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("second encrypt failed: %v", err)
	}

	if bytes.Equal(ct1, ct2) {
		t.Error("ciphertexts should be different due to random DEK and nonce")
	}

	decrypted1, err := enc.Decrypt(ct1)
	if err != nil {
		t.Fatalf("decrypt 1 failed: %v", err)
	}

	decrypted2, err := enc.Decrypt(ct2)
	if err != nil {
		t.Fatalf("decrypt 2 failed: %v", err)
	}

	if !bytes.Equal(decrypted1, decrypted2) {
		t.Error("both should decrypt to same plaintext")
	}
}

func TestEnvelopeEncryptor_Decrypt_WrongKey(t *testing.T) {
	provider1 := newMockKEKProvider(testKeyBytes, "key-1")
	provider2 := newMockKEKProvider([]byte("wrong-key-here-32-bytes-long!!"), "key-2")

	enc1 := NewEnvelopeEncryptor(provider1)
	enc2 := NewEnvelopeEncryptor(provider2)

	plaintext := []byte(testPlaintext)

	ciphertext, err := enc1.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	_, err = enc2.Decrypt(ciphertext)
	if err == nil {
		t.Error("expected error when decrypting with wrong key")
	}
}

func TestEnvelopeEncryptor_Decrypt_TamperedCiphertext(t *testing.T) {
	provider := newMockKEKProvider(testKeyBytes, "test-key")
	enc := NewEnvelopeEncryptor(provider)

	plaintext := []byte(testPlaintext)

	ciphertext, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	tampered := make([]byte, len(ciphertext))
	copy(tampered, ciphertext)
	tampered[len(tampered)-1] ^= 0xFF

	_, err = enc.Decrypt(tampered)
	if err == nil {
		t.Error("expected error for tampered ciphertext")
	}
}

func TestEnvelopeEncryptor_Decrypt_TruncatedCiphertext(t *testing.T) {
	provider := newMockKEKProvider(testKeyBytes, "test-key")
	enc := NewEnvelopeEncryptor(provider)

	plaintext := []byte(testPlaintext)

	ciphertext, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	truncated := ciphertext[:len(ciphertext)/2]

	_, err = enc.Decrypt(truncated)
	if err == nil {
		t.Error("expected error for truncated ciphertext")
	}
}

func TestEnvelopeEncryptor_Encrypt_EmptyPlaintext(t *testing.T) {
	provider := newMockKEKProvider(testKeyBytes, "test-key")
	enc := NewEnvelopeEncryptor(provider)

	ciphertext, err := enc.Encrypt([]byte{})
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	if len(ciphertext) == 0 {
		t.Error("ciphertext should not be empty")
	}

	decrypted, err := enc.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}

	if len(decrypted) != 0 {
		t.Errorf("expected empty plaintext, got %d bytes", len(decrypted))
	}
}

func TestEnvelopeEncryptor_Encrypt_LargePlaintext(t *testing.T) {
	provider := newMockKEKProvider(testKeyBytes, "test-key")
	enc := NewEnvelopeEncryptor(provider)

	plaintext := bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz"), 10000)

	ciphertext, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	decrypted, err := enc.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Error("large plaintext round trip failed")
	}
}

func TestEnvelopeEncryptor_GoldenVectors(t *testing.T) {
	goldenVectors := []struct {
		name      string
		keyHex    string
		plaintext string
	}{
		{
			name:      "simple",
			keyHex:    "0123456789abcdef0123456789abcdef",
			plaintext: "Hello",
		},
		{
			name:      "normal",
			keyHex:    "fedcba9876543210fedcba9876543210",
			plaintext: testPlaintext,
		},
		{
			name:      "binary",
			keyHex:    "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
			plaintext: "\x00\x01\x02\x03\x04\x05",
		},
	}

	for _, gv := range goldenVectors {
		t.Run(gv.name, func(t *testing.T) {
			keyBytes, err := hex.DecodeString(gv.keyHex)
			if err != nil {
				t.Fatalf("failed to decode key: %v", err)
			}

			provider := newMockKEKProvider(keyBytes, "golden-"+gv.name)
			enc := NewEnvelopeEncryptor(provider)

			ciphertext, err := enc.Encrypt([]byte(gv.plaintext))
			if err != nil {
				t.Fatalf("encrypt failed: %v", err)
			}

			decrypted, err := enc.Decrypt(ciphertext)
			if err != nil {
				t.Fatalf("decrypt failed: %v", err)
			}

			if string(decrypted) != gv.plaintext {
				t.Errorf("expected %q, got %q", gv.plaintext, decrypted)
			}

			t.Logf("key=%s plaintext=%q ciphertext=%x", gv.keyHex, gv.plaintext, ciphertext)
		})
	}
}

func TestEnvelopeEncryptor_KeyIDFromProvider(t *testing.T) {
	expectedKeyID := "my-unique-key-id-123"
	provider := newMockKEKProvider(testKeyBytes, expectedKeyID)
	enc := NewEnvelopeEncryptor(provider)

	plaintext := []byte(testPlaintext)

	ciphertext, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	if len(ciphertext) == 0 {
		t.Error("ciphertext should not be empty")
	}

	if provider.KeyID() != expectedKeyID {
		t.Errorf("expected key ID %s, got %s", expectedKeyID, provider.KeyID())
	}
}

func TestEnvelopeEncryptor_ProviderGetKeyError(t *testing.T) {
	provider := &errorKEKProvider{getKeyErr: errors.New("key not available")}
	enc := NewEnvelopeEncryptor(provider)

	_, err := enc.Encrypt([]byte(testPlaintext))
	if err == nil {
		t.Error("expected error when provider fails to get key")
	}
}

func TestEnvelopeEncryptor_ProviderCloseError(t *testing.T) {
	provider := newMockKEKProvider(testKeyBytes, "test-key")
	provider.closeErr = errors.New("close failed")
	enc := NewEnvelopeEncryptor(provider)

	ciphertext, err := enc.Encrypt([]byte(testPlaintext))
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	_, err = enc.Decrypt(ciphertext)
	if err == nil {
		t.Error("expected error when provider close fails")
	}
}

type errorKEKProvider struct {
	getKeyErr error
	keyID     string
	closeErr  error
}

func (e *errorKEKProvider) GetKey(ctx context.Context) ([]byte, error) {
	return nil, e.getKeyErr
}

func (e *errorKEKProvider) KeyID() string {
	return e.keyID
}

func (e *errorKEKProvider) Close() error {
	return e.closeErr
}

func TestCiphertextFormat(t *testing.T) {
	provider := newMockKEKProvider(testKeyBytes, "format-test")
	enc := NewEnvelopeEncryptor(provider)

	plaintext := []byte("test data")

	ciphertext, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	nonceSize := 12
	tagSize := 16
	dekSize := 32
	minLength := nonceSize + tagSize + dekSize + tagSize + len(plaintext)

	if len(ciphertext) < minLength {
		t.Errorf("ciphertext too short: got %d, expected at least %d", len(ciphertext), minLength)
	}

	decrypted, err := enc.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("decryption produced wrong plaintext")
	}
}

func TestMultipleEncryptors_SameKey_DifferentCiphertexts(t *testing.T) {
	provider := newMockKEKProvider(testKeyBytes, "shared-key")

	enc1 := NewEnvelopeEncryptor(provider)
	enc2 := NewEnvelopeEncryptor(provider)

	plaintext := []byte(testPlaintext)

	ct1, err := enc1.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("enc1 encrypt failed: %v", err)
	}

	ct2, err := enc2.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("enc2 encrypt failed: %v", err)
	}

	decrypted1, err := enc1.Decrypt(ct1)
	if err != nil {
		t.Fatalf("enc1 decrypt failed: %v", err)
	}

	decrypted2, err := enc2.Decrypt(ct2)
	if err != nil {
		t.Fatalf("enc2 decrypt failed: %v", err)
	}

	if !bytes.Equal(decrypted1, plaintext) {
		t.Error("enc1 should decrypt its own ciphertext")
	}

	if !bytes.Equal(decrypted2, plaintext) {
		t.Error("enc2 should decrypt its own ciphertext")
	}

	if bytes.Equal(ct1, ct2) {
		t.Error("different encryptors should produce different ciphertexts")
	}
}

func TestEncryptDecrypt_Concurrent(t *testing.T) {
	provider := newMockKEKProvider(testKeyBytes, "concurrent-test")
	enc := NewEnvelopeEncryptor(provider)

	numOps := 100
	plaintext := []byte(testPlaintext)

	errCh := make(chan error, numOps*2)

	for i := 0; i < numOps; i++ {
		go func() {
			ct, err := enc.Encrypt(plaintext)
			if err != nil {
				errCh <- err
				return
			}
			_, err = enc.Decrypt(ct)
			errCh <- err
		}()
	}

	for i := 0; i < numOps*2; i++ {
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("operation failed: %v", err)
			}
		}
	}
}
