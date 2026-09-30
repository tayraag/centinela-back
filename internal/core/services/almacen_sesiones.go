package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
)

// Claves de sesión en el almacén efímero (ports.KeyValueStore):
//   - auth:pre2fa:<jti>          → usuario_id              (TTL 5 min)
//   - auth:session:<session_id>  → sesionCacheada en JSON  (TTL = vida del refresh)
const (
	prefijoPre2FA = "auth:pre2fa:"
	prefijoSesion = "auth:session:"
)

// errNoEncontrada: la clave no existe o venció.
var errNoEncontrada = ports.ErrClaveNoEncontrada

// sesionCacheada es la copia de una sesión activa que se guarda en el almacén
// para validar cada request sin consultar PostgreSQL (la fuente de verdad).
type sesionCacheada struct {
	SesionID        uuid.UUID `json:"sesion_id"`
	UsuarioID       uuid.UUID `json:"usuario_id"`
	JtiAccess       string    `json:"jti_access"`
	JtiRefresh      string    `json:"jti_refresh"`
	FechaExpiracion time.Time `json:"fecha_expiracion"`
}

// almacenSesiones traduce las operaciones de sesión a claves del KeyValueStore.
type almacenSesiones struct {
	kv ports.KeyValueStore
}

// guardarPre2FA: SET auth:pre2fa:<jti> <usuario_id> EX <ttl>
func (a almacenSesiones) guardarPre2FA(ctx context.Context, jti string, usuarioID uuid.UUID, ttl time.Duration) error {
	return a.kv.Set(ctx, prefijoPre2FA+jti, usuarioID.String(), ttl)
}

// obtenerPre2FA lee el usuario dueño del token temporal sin consumirlo (lo usa el QR).
func (a almacenSesiones) obtenerPre2FA(ctx context.Context, jti string) (uuid.UUID, error) {
	return parsearUsuario(a.kv.Get(ctx, prefijoPre2FA+jti))
}

// consumirPre2FA lee y borra el token temporal en una sola operación atómica
// (GETDEL): si dos verificaciones compiten, solo una lo obtiene.
func (a almacenSesiones) consumirPre2FA(ctx context.Context, jti string) (uuid.UUID, error) {
	return parsearUsuario(a.kv.GetDel(ctx, prefijoPre2FA+jti))
}

// guardarSesion: SET auth:session:<session_id> <payload> EX <ttl>
func (a almacenSesiones) guardarSesion(ctx context.Context, s sesionCacheada, ttl time.Duration) error {
	return a.kv.Set(ctx, prefijoSesion+s.SesionID.String(), s, ttl)
}

func (a almacenSesiones) obtenerSesion(ctx context.Context, sesionID uuid.UUID) (*sesionCacheada, error) {
	payload, err := a.kv.Get(ctx, prefijoSesion+sesionID.String())
	if err != nil {
		return nil, err
	}
	var s sesionCacheada
	if err := json.Unmarshal([]byte(payload), &s); err != nil {
		return nil, fmt.Errorf("sesión con formato inválido en el almacén: %w", err)
	}
	return &s, nil
}

// eliminarSesiones: DEL auth:session:<id> ...
func (a almacenSesiones) eliminarSesiones(ctx context.Context, sesionIDs ...uuid.UUID) error {
	claves := make([]string, len(sesionIDs))
	for i, id := range sesionIDs {
		claves[i] = prefijoSesion + id.String()
	}
	return a.kv.Del(ctx, claves...)
}

func parsearUsuario(valor string, err error) (uuid.UUID, error) {
	if err != nil {
		return uuid.Nil, err
	}
	id, err := uuid.Parse(valor)
	if err != nil {
		return uuid.Nil, errors.New("sesión temporal con formato inválido en el almacén")
	}
	return id, nil
}
