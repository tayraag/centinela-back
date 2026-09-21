package ports

import (
	"context"

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
	RefreshToken string `json:"refreshToken"`
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

	// GuardarSesion persiste una nueva sesión activa (pre-2FA o completa).
	GuardarSesion(ctx context.Context, sesion *domain.SesionActiva) error

	// BuscarSesionPorJTI recupera una sesión por su JTI (JWT ID).
	BuscarSesionPorJTI(ctx context.Context, jti string) (*domain.SesionActiva, error)

	// ActualizarSesion actualiza una sesión existente (ej: Estado2fa, Activa).
	ActualizarSesion(ctx context.Context, sesion *domain.SesionActiva) error

	// ActualizarTotp guarda el secreto TOTP cifrado y el estado de vinculación.
	ActualizarTotp(ctx context.Context, usuarioID uuid.UUID, secretoCifrado string, vinculado bool) error

	// ResetearTotp establece TotpVinculado=false y borra el secreto cifrado del usuario.
	ResetearTotp(ctx context.Context, usuarioID uuid.UUID) error

	// InvalidarSesionesDeUsuario marca todas las sesiones activas de un usuario como inactivas.
	InvalidarSesionesDeUsuario(ctx context.Context, usuarioID uuid.UUID) error

	// ActualizarUltimoTotpPeriodo guarda el período del último código TOTP usado (anti-replay).
	ActualizarUltimoTotpPeriodo(ctx context.Context, usuarioID uuid.UUID, periodo int64) error
}

// ==========================================
// Puerto: Servicio de Autenticación
// ==========================================

// AuthService define el contrato de la lógica de negocio de autenticación.
// Lo implementa el servicio de dominio y lo consumen los handlers HTTP.
type AuthService interface {
	// Login valida credenciales y emite un JWT temporal pre-2FA.
	Login(ctx context.Context, email, contrasena string) (*LoginResult, error)

	// ObtenerQRParaVinculacion genera un secreto TOTP nuevo y lo retorna como QR base64.
	// Requiere un JWT temporal válido (tipo "pre-auth").
	ObtenerQRParaVinculacion(ctx context.Context, jtiTemporal string) (*QRResult, error)

	// VerificarTotp valida el código TOTP y, si es correcto, emite access + refresh tokens.
	VerificarTotp(ctx context.Context, jtiTemporal, codigo string) (*TokenResult, error)

	// RefrescarToken valida un refresh token y emite un nuevo access token.
	RefrescarToken(ctx context.Context, refreshToken string) (*TokenResult, error)

	// CerrarSesion invalida la sesión asociada a un refresh token (logout).
	CerrarSesion(ctx context.Context, refreshToken string) error
}
