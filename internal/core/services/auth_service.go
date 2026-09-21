package services

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/infrastructure/crypto"

	"github.com/google/uuid"
	"github.com/skip2/go-qrcode"
)

// authServiceImpl implementa ports.AuthService.
type authServiceImpl struct {
	repo ports.AuthRepository
}

// NewAuthService crea una nueva instancia del servicio de autenticación.
func NewAuthService(repo ports.AuthRepository) ports.AuthService {
	return &authServiceImpl{repo: repo}
}

// ==========================================
// Login
// ==========================================

// Login valida credenciales de email+contraseña y emite un JWT temporal pre-2FA.
// El JWT temporal tiene una vida muy corta (5 minutos) y solo sirve para el flujo 2FA.
func (s *authServiceImpl) Login(ctx context.Context, email, contrasena string) (*ports.LoginResult, error) {
	log.Printf("[AUTH] login attempt | email=%s", email)

	// 1. Buscar usuario por email
	usuario, err := s.repo.BuscarUsuarioPorEmail(ctx, email)
	if err != nil {
		log.Printf("[AUTH] login failed | email=%s | reason=user_not_found", email)
		// No revelar si el email existe o no (seguridad)
		return nil, fmt.Errorf("credenciales inválidas")
	}

	// 2. Verificar que el usuario está activo
	if !usuario.Activo {
		log.Printf("[AUTH] login failed | email=%s | reason=account_inactive", email)
		return nil, fmt.Errorf("cuenta desactivada, contacte al administrador")
	}

	// 3. Verificar contraseña
	if !crypto.VerificarContrasena(usuario.ContrasenaHash, contrasena) {
		log.Printf("[AUTH] login failed | email=%s | reason=wrong_password", email)
		return nil, fmt.Errorf("credenciales inválidas")
	}

	// 4. Crear sesión temporal pre-2FA en BD
	jti := uuid.New().String()
	expiracion := time.Now().Add(5 * time.Minute)
	sesion := &domain.SesionActiva{
		ID:              uuid.New(),
		UsuarioID:       usuario.ID,
		JtiToken:        jti,
		Activa:          true,
		Estado2fa:       false,
		FechaExpiracion: expiracion,
	}
	if err := s.repo.GuardarSesion(ctx, sesion); err != nil {
		return nil, fmt.Errorf("error al crear sesión: %w", err)
	}

	// 5. Emitir JWT temporal
	jwtSecret := os.Getenv("JWT_SECRET")
	claims := crypto.JWTClaims{
		Rol:           usuario.Rol,
		Tipo:          crypto.TipoPreAuth,
		Verificado2FA: false,
		OrgID:         usuario.OrganizacionID.String(),
	}
	claims.Subject = usuario.ID.String()
	claims.ID = jti

	jwtTemporal, err := crypto.FirmarToken(claims, jwtSecret, 5*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("error al emitir token temporal: %w", err)
	}

	log.Printf("[AUTH] login ok | user=%s | rol=%s | totp_vinculado=%v | cambio_pass=%v | jti=%s", usuario.EmailUsuario, usuario.Rol, usuario.TotpVinculado, usuario.CambioContrasena, jti)

	return &ports.LoginResult{
		JWTTemporal:               jwtTemporal,
		TotpVinculado:             usuario.TotpVinculado,
		CambioContrasenaRequerido: usuario.CambioContrasena,
	}, nil
}

// ==========================================
// ObtenerQRParaVinculacion
// ==========================================

// ObtenerQRParaVinculacion genera un nuevo secreto TOTP y lo retorna como imagen QR.
// Solo funciona con un JWT temporal válido (pre-auth) cuando el usuario aún no tiene TOTP vinculado.
func (s *authServiceImpl) ObtenerQRParaVinculacion(ctx context.Context, jtiTemporal string) (*ports.QRResult, error) {
	jwtSecret := os.Getenv("JWT_SECRET")
	encKey := os.Getenv("TOTP_ENCRYPTION_KEY")
	appName := os.Getenv("APP_NAME")
	if appName == "" {
		appName = "El Centinela"
	}

	// 1. Buscar la sesión en BD para obtener el usuarioID
	sesion, err := s.repo.BuscarSesionPorJTI(ctx, jtiTemporal)
	if err != nil {
		return nil, fmt.Errorf("sesión no válida: %w", err)
	}

	// 2. Buscar el usuario para obtener su email
	usuario, err := s.repo.BuscarUsuarioPorID(ctx, sesion.UsuarioID)
	if err != nil {
		return nil, fmt.Errorf("usuario no encontrado: %w", err)
	}

	// Verificar que el JWT secret es correcto (no expuesto al cliente)
	_ = jwtSecret // Se usa en el middleware, aquí solo necesitamos la sesión de BD

	// Rechazar si el usuario ya tiene TOTP vinculado (requiere reset administrativo para reemplazar)
	if usuario.TotpVinculado {
		log.Printf("[2FA] qr rejected | user=%s | reason=totp_already_linked", usuario.EmailUsuario)
		return nil, fmt.Errorf("el doble factor ya está activo. Para regenerar el QR se requiere un restablecimiento administrativo")
	}

	log.Printf("[2FA] qr requested | user=%s | jti=%s", usuario.EmailUsuario, jtiTemporal)

	// 3. Generar nuevo secreto TOTP
	key, err := crypto.GenerarSecreto(appName, usuario.EmailUsuario)
	if err != nil {
		return nil, fmt.Errorf("error al generar secreto TOTP: %w", err)
	}

	// 4. Cifrar secreto y guardarlo en BD (TotpVinculado sigue en false hasta que se verifique)
	secretoCifrado, err := crypto.CifrarSecreto(key.Secret(), encKey)
	if err != nil {
		return nil, fmt.Errorf("error al cifrar secreto TOTP: %w", err)
	}
	if err := s.repo.ActualizarTotp(ctx, usuario.ID, secretoCifrado, false); err != nil {
		return nil, fmt.Errorf("error al guardar secreto TOTP: %w", err)
	}

	// 5. Generar QR PNG en base64 desde la URL otpauth://
	qrPNG, err := qrcode.Encode(key.URL(), qrcode.Medium, 256)
	if err != nil {
		return nil, fmt.Errorf("error al generar QR: %w", err)
	}
	qrBase64 := "data:image/png;base64," + base64.StdEncoding.EncodeToString(qrPNG)

	log.Printf("[2FA] qr generated | user=%s | secreto_len=%d | qr_bytes=%d", usuario.EmailUsuario, len(key.Secret()), len(qrPNG))

	return &ports.QRResult{
		QRBase64:      qrBase64,
		SecretoManual: key.Secret(), // Solo retornado aquí, nunca más desde la BD
	}, nil
}

// ==========================================
// VerificarTotp
// ==========================================

// VerificarTotp valida el código TOTP y emite access + refresh tokens si es correcto.
func (s *authServiceImpl) VerificarTotp(ctx context.Context, jtiTemporal, codigo string) (*ports.TokenResult, error) {
	jwtSecret := os.Getenv("JWT_SECRET")
	encKey := os.Getenv("TOTP_ENCRYPTION_KEY")

	// 1. Buscar la sesión pre-2FA en BD
	sesion, err := s.repo.BuscarSesionPorJTI(ctx, jtiTemporal)
	if err != nil {
		return nil, fmt.Errorf("sesión no válida: %w", err)
	}
	if sesion.Estado2fa {
		return nil, fmt.Errorf("sesión ya fue verificada")
	}

	// 2. Buscar usuario y validar TOTP
	usuario, err := s.repo.BuscarUsuarioPorID(ctx, sesion.UsuarioID)
	if err != nil {
		return nil, fmt.Errorf("usuario no encontrado: %w", err)
	}
	if usuario.SecretoTotpCifrado == "" {
		return nil, fmt.Errorf("el usuario no tiene TOTP configurado, obtenga primero el QR")
	}

	log.Printf("[2FA] totp verify attempt | user=%s | codigo=%s", usuario.EmailUsuario, codigo)
	valido, err := crypto.ValidarCodigo(usuario.SecretoTotpCifrado, encKey, codigo)
	if err != nil {
		return nil, fmt.Errorf("error al validar código TOTP: %w", err)
	}
	if !valido {
		log.Printf("[2FA] totp invalid | user=%s", usuario.EmailUsuario)
		return nil, fmt.Errorf("código TOTP incorrecto")
	}

	// 3. Si era la primera vinculación, marcar como vinculado
	if !usuario.TotpVinculado {
		log.Printf("[2FA] first link confirmed | user=%s", usuario.EmailUsuario)
		if err := s.repo.ActualizarTotp(ctx, usuario.ID, usuario.SecretoTotpCifrado, true); err != nil {
			return nil, fmt.Errorf("error al confirmar vinculación TOTP: %w", err)
		}
	}

	// 4. Actualizar sesión: Estado2fa=true
	sesion.Estado2fa = true
	if err := s.repo.ActualizarSesion(ctx, sesion); err != nil {
		return nil, fmt.Errorf("error al actualizar sesión: %w", err)
	}

	// 5. Emitir access token
	accessTTL := obtenerAccessTTL()
	jtiAccess := uuid.New().String()
	accessClaims := crypto.JWTClaims{
		Rol:           usuario.Rol,
		Tipo:          crypto.TipoAccess,
		Verificado2FA: true,
		OrgID:         usuario.OrganizacionID.String(),
	}
	accessClaims.Subject = usuario.ID.String()
	accessClaims.ID = jtiAccess

	accessToken, err := crypto.FirmarToken(accessClaims, jwtSecret, accessTTL)
	if err != nil {
		return nil, fmt.Errorf("error al emitir access token: %w", err)
	}

	// 6. Emitir refresh token con su propio JTI
	refreshTTL := obtenerRefreshTTL()
	jtiRefresh := uuid.New().String()
	refreshClaims := crypto.JWTClaims{
		Rol:           usuario.Rol,
		Tipo:          crypto.TipoRefresh,
		Verificado2FA: true,
		OrgID:         usuario.OrganizacionID.String(),
	}
	refreshClaims.Subject = usuario.ID.String()
	refreshClaims.ID = jtiRefresh

	refreshToken, err := crypto.FirmarToken(refreshClaims, jwtSecret, refreshTTL)
	if err != nil {
		return nil, fmt.Errorf("error al emitir refresh token: %w", err)
	}

	// 7. Guardar sesión de refresh en BD (reutilizando el modelo SesionActiva)
	sesionRefresh := &domain.SesionActiva{
		ID:              uuid.New(),
		UsuarioID:       usuario.ID,
		JtiToken:        jtiRefresh,
		Activa:          true,
		Estado2fa:       true,
		FechaExpiracion: time.Now().Add(refreshTTL),
	}
	if err := s.repo.GuardarSesion(ctx, sesionRefresh); err != nil {
		return nil, fmt.Errorf("error al guardar sesión de refresh: %w", err)
	}

	log.Printf("[AUTH] tokens issued | user=%s | access_jti=%s | refresh_jti=%s | access_ttl=%s", usuario.EmailUsuario, jtiAccess, jtiRefresh, accessTTL)

	return &ports.TokenResult{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int64(accessTTL.Seconds()),
	}, nil
}

// ==========================================
// RefrescarToken
// ==========================================

// RefrescarToken valida un refresh token y emite un nuevo access token.
func (s *authServiceImpl) RefrescarToken(ctx context.Context, refreshToken string) (*ports.TokenResult, error) {
	jwtSecret := os.Getenv("JWT_SECRET")

	log.Printf("[AUTH] refresh attempt")

	// 1. Verificar firma y claims del refresh token
	claims, err := crypto.VerificarToken(refreshToken, jwtSecret)
	if err != nil {
		log.Printf("[AUTH] refresh failed | reason=invalid_token")
		return nil, fmt.Errorf("refresh token inválido: %w", err)
	}
	if claims.Tipo != crypto.TipoRefresh {
		log.Printf("[AUTH] refresh failed | reason=wrong_type | got=%s", claims.Tipo)
		return nil, fmt.Errorf("token no es del tipo refresh")
	}

	// 2. Verificar que la sesión de refresh está activa en BD
	_, err = s.repo.BuscarSesionPorJTI(ctx, claims.ID)
	if err != nil {
		return nil, fmt.Errorf("sesión de refresh no válida o revocada: %w", err)
	}

	// 3. Buscar usuario para obtener datos actualizados
	usuarioID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return nil, fmt.Errorf("token con subject inválido: %w", err)
	}
	usuario, err := s.repo.BuscarUsuarioPorID(ctx, usuarioID)
	if err != nil {
		return nil, fmt.Errorf("usuario no encontrado: %w", err)
	}
	if !usuario.Activo {
		return nil, fmt.Errorf("cuenta desactivada")
	}

	// 4. Emitir nuevo access token
	accessTTL := obtenerAccessTTL()
	jtiAccess := uuid.New().String()
	accessClaims := crypto.JWTClaims{
		Rol:           usuario.Rol,
		Tipo:          crypto.TipoAccess,
		Verificado2FA: true,
		OrgID:         usuario.OrganizacionID.String(),
	}
	accessClaims.Subject = usuario.ID.String()
	accessClaims.ID = jtiAccess

	accessToken, err := crypto.FirmarToken(accessClaims, jwtSecret, accessTTL)
	if err != nil {
		return nil, fmt.Errorf("error al emitir access token: %w", err)
	}

	log.Printf("[AUTH] refresh ok | user=%s | new_access_jti=%s", usuario.EmailUsuario, jtiAccess)

	return &ports.TokenResult{
		AccessToken:  accessToken,
		RefreshToken: refreshToken, // El refresh token se mantiene igual
		ExpiresIn:    int64(accessTTL.Seconds()),
	}, nil
}

// ==========================================
// CerrarSesion
// ==========================================

// CerrarSesion invalida la sesión asociada al refresh token recibido (logout).
// Se apoya en el refresh token para que el cierre funcione aunque el access
// token ya haya expirado.
func (s *authServiceImpl) CerrarSesion(ctx context.Context, refreshToken string) error {
	jwtSecret := os.Getenv("JWT_SECRET")

	log.Printf("[AUTH] logout attempt")

	// 1. Verificar firma y claims del refresh token
	claims, err := crypto.VerificarToken(refreshToken, jwtSecret)
	if err != nil {
		log.Printf("[AUTH] logout failed | reason=invalid_token")
		return fmt.Errorf("refresh token inválido: %w", err)
	}
	if claims.Tipo != crypto.TipoRefresh {
		log.Printf("[AUTH] logout failed | reason=wrong_type | got=%s", claims.Tipo)
		return fmt.Errorf("token no es del tipo refresh")
	}

	// 2. Buscar la sesión asociada al JTI
	sesion, err := s.repo.BuscarSesionPorJTI(ctx, claims.ID)
	if err != nil {
		log.Printf("[AUTH] logout failed | reason=session_not_found")
		return fmt.Errorf("sesión no válida o ya cerrada: %w", err)
	}

	// 3. Desactivarla
	sesion.Activa = false
	if err := s.repo.ActualizarSesion(ctx, sesion); err != nil {
		return fmt.Errorf("no se pudo cerrar la sesión: %w", err)
	}

	log.Printf("[AUTH] logout done | jti=%s", claims.ID)
	return nil
}

// ==========================================
// SolicitarRevinculacion
// ==========================================

// SolicitarRevinculacion permite a un administrador resetear el 2FA de otro usuario.
// El jtiAdmin corresponde al JTI del access token del administrador que hace la solicitud.
// Nota: la verificación de rol ADMIN ya fue hecha por el middleware RequireAuth + RequireRole.
func (s *authServiceImpl) SolicitarRevinculacion(ctx context.Context, jtiAdmin string, targetUsuarioID uuid.UUID) error {
	log.Printf("[ADMIN] relink requested | target_user=%s | admin_jti=%s", targetUsuarioID, jtiAdmin)

	// 1. Resetear el TOTP del usuario objetivo
	if err := s.repo.ResetearTotp(ctx, targetUsuarioID); err != nil {
		return fmt.Errorf("error al resetear TOTP: %w", err)
	}

	// 2. Invalidar todas las sesiones activas del usuario objetivo (forzar re-login)
	if err := s.repo.InvalidarSesionesDeUsuario(ctx, targetUsuarioID); err != nil {
		return fmt.Errorf("error al invalidar sesiones del usuario: %w", err)
	}

	log.Printf("[ADMIN] relink done | target_user=%s | totp_reset=true | sessions_invalidated=true", targetUsuarioID)
	return nil
}

// ==========================================
// Helpers de configuración
// ==========================================

func obtenerAccessTTL() time.Duration {
	hours := 8
	if val := os.Getenv("JWT_ACCESS_TTL_HOURS"); val != "" {
		if h, err := strconv.Atoi(val); err == nil && h > 0 {
			hours = h
		}
	}
	return time.Duration(hours) * time.Hour
}

func obtenerRefreshTTL() time.Duration {
	days := 30
	if val := os.Getenv("JWT_REFRESH_TTL_DAYS"); val != "" {
		if d, err := strconv.Atoi(val); err == nil && d > 0 {
			days = d
		}
	}
	return time.Duration(days) * 24 * time.Hour
}

