package ports

import (
	"context"
	"errors"
	"time"

	"el-centinela/internal/core/domain"

	"github.com/google/uuid"
)

// ==========================================
// DTOs de resultado del AuthService
// ==========================================

// LoginResult es la respuesta del paso de login (pre-2FA).
type LoginResult struct {
	JWTTemporal               string `json:"jwtTemporal"`
	TotpVinculado             bool   `json:"totpVinculado"`
	CambioContrasenaRequerido bool   `json:"cambioContrasenaRequerido"` // true si el usuario debe cambiar su contraseña antes de continuar
}

// QRResult contiene el QR de vinculación TOTP y el secreto manual como fallback.
type QRResult struct {
	QRBase64      string `json:"qrBase64"`
	SecretoManual string `json:"secretoManual"` // Solo retornado en la vinculación inicial, nunca más
}

// TokenResult contiene el par de tokens emitidos tras autenticación completa.
type TokenResult struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"-"`
	ExpiresIn    int64  `json:"expiresIn"` // segundos hasta expiración del access token
}

// ==========================================
// Puerto: Repositorio de Autenticación
// ==========================================

// AuthRepository define el contrato de persistencia para el dominio de autenticación.
// Las implementaciones concretas viven en los adaptadores secundarios (ej: postgres).
type AuthRepository interface {
	// BuscarUsuarioPorEmail busca un usuario activo por su email.
	BuscarUsuarioPorEmail(ctx context.Context, email string) (*domain.Usuario, error)

	// BuscarUsuarioPorID busca un usuario por su UUID.
	BuscarUsuarioPorID(ctx context.Context, id uuid.UUID) (*domain.Usuario, error)

	// CrearSesionYRegistrarAcceso inserta la única fila de la sesión (al superar
	// el 2FA) y, en la misma transacción, actualiza fecha_ultimo_acceso del usuario.
	CrearSesionYRegistrarAcceso(ctx context.Context, sesion *domain.SesionActiva, fechaAcceso time.Time) error

	// BuscarSesionPorID recupera una sesión por su session_id (activa o no).
	BuscarSesionPorID(ctx context.Context, sesionID uuid.UUID) (*domain.SesionActiva, error)

	// RotarJtiAccess hace el UPDATE de la renovación: reemplaza jti_access en la
	// fila de la sesión. Solo aplica si la sesión está activa, no venció y el
	// refresh coincide; si no, devuelve ErrSesionRevocada. Nunca inserta filas.
	RotarJtiAccess(ctx context.Context, sesionID uuid.UUID, jtiRefresh, nuevoJtiAccess string) error

	// DesactivarSesion marca activa = false en la fila de la sesión (logout).
	// Es idempotente; devuelve ErrSesionRevocada si el refresh no corresponde a la sesión.
	DesactivarSesion(ctx context.Context, sesionID uuid.UUID, jtiRefresh string) error

	// ActualizarTotp guarda el secreto TOTP cifrado y el estado de vinculación.
	ActualizarTotp(ctx context.Context, usuarioID uuid.UUID, secretoCifrado string, vinculado bool) error

	// ResetearTotp establece TotpVinculado=false y borra el secreto cifrado del usuario.
	ResetearTotp(ctx context.Context, usuarioID uuid.UUID) error

	// InvalidarSesionesDeUsuario marca todas las sesiones activas de un usuario
	// como inactivas y devuelve sus IDs (para borrarlas también del almacén efímero).
	InvalidarSesionesDeUsuario(ctx context.Context, usuarioID uuid.UUID) ([]uuid.UUID, error)

	// ActualizarUltimoTotpPeriodo guarda el período del último código TOTP usado (anti-replay).
	ActualizarUltimoTotpPeriodo(ctx context.Context, usuarioID uuid.UUID, periodo int64) error

	// ActualizarCodigoRecuperacion guarda el código de 6 dígitos y su expiración en el usuario.
	ActualizarCodigoRecuperacion(ctx context.Context, usuarioID uuid.UUID, codigo *string, expiracion *time.Time) error

	// ActualizarIntentosRecuperacion actualiza el número de intentos de recuperación fallidos.
	ActualizarIntentosRecuperacion(ctx context.Context, usuarioID uuid.UUID, intentos int) error

	// ActualizarContrasenaYLimpiarCodigo cambia la contraseña y elimina el código temporal usado.
	ActualizarContrasenaYLimpiarCodigo(ctx context.Context, usuarioID uuid.UUID, hash string) error
}

// ErrSesionRevocada indica que la sesión no existe, fue cerrada o revocada, o venció.
var ErrSesionRevocada = errors.New("sesión revocada o expirada")

// ErrCuentaSuspendida: credenciales correctas, pero la cuenta está suspendida
// (activo = false y no eliminada). Se informa solo después de validar la
// contraseña, para no revelar el estado de cuentas ajenas.
var ErrCuentaSuspendida = errors.New("la cuenta está suspendida, contacte al administrador")

// VerificadorSesion es lo que necesitan los middlewares de autenticación para
// saber si un token sigue perteneciendo a una sesión viva.
type VerificadorSesion interface {
	// VerificarSesionPreAuth comprueba que el JTI del token temporal siga vigente.
	VerificarSesionPreAuth(ctx context.Context, jti string) error

	// VerificarSesionAccess comprueba que la sesión esté activa y que el access
	// token sea el vigente (el último emitido para esa sesión).
	VerificarSesionAccess(ctx context.Context, sesionID uuid.UUID, jtiAccess string) error
}

// ==========================================
// Puerto: Servicio de Autenticación
// ==========================================

// AuthService define el contrato de la lógica de negocio de autenticación.
// Lo implementa el servicio de dominio y lo consumen los handlers HTTP.
type AuthService interface {
	VerificadorSesion

	// Login valida credenciales y emite un JWT temporal pre-2FA.
	Login(ctx context.Context, email, contrasena string) (*LoginResult, error)

	// ObtenerQRParaVinculacion genera un secreto TOTP nuevo y lo retorna como QR base64.
	// Requiere un JWT temporal válido (tipo "pre-auth").
	ObtenerQRParaVinculacion(ctx context.Context, jtiTemporal string) (*QRResult, error)

	// VerificarTotp valida el código TOTP y, si es correcto, emite access + refresh tokens.
	VerificarTotp(ctx context.Context, jtiTemporal, codigo string) (*TokenResult, error)

	// RefrescarToken valida un refresh token y emite un nuevo access token.
	RefrescarToken(ctx context.Context, refreshToken string) (*TokenResult, error)

	// CerrarSesion invalida la sesión del refresh token (logout), en Redis y en PostgreSQL.
	CerrarSesion(ctx context.Context, refreshToken string) error

	// RevocarSesionesUsuario invalida inmediatamente todas las sesiones activas de un usuario.
	// Útil para flujos administrativos y reseteos críticos.
	RevocarSesionesUsuario(ctx context.Context, usuarioID uuid.UUID) error

	// SolicitarRecuperacionContrasena genera un código de 6 dígitos y lo envía por email.
	SolicitarRecuperacionContrasena(ctx context.Context, email string) error

	// ConfirmarRecuperacionContrasena valida el código de 6 dígitos y aplica la nueva contraseña.
	ConfirmarRecuperacionContrasena(ctx context.Context, email, codigo, nuevaContrasena string) error
}
