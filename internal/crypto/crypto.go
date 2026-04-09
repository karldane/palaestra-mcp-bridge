package crypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	keySize    = 32
	nonceSize  = 12
	gcmTagSize = 16
)

var (
	errKeyNotFound     = errors.New("key not found in secret directory")
	errKeyFileNotFound = errors.New("key file not found")
)

type KEKProvider interface {
	GetKey(ctx context.Context) ([]byte, error)
	KeyID() string
	Close() error
}

type EnvVarProvider struct {
	keyEnv     string
	keyFileEnv string
	cached     []byte
	mu         sync.RWMutex
}

func NewEnvVarProvider(keyEnv, keyFileEnv string) *EnvVarProvider {
	return &EnvVarProvider{
		keyEnv:     keyEnv,
		keyFileEnv: keyFileEnv,
	}
}

func (p *EnvVarProvider) GetKey(ctx context.Context) ([]byte, error) {
	p.mu.RLock()
	if p.cached != nil {
		key := make([]byte, len(p.cached))
		copy(key, p.cached)
		p.mu.RUnlock()
		return key, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cached != nil {
		key := make([]byte, len(p.cached))
		copy(key, p.cached)
		return key, nil
	}

	envVar := p.keyEnv
	if envVar == "" {
		envVar = "ENCRYPTION_KEY"
	}
	if env := os.Getenv(envVar); env != "" {
		key, err := parseKey(env)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", envVar, err)
		}
		p.cached = key
		return key, nil
	}

	fileEnv := p.keyFileEnv
	if fileEnv == "" {
		fileEnv = "ENCRYPTION_KEY_FILE"
	}
	if filePath := os.Getenv(fileEnv); filePath != "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("failed to read key file: %w", err)
		}
		trimmed := strings.TrimSpace(string(data))
		key, err := parseKey(trimmed)
		if err != nil {
			return nil, fmt.Errorf("failed to parse key from file: %w", err)
		}
		p.cached = key
		return key, nil
	}

	return nil, fmt.Errorf("neither %s nor %s is set", envVar, fileEnv)
}

func (p *EnvVarProvider) KeyID() string {
	key, err := p.GetKey(context.Background())
	if err != nil {
		return ""
	}
	return KeyID(key)
}

func (p *EnvVarProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cached != nil {
		for i := range p.cached {
			p.cached[i] = 0
		}
		p.cached = nil
	}
	return nil
}

type K8sSecretProvider struct {
	dir      string
	keyName  string
	cached   []byte
	mu       sync.RWMutex
	closeErr error
}

func NewK8sSecretProvider(dir, keyName string) (*K8sSecretProvider, error) {
	provider := &K8sSecretProvider{
		dir:     dir,
		keyName: keyName,
	}
	if keyName == "" {
		provider.keyName = "encryption.key"
	}
	if _, err := os.Stat(filepath.Join(dir, "token")); os.IsNotExist(err) {
		return provider, nil
	}
	return provider, nil
}

func (p *K8sSecretProvider) GetKey(ctx context.Context) ([]byte, error) {
	p.mu.RLock()
	if p.cached != nil {
		key := make([]byte, len(p.cached))
		copy(key, p.cached)
		p.mu.RUnlock()
		return key, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cached != nil {
		key := make([]byte, len(p.cached))
		copy(key, p.cached)
		return key, nil
	}

	keyFile := filepath.Join(p.dir, p.keyName)
	data, err := os.ReadFile(keyFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errKeyFileNotFound
		}
		return nil, fmt.Errorf("failed to read key file: %w", err)
	}

	trimmed := strings.TrimSpace(string(data))
	key, err := parseKey(trimmed)
	if err != nil {
		return nil, fmt.Errorf("failed to parse key: %w", err)
	}

	p.cached = key
	return key, nil
}

func (p *K8sSecretProvider) KeyID() string {
	key, err := p.GetKey(context.Background())
	if err != nil {
		return ""
	}
	return KeyID(key)
}

func (p *K8sSecretProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cached != nil {
		for i := range p.cached {
			p.cached[i] = 0
		}
		p.cached = nil
	}
	return p.closeErr
}

type EnvelopeEncryptor struct {
	provider KEKProvider
}

func NewEnvelopeEncryptor(provider KEKProvider) *EnvelopeEncryptor {
	return &EnvelopeEncryptor{provider: provider}
}

func (e *EnvelopeEncryptor) Encrypt(plaintext []byte) ([]byte, error) {
	kek, err := e.provider.GetKey(context.Background())
	if err != nil {
		return nil, err
	}
	kek = deriveKey(kek)
	defer zeroize(kek)

	dek := make([]byte, keySize)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, fmt.Errorf("failed to generate DEK: %w", err)
	}
	defer zeroize(dek)

	nonceDEK := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonceDEK); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	noncePT := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, noncePT); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	ciphertext, err := encryptAESGCM(dek, noncePT, plaintext)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt plaintext: %w", err)
	}

	encryptedDEK, err := encryptAESGCM(kek, nonceDEK, dek)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt DEK: %w", err)
	}

	result := make([]byte, 4+len(encryptedDEK)+nonceSize+nonceSize+len(ciphertext))
	binary.BigEndian.PutUint32(result[0:4], uint32(len(encryptedDEK)))
	offset := 4
	copy(result[offset:offset+len(encryptedDEK)], encryptedDEK)
	offset += len(encryptedDEK)
	copy(result[offset:offset+nonceSize], nonceDEK)
	offset += nonceSize
	copy(result[offset:offset+nonceSize], noncePT)
	offset += nonceSize
	copy(result[offset:], ciphertext)

	return result, nil
}

func (e *EnvelopeEncryptor) Decrypt(ciphertext []byte) ([]byte, error) {
	kek, err := e.provider.GetKey(context.Background())
	if err != nil {
		return nil, err
	}
	kek = deriveKey(kek)
	defer zeroize(kek)

	var plaintext []byte
	var decryptErr error

	if len(ciphertext) < 4+nonceSize+nonceSize+gcmTagSize {
		decryptErr = errors.New("ciphertext too short")
	} else {
		encryptedDEKLen := int(binary.BigEndian.Uint32(ciphertext[0:4]))
		offset := 4

		if len(ciphertext) < offset+encryptedDEKLen+nonceSize+nonceSize+gcmTagSize {
			decryptErr = errors.New("ciphertext too short for encrypted DEK")
		} else {
			encryptedDEK := ciphertext[offset : offset+encryptedDEKLen]
			offset += encryptedDEKLen

			nonceDEK := ciphertext[offset : offset+nonceSize]
			offset += nonceSize

			noncePT := ciphertext[offset : offset+nonceSize]
			offset += nonceSize

			encryptedPlaintext := ciphertext[offset:]

			dek, err := decryptAESGCM(kek, nonceDEK, encryptedDEK)
			if err != nil {
				decryptErr = fmt.Errorf("failed to decrypt DEK: %w", err)
			} else {
				defer zeroize(dek)
				plaintext, decryptErr = decryptAESGCM(dek, noncePT, encryptedPlaintext)
				if decryptErr != nil {
					decryptErr = fmt.Errorf("failed to decrypt plaintext: %w", decryptErr)
				}
			}
		}
	}

	if closeErr := e.provider.Close(); closeErr != nil {
		return nil, closeErr
	}

	if decryptErr != nil {
		return nil, decryptErr
	}

	return plaintext, nil
}

func (e *EnvelopeEncryptor) DecryptNoClose(ciphertext []byte) ([]byte, error) {
	kek, err := e.provider.GetKey(context.Background())
	if err != nil {
		return nil, err
	}
	kek = deriveKey(kek)
	defer zeroize(kek)

	var plaintext []byte
	var decryptErr error

	if len(ciphertext) < 4+nonceSize+nonceSize+gcmTagSize {
		decryptErr = errors.New("ciphertext too short")
	} else {
		encryptedDEKLen := int(binary.BigEndian.Uint32(ciphertext[0:4]))
		offset := 4

		if len(ciphertext) < offset+encryptedDEKLen+nonceSize+nonceSize+gcmTagSize {
			decryptErr = errors.New("ciphertext too short for encrypted DEK")
		} else {
			encryptedDEK := ciphertext[offset : offset+encryptedDEKLen]
			offset += encryptedDEKLen

			nonceDEK := ciphertext[offset : offset+nonceSize]
			offset += nonceSize

			noncePT := ciphertext[offset : offset+nonceSize]
			offset += nonceSize

			encryptedPlaintext := ciphertext[offset:]

			dek, err := decryptAESGCM(kek, nonceDEK, encryptedDEK)
			if err != nil {
				decryptErr = fmt.Errorf("failed to decrypt DEK: %w", err)
			} else {
				defer zeroize(dek)
				plaintext, decryptErr = decryptAESGCM(dek, noncePT, encryptedPlaintext)
				if decryptErr != nil {
					decryptErr = fmt.Errorf("failed to decrypt plaintext: %w", decryptErr)
				}
			}
		}
	}

	if decryptErr != nil {
		return nil, decryptErr
	}

	return plaintext, nil
}

func AES256GCMEncrypt(key, plaintext []byte) ([]byte, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("invalid key size: expected %d, got %d", keySize, len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func AES256GCMDecrypt(key, ciphertext []byte) ([]byte, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("invalid key size: expected %d, got %d", keySize, len(key))
	}

	if len(ciphertext) < gcmTagSize {
		return nil, errors.New("ciphertext too short")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short for nonce")
	}

	nonce, ct := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return gcm.Open(nil, nonce, ct, nil)
}

func encryptAESGCM(key, nonce, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("invalid nonce size: expected %d, got %d", gcm.NonceSize(), len(nonce))
	}

	return gcm.Seal(nil, nonce, plaintext, nil), nil
}

func decryptAESGCM(key, nonce, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("invalid nonce size: expected %d, got %d", gcm.NonceSize(), len(nonce))
	}

	return gcm.Open(nil, nonce, ciphertext, nil)
}

func GenerateRandomKey() ([]byte, error) {
	key := make([]byte, keySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("failed to generate random key: %w", err)
	}
	return key, nil
}

func KeyFromHex(s string) ([]byte, error) {
	return parseKey(s)
}

func KeyFromBase64(s string) ([]byte, error) {
	return parseKey(s)
}

func KeyID(key []byte) string {
	hash := sha256.Sum256(key)
	return hex.EncodeToString(hash[:8])
}

func parseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)

	if isHex(s) {
		if len(s) == 32 || len(s) == 64 {
			decoded, err := hex.DecodeString(s)
			if err == nil {
				return decoded, nil
			}
		}
		return nil, fmt.Errorf("invalid hex key length: expected 32 or 64 characters, got %d", len(s))
	}

	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(s)))
	n, err := base64.StdEncoding.Decode(decoded, []byte(s))
	if err == nil && n >= 16 {
		return decoded[:n], nil
	}

	if isLikelyRawBytes(s) && len(s) >= 16 {
		return []byte(s), nil
	}

	return nil, fmt.Errorf("invalid key format")
}

func isLikelyRawBytes(s string) bool {
	for _, c := range s {
		if c < 32 || c > 126 {
			return true
		}
		if c >= '0' && c <= '9' {
			return false
		}
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
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

func zeroize(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// Zeroize clears a byte slice in place (exported version).
func Zeroize(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func deriveKey(key []byte) []byte {
	result := make([]byte, keySize)
	if len(key) == keySize {
		copy(result, key)
		return result
	}
	derived, err := hkdf.Key(sha256.New, key, nil, "mcp-bridge-kek-v1", keySize)
	if err != nil {
		hash := sha256.Sum256(key)
		copy(result, hash[:])
		return result
	}
	return derived
}
