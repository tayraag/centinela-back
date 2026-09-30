package ports

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ==========================================
// Puerto: Almacén efímero de sesiones (Redis)
// ==========================================

// ErrSesionNoEncontrada indica que la clave no existe o ya venció su TTL.
var ErrSesionNoEncontrada = errors.New("sesión no encontrada o expirada")

// SesionCacheada es la copia de una sesión activa que se guarda en el almacén
// efímero para validar cada request sin consultar PostgreSQL.
type SesionCacheada struct {
	SesionID        uuid.UUID `json:"sesion_id"`
	UsuarioID       uuid.UUID `json:"usuario_id"`
	JtiAccess       string    `json:"jti_access"`
	JtiRefresh      string    `json:"jti_refresh"`
	FechaExpiracion time.Time `json:"fecha_expiracion"`
}

// SesionCache es el almacén efímero de sesiones con TTL automático.
// PostgreSQL sigue siendo la fuente de verdad: esto es el acceso rápido.
//
// Lo implementan internal/adapters/secondary/redis (Redis real) y
// internal/adapters/secondary/memoria (respaldo en memoria para cuando no hay
// Redis disponible, por ejemplo en un servidor que todavía no lo tiene).
//
// Claves:
//   - auth:pre2fa:<jti>          → usuario_id              (TTL 5 min)
//   - auth:session:<session_id>  → SesionCacheada en JSON  (TTL = vida del refresh)
type SesionCache interface {
	// GuardarPre2FA registra el JTI del token temporal pre-2FA.
	GuardarPre2FA(ctx context.Context, jti string, usuarioID uuid.UUID, ttl time.Duration) error

	// ObtenerPre2FA devuelve el usuario dueño del JTI temporal, o ErrSesionNoEncontrada.
	ObtenerPre2FA(ctx context.Context, jti string) (uuid.UUID, error)

	// EliminarPre2FA borra el JTI temporal. Devuelve true si existía: así, si
	// llegan dos verificaciones a la vez con el mismo token, solo una gana.
	EliminarPre2FA(ctx context.Context, jti string) (bool, error)

	// GuardarSesion crea o reemplaza la sesión activa con el TTL indicado.
	GuardarSesion(ctx context.Context, sesion SesionCacheada, ttl time.Duration) error

	// ObtenerSesion devuelve la sesión activa, o ErrSesionNoEncontrada.
	ObtenerSesion(ctx context.Context, sesionID uuid.UUID) (*SesionCacheada, error)

	// EliminarSesiones borra las sesiones indicadas (las inexistentes se ignoran).
	EliminarSesiones(ctx context.Context, sesionIDs ...uuid.UUID) error
}
