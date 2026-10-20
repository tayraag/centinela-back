package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = 12

// Las contraseñas largas usan un formato explícito para evitar el límite de
// 72 bytes de bcrypt sin truncar ni alterar los hashes existentes.
const prefijoBcryptSHA256 = "$centinela-bcrypt-sha256$v1$"

func entradaContrasenaLarga(raw string) []byte {
	// Separación de dominio: este digest no coincide con un SHA-256 genérico.
	digest := sha256.Sum256([]byte("centinela:password:v1:" + raw))
	return []byte(base64.StdEncoding.EncodeToString(digest[:]))
}

// HashContrasena usa bcrypt con cost 12 y admite hasta 128 caracteres.
func HashContrasena(raw string) (string, error) {
	if utf8.RuneCountInString(raw) > 128 {
		return "", fmt.Errorf("la contraseña no puede superar los 128 caracteres")
	}
	entrada := []byte(raw)
	prefijo := ""
	if len(entrada) > 72 {
		entrada = entradaContrasenaLarga(raw)
		prefijo = prefijoBcryptSHA256
	}
	hash, err := bcrypt.GenerateFromPassword(entrada, bcryptCost)
	if err != nil {
		return "", fmt.Errorf("error al hashear contraseña: %w", err)
	}
	return prefijo + string(hash), nil
}

// VerificarContrasena admite hashes existentes y el formato de claves largas.
func VerificarContrasena(hash, raw string) bool {
	if utf8.RuneCountInString(raw) > 128 {
		return false
	}
	entrada := []byte(raw)
	if strings.HasPrefix(hash, prefijoBcryptSHA256) {
		hash = strings.TrimPrefix(hash, prefijoBcryptSHA256)
		entrada = entradaContrasenaLarga(raw)
	} else if len(entrada) > 72 {
		// Impide que bcrypt acepte una clave por coincidir solo en sus primeros bytes.
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), entrada) == nil
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

// ValidarComplejidadContrasena verifica que la contraseña cumpla las reglas de seguridad:
// - Entre 8 y 128 caracteres
// - Al menos una letra mayúscula
// - Al menos un dígito numérico
// - Al menos un carácter especial (!@#$%^&*-_=+)
// Retorna un error descriptivo si alguna regla no se cumple.
func ValidarComplejidadContrasena(contrasena string) error {
	if utf8.RuneCountInString(contrasena) < 8 {
		return fmt.Errorf("la contraseña debe tener al menos 8 caracteres")
	}
	if utf8.RuneCountInString(contrasena) > 128 {
		return fmt.Errorf("la contraseña no puede superar los 128 caracteres")
	}

	tieneMayuscula := false
	tieneDigito := false
	tieneEspecial := false
	especiales := "!@#$%^&*-_=+"

	for _, c := range contrasena {
		switch {
		case c >= 'A' && c <= 'Z':
			tieneMayuscula = true
		case c >= '0' && c <= '9':
			tieneDigito = true
		case containsRune(especiales, c):
			tieneEspecial = true
		}
	}

	if !tieneMayuscula {
		return fmt.Errorf("la contraseña debe contener al menos una letra mayúscula")
	}
	if !tieneDigito {
		return fmt.Errorf("la contraseña debe contener al menos un número")
	}
	if !tieneEspecial {
		return fmt.Errorf("la contraseña debe contener al menos un carácter especial (!@#$%%^&*-_=+)")
	}

	return nil
}

// containsRune reporta si el rune r está presente en el string s.
func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}
