package crypto

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// TipoToken distingue entre el token temporal pre-2FA y los tokens definitivos.
const (
	TipoPreAuth = "pre-auth"
	TipoAccess  = "access"
	TipoRefresh = "refresh"
)

// JWTClaims contiene los claims personalizados del token JWT.
type JWTClaims struct {
	Rol           string    `json:"rol"`
	Tipo          string    `json:"tipo"`            // "pre-auth" | "access" | "refresh"
	Verificado2FA bool      `json:"2fa_verificado"`
	OrgID         string    `json:"org_id"`          // UUID de la organización del usuario
	jwt.RegisteredClaims
}

// FirmarToken genera y firma un JWT con los claims dados.
// El JTI se genera automáticamente si no está seteado en los RegisteredClaims.
func FirmarToken(claims JWTClaims, secret string, ttl time.Duration) (string, error) {
	now := time.Now()

	// Setear timestamps y JTI si no están definidos
	if claims.IssuedAt == nil {
		claims.IssuedAt = jwt.NewNumericDate(now)
	}
	if claims.ExpiresAt == nil {
		claims.ExpiresAt = jwt.NewNumericDate(now.Add(ttl))
	}
	if claims.ID == "" {
		claims.ID = uuid.New().String()
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("error al firmar JWT: %w", err)
	}
	return signed, nil
}

// VerificarToken valida la firma y los claims de un JWT.
// Retorna los claims si el token es válido, error en caso contrario.
func VerificarToken(tokenStr, secret string) (*JWTClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &JWTClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("método de firma inesperado: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, fmt.Errorf("token expirado")
		}
		return nil, fmt.Errorf("token inválido: %w", err)
	}

	claims, ok := token.Claims.(*JWTClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("claims inválidos")
	}

	return claims, nil
}
