package crypto

type KeyStore struct {
	encryptor *EnvelopeEncryptor
}

func NewKeyStore(provider KEKProvider) *KeyStore {
	return &KeyStore{
		encryptor: NewEnvelopeEncryptor(provider),
	}
}

func (ks *KeyStore) EncryptSecret(plaintext []byte) (encrypted []byte, err error) {
	return ks.encryptor.Encrypt(plaintext)
}

func (ks *KeyStore) DecryptSecret(encrypted []byte) (plaintext []byte, err error) {
	return ks.encryptor.DecryptNoClose(encrypted)
}

func (ks *KeyStore) DecryptSecretForUser(encrypted []byte, userID string) (plaintext []byte, err error) {
	return ks.encryptor.Decrypt(encrypted)
}

func (ks *KeyStore) SetEncryptor(encryptor *EnvelopeEncryptor) {
	ks.encryptor = encryptor
}
