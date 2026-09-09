package security

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"el-centinela/internal/core/ports"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type JWTProvider struct {
	Secret []byte
}

func NewJWTProvider(secret string) (*JWTProvider, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("JWT_SECRET must contain at least 32 characters")
	}
	return &JWTProvider{Secret: []byte(secret)}, nil
}

func (provider *JWTProvider) Issue(user ports.UserSession, accessTTL, refreshTTL time.Duration) (ports.TokenPair, error) {
	now := time.Now().UTC()
	accessExpires := now.Add(accessTTL)
	refreshExpires := now.Add(refreshTTL)
	accessClaims := jwt.MapClaims{
		"sub": user.ID.String(), "org": user.OrganizationID.String(), "role": user.Role,
		"instances": user.InstanceIDs, "jti": uuid.NewString(), "iat": now.Unix(), "exp": accessExpires.Unix(),
	}
	accessToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).SignedString(provider.Secret)
	if err != nil {
		return ports.TokenPair{}, err
	}
	refreshBytes := make([]byte, 32)
	if _, err := rand.Read(refreshBytes); err != nil {
		return ports.TokenPair{}, err
	}
	return ports.TokenPair{AccessToken: accessToken, RefreshToken: base64.RawURLEncoding.EncodeToString(refreshBytes), AccessExpiresAt: accessExpires, RefreshExpiresAt: refreshExpires}, nil
}