package crypto

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = 12

// HashContrasena genera un hash bcrypt de la contraseña en texto plano.
// Usa un cost de 12 para un balance adecuado entre seguridad y rendimiento.
func HashContrasena(raw string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("error al hashear contraseña: %w", err)
	}
	return string(hash), nil
}

// VerificarContrasena compara una contraseña en texto plano con su hash bcrypt.
// Retorna true si coinciden, false en caso contrario.
func VerificarContrasena(hash, raw string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(raw))
	return err == nil
}
