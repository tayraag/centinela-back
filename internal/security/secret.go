package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

type SecretProtector struct {
	key []byte
}

func NewSecretProtector(encodedKey string) (*SecretProtector, error) {
	key, err := base64.RawURLEncoding.DecodeString(encodedKey)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("TOTP_ENCRYPTION_KEY must be a base64url-encoded 32-byte key")
	}
	return &SecretProtector{key: key}, nil
}

func (protector *SecretProtector) Encrypt(value string) (string, error) {
	block, err := aes.NewCipher(protector.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(value), nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (protector *SecretProtector) Decrypt(value string) (string, error) {
	block, err := aes.NewCipher(protector.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	sealed, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(sealed) < gcm.NonceSize() {
		return "", fmt.Errorf("invalid protected secret")
	}
	nonce, ciphertext := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("invalid protected secret: %w", err)
	}
	return string(plaintext), nil
}