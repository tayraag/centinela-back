package crypto

import (
	"crypto/rand"
	"fmt"
	"math/big"

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

// GenerarContrasenaTemp genera una contraseña aleatoria segura de 12 caracteres.
// Garantiza al menos una mayúscula, una minúscula, un dígito y un símbolo.
// Usa crypto/rand para aleatoriedad criptográficamente segura.
func GenerarContrasenaTemp() (string, error) {
	const (
		mayusculas = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
		minusculas = "abcdefghijklmnopqrstuvwxyz"
		digitos    = "0123456789"
		simbolos   = "!@#$%^&*"
		todos      = mayusculas + minusculas + digitos + simbolos
		longitud   = 12
	)

	elegir := func(charset string) (byte, error) {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return 0, err
		}
		return charset[n.Int64()], nil
	}

	// Garantizar al menos uno de cada grupo
	resultado := make([]byte, longitud)
	obligatorios := []string{mayusculas, minusculas, digitos, simbolos}
	for i, charset := range obligatorios {
		c, err := elegir(charset)
		if err != nil {
			return "", fmt.Errorf("error al generar contraseña temporal: %w", err)
		}
		resultado[i] = c
	}

	// Rellenar el resto con caracteres aleatorios del conjunto completo
	for i := len(obligatorios); i < longitud; i++ {
		c, err := elegir(todos)
		if err != nil {
			return "", fmt.Errorf("error al generar contraseña temporal: %w", err)
		}
		resultado[i] = c
	}

	// Mezclar para que los obligatorios no queden siempre al inicio
	for i := longitud - 1; i > 0; i-- {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return "", fmt.Errorf("error al mezclar contraseña temporal: %w", err)
		}
		resultado[i], resultado[j.Int64()] = resultado[j.Int64()], resultado[i]
	}

	return string(resultado), nil
}
