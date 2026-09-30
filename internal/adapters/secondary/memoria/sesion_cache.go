// Package memoria implementa ports.SesionCache en la memoria del proceso.
//
// Es el respaldo para cuando no hay Redis disponible (por ejemplo, en un
// servidor donde todavía no se instaló). Respeta los mismos TTL que Redis,
// pero tiene dos limitaciones: las sesiones se pierden al reiniciar la API
// (hay que volver a loguearse) y no se comparten entre varias instancias de
// la API. Para una sola instancia funciona igual que Redis.
package memoria

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
)

const (
	prefijoPre2FA = "auth:pre2fa:"
	prefijoSesion = "auth:session:"
	// cadaCuantoLimpiar: cada tanto se barren las claves vencidas para que no se acumulen.
	cadaCuantoLimpiar = time.Minute
)

type entrada struct {
	valor []byte
	vence time.Time
}

// SesionCache es un mapa clave → valor con vencimiento, seguro para concurrencia.
type SesionCache struct {
	mu             sync.Mutex
	datos          map[string]entrada
	ultimaLimpieza time.Time
	ahora          func() time.Time
}

var _ ports.SesionCache = (*SesionCache)(nil)

// NewSesionCache crea el almacén vacío.
func NewSesionCache() *SesionCache {
	return NewSesionCacheConReloj(time.Now)
}

// NewSesionCacheConReloj permite inyectar el reloj (para probar los TTL en tests).
func NewSesionCacheConReloj(ahora func() time.Time) *SesionCache {
	return &SesionCache{datos: map[string]entrada{}, ahora: ahora}
}

func (s *SesionCache) set(clave string, valor []byte, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ahora := s.ahora()
	s.datos[clave] = entrada{valor: valor, vence: ahora.Add(ttl)}
	if ahora.Sub(s.ultimaLimpieza) > cadaCuantoLimpiar {
		for k, e := range s.datos {
			if !ahora.Before(e.vence) {
				delete(s.datos, k)
			}
		}
		s.ultimaLimpieza = ahora
	}
}

func (s *SesionCache) get(clave string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.datos[clave]
	if !ok {
		return nil, false
	}
	if !s.ahora().Before(e.vence) {
		delete(s.datos, clave)
		return nil, false
	}
	return e.valor, true
}

func (s *SesionCache) del(claves ...string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	borradas := 0
	for _, clave := range claves {
		if e, ok := s.datos[clave]; ok {
			delete(s.datos, clave)
			if s.ahora().Before(e.vence) {
				borradas++
			}
		}
	}
	return borradas
}

func (s *SesionCache) GuardarPre2FA(_ context.Context, jti string, usuarioID uuid.UUID, ttl time.Duration) error {
	s.set(prefijoPre2FA+jti, []byte(usuarioID.String()), ttl)
	return nil
}

func (s *SesionCache) ObtenerPre2FA(_ context.Context, jti string) (uuid.UUID, error) {
	valor, ok := s.get(prefijoPre2FA + jti)
	if !ok {
		return uuid.Nil, ports.ErrSesionNoEncontrada
	}
	return uuid.Parse(string(valor))
}

func (s *SesionCache) EliminarPre2FA(_ context.Context, jti string) (bool, error) {
	return s.del(prefijoPre2FA+jti) == 1, nil
}

func (s *SesionCache) GuardarSesion(_ context.Context, sesion ports.SesionCacheada, ttl time.Duration) error {
	payload, err := json.Marshal(sesion)
	if err != nil {
		return err
	}
	s.set(prefijoSesion+sesion.SesionID.String(), payload, ttl)
	return nil
}

func (s *SesionCache) ObtenerSesion(_ context.Context, sesionID uuid.UUID) (*ports.SesionCacheada, error) {
	payload, ok := s.get(prefijoSesion + sesionID.String())
	if !ok {
		return nil, ports.ErrSesionNoEncontrada
	}
	var sesion ports.SesionCacheada
	if err := json.Unmarshal(payload, &sesion); err != nil {
		return nil, err
	}
	return &sesion, nil
}

func (s *SesionCache) EliminarSesiones(_ context.Context, sesionIDs ...uuid.UUID) error {
	claves := make([]string, len(sesionIDs))
	for i, id := range sesionIDs {
		claves[i] = prefijoSesion + id.String()
	}
	s.del(claves...)
	return nil
}
