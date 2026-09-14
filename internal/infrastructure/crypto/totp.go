package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// GenerarSecreto genera un nuevo secreto TOTP base32 aleatorio.
func GenerarSecreto(appName, emailUsuario string) (*otp.Key, error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      appName,
		AccountName: emailUsuario,
	})
	if err != nil {
		return nil, fmt.Errorf("error al generar secreto TOTP: %w", err)
	}
	return key, nil
}

// CifrarSecreto cifra un secreto TOTP en texto plano usando AES-256-GCM.
// key debe ser exactamente 32 bytes ASCII.
// Retorna el texto cifrado codificado en base64.
func CifrarSecreto(raw, key string) (string, error) {
	keyBytes := []byte(key)
	if len(keyBytes) != 32 {
		return "", fmt.Errorf("TOTP_ENCRYPTION_KEY debe ser exactamente 32 bytes, tiene %d", len(keyBytes))
	}

	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return "", fmt.Errorf("error al crear cipher AES: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("error al crear GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("error al generar nonce: %w", err)
	}

	// El ciphertext incluye el nonce como prefijo: nonce || ciphertext
	ciphertext := gcm.Seal(nonce, nonce, []byte(raw), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// DescifrarSecreto descifra un secreto TOTP cifrado con CifrarSecreto.
func DescifrarSecreto(enc, key string) (string, error) {
	keyBytes := []byte(key)
	if len(keyBytes) != 32 {
		return "", fmt.Errorf("TOTP_ENCRYPTION_KEY debe ser exactamente 32 bytes, tiene %d", len(keyBytes))
	}

	data, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", fmt.Errorf("error al decodificar base64: %w", err)
	}

	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return "", fmt.Errorf("error al crear cipher AES: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("error al crear GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("datos cifrados demasiado cortos")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("error al descifrar secreto TOTP: %w", err)
	}

	return string(plaintext), nil
}

// ValidarCodigo verifica un código TOTP de 6 dígitos contra el secreto cifrado.
// Usa una ventana de ±1 período (30s) para tolerar desfases de reloj.
func ValidarCodigo(secretoCifrado, encryptionKey, codigo string) (bool, error) {
	secreto, err := DescifrarSecreto(secretoCifrado, encryptionKey)
	if err != nil {
		return false, fmt.Errorf("error al descifrar secreto para validación: %w", err)
	}

	valido, err := totp.ValidateCustom(codigo, secreto, time.Now().UTC(), totp.ValidateOpts{
		Period:    30,
		Skew:      1, // ±1 período de tolerancia
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		return false, fmt.Errorf("error al validar código TOTP: %w", err)
	}

	return valido, nil
}
