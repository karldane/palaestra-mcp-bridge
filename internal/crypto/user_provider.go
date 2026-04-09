package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"

	"golang.org/x/crypto/pbkdf2"
)

const (
	PBKDF2Iterations = 100000
	DerivedKeySize   = 32
	SaltSize         = 32
)

type UserPasswordProvider struct{}

func (p *UserPasswordProvider) DeriveUserDEK(password, saltBase64 string) ([]byte, error) {
	salt, err := base64.StdEncoding.DecodeString(saltBase64)
	if err != nil {
		return nil, err
	}

	return pbkdf2.Key([]byte(password), salt, PBKDF2Iterations, DerivedKeySize, sha256.New), nil
}

func GenerateSalt() (string, error) {
	salt := make([]byte, SaltSize)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(salt), nil
}

func DeriveUserDEK(password, saltBase64 string) ([]byte, error) {
	return (&UserPasswordProvider{}).DeriveUserDEK(password, saltBase64)
}
