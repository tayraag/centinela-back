// Package redis implementa ports.SesionCache sobre un Redis real.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

const (
	prefijoPre2FA = "auth:pre2fa:"
	prefijoSesion = "auth:session:"
)

// SesionCache guarda las sesiones en Redis con TTL (SET ... EX).
type SesionCache struct {
	client *goredis.Client
}

var _ ports.SesionCache = (*SesionCache)(nil)

// Conectar abre la conexión a la base db de Redis y verifica con un PING que responda.
func Conectar(addr, password string, db int) (*SesionCache, error) {
	client := goredis.NewClient(&goredis.Options{Addr: addr, Password: password, DB: db})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis no responde en %s: %w", addr, err)
	}
	return &SesionCache{client: client}, nil
}

// NewSesionCache usa un cliente ya creado (útil en los tests con miniredis).
func NewSesionCache(client *goredis.Client) *SesionCache {
	return &SesionCache{client: client}
}

// SET auth:pre2fa:<jti> <usuario_id> EX <ttl>
func (s *SesionCache) GuardarPre2FA(ctx context.Context, jti string, usuarioID uuid.UUID, ttl time.Duration) error {
	if err := s.client.Set(ctx, prefijoPre2FA+jti, usuarioID.String(), ttl).Err(); err != nil {
		return fmt.Errorf("redis: error al guardar sesión pre-2FA: %w", err)
	}
	return nil
}

func (s *SesionCache) ObtenerPre2FA(ctx context.Context, jti string) (uuid.UUID, error) {
	valor, err := s.client.Get(ctx, prefijoPre2FA+jti).Result()
	if errors.Is(err, goredis.Nil) {
		return uuid.Nil, ports.ErrSesionNoEncontrada
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("redis: error al leer sesión pre-2FA: %w", err)
	}
	return uuid.Parse(valor)
}

// DEL auth:pre2fa:<jti>
func (s *SesionCache) EliminarPre2FA(ctx context.Context, jti string) (bool, error) {
	borradas, err := s.client.Del(ctx, prefijoPre2FA+jti).Result()
	if err != nil {
		return false, fmt.Errorf("redis: error al eliminar sesión pre-2FA: %w", err)
	}
	return borradas == 1, nil
}

// SET auth:session:<session_id> <payload> EX <ttl>
func (s *SesionCache) GuardarSesion(ctx context.Context, sesion ports.SesionCacheada, ttl time.Duration) error {
	payload, err := json.Marshal(sesion)
	if err != nil {
		return fmt.Errorf("redis: error al serializar sesión: %w", err)
	}
	if err := s.client.Set(ctx, prefijoSesion+sesion.SesionID.String(), payload, ttl).Err(); err != nil {
		return fmt.Errorf("redis: error al guardar sesión: %w", err)
	}
	return nil
}

func (s *SesionCache) ObtenerSesion(ctx context.Context, sesionID uuid.UUID) (*ports.SesionCacheada, error) {
	payload, err := s.client.Get(ctx, prefijoSesion+sesionID.String()).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, ports.ErrSesionNoEncontrada
	}
	if err != nil {
		return nil, fmt.Errorf("redis: error al leer sesión: %w", err)
	}
	var sesion ports.SesionCacheada
	if err := json.Unmarshal(payload, &sesion); err != nil {
		return nil, fmt.Errorf("redis: sesión con formato inválido: %w", err)
	}
	return &sesion, nil
}

// DEL auth:session:<session_id> ...
func (s *SesionCache) EliminarSesiones(ctx context.Context, sesionIDs ...uuid.UUID) error {
	if len(sesionIDs) == 0 {
		return nil
	}
	claves := make([]string, len(sesionIDs))
	for i, id := range sesionIDs {
		claves[i] = prefijoSesion + id.String()
	}
	if err := s.client.Del(ctx, claves...).Err(); err != nil {
		return fmt.Errorf("redis: error al eliminar sesiones: %w", err)
	}
	return nil
}
