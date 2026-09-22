package services

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"math/big"
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
	repo         ports.AuthRepository
	emailService ports.EmailService
	auditSvc     ports.AuditService
}

// NewAuthService crea una nueva instancia del servicio de autenticación.
func NewAuthService(repo ports.AuthRepository, emailService ports.EmailService, auditSvc ports.AuditService) ports.AuthService {
	return &authServiceImpl{
		repo:         repo,
		emailService: emailService,
		auditSvc:     auditSvc,	
	}
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
		s.auditSvc.Registrar(ctx, ports.RegistrarAuditoriaInput{
			UsuarioID: usuario.ID,
			Accion:    ports.AccionLoginFalla,
			Resultado: ports.ResultadoFalla,
			Detalles:  map[string]any{"razon": "contraseña incorrecta"},
		})
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

	s.auditSvc.Registrar(ctx, ports.RegistrarAuditoriaInput{
		UsuarioID: usuario.ID,
		Accion:    ports.AccionLogin,
		Resultado: ports.ResultadoExito,
	})

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
		s.auditSvc.Registrar(ctx, ports.RegistrarAuditoriaInput{
			UsuarioID: usuario.ID,
			Accion:    ports.Accion2FAFalla,
			Resultado: ports.ResultadoFalla,
			Detalles:  map[string]any{"razon": "código TOTP incorrecto"},
		})
		return nil, fmt.Errorf("código TOTP incorrecto")
	}

	// Protección anti-replay
	periodoActual := time.Now().Unix() / 30
	if usuario.UltimoTotpPeriodo != nil && *usuario.UltimoTotpPeriodo == periodoActual {
		log.Printf("[2FA] totp replay detected | user=%s", usuario.EmailUsuario)
		return nil, fmt.Errorf("código TOTP ya utilizado, espere al siguiente código")
	}

	if err := s.repo.ActualizarUltimoTotpPeriodo(ctx, usuario.ID, periodoActual); err != nil {
		return nil, fmt.Errorf("error al registrar uso del código TOTP: %w", err)
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
		Rol:                       usuario.Rol,
		Tipo:                      crypto.TipoAccess,
		Verificado2FA:             true,
		OrgID:                     usuario.OrganizacionID.String(),
		CambioContrasenaRequerido: usuario.CambioContrasena,
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

	// 8. Guardar sesión de access en BD (para poder revocar el token individualmente)
	sesionAccess := &domain.SesionActiva{
		ID:              uuid.New(),
		UsuarioID:       usuario.ID,
		JtiToken:        jtiAccess,
		Activa:          true,
		Estado2fa:       true,
		FechaExpiracion: time.Now().Add(accessTTL),
	}
	if err := s.repo.GuardarSesion(ctx, sesionAccess); err != nil {
		return nil, fmt.Errorf("error al guardar sesión de access: %w", err)
	}

	log.Printf("[AUTH] tokens issued | user=%s | access_jti=%s | refresh_jti=%s | access_ttl=%s", usuario.EmailUsuario, jtiAccess, jtiRefresh, accessTTL)

	s.auditSvc.Registrar(ctx, ports.RegistrarAuditoriaInput{
		UsuarioID: usuario.ID,
		Accion:    ports.AccionVerificar2FA,
		Resultado: ports.ResultadoExito,
	})

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

	// 4. Emitir nuevo access token (refleja el estado actual de la BD)
	accessTTL := obtenerAccessTTL()
	jtiAccess := uuid.New().String()
	accessClaims := crypto.JWTClaims{
		Rol:                       usuario.Rol,
		Tipo:                      crypto.TipoAccess,
		Verificado2FA:             true,
		OrgID:                     usuario.OrganizacionID.String(),
		CambioContrasenaRequerido: usuario.CambioContrasena,
	}
	accessClaims.Subject = usuario.ID.String()
	accessClaims.ID = jtiAccess

	accessToken, err := crypto.FirmarToken(accessClaims, jwtSecret, accessTTL)
	if err != nil {
		return nil, fmt.Errorf("error al emitir access token: %w", err)
	}

	// 5. Guardar sesión de access en BD (para poder revocar el token individualmente)
	sesionAccess := &domain.SesionActiva{
		ID:              uuid.New(),
		UsuarioID:       usuario.ID,
		JtiToken:        jtiAccess,
		Activa:          true,
		Estado2fa:       true,
		FechaExpiracion: time.Now().Add(accessTTL),
	}
	if err := s.repo.GuardarSesion(ctx, sesionAccess); err != nil {
		return nil, fmt.Errorf("error al guardar sesión de access: %w", err)
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

// CerrarSesion invalida la sesión asociada al refresh token recibido y al access token actual.
// Se apoya en el JTI para cortar el acceso inmediatamente.
func (s *authServiceImpl) CerrarSesion(ctx context.Context, refreshToken, accessTokenJTI string) error {
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

	// 2. Revocar ambas sesiones en una sola transacción
	jtis := []string{claims.ID}
	if accessTokenJTI != "" {
		jtis = append(jtis, accessTokenJTI)
	}

	if err := s.repo.RevocarSesiones(ctx, jtis); err != nil {
		log.Printf("[AUTH] logout failed | reason=db_error")
		return fmt.Errorf("no se pudo cerrar la sesión: %w", err)
	}

	log.Printf("[AUTH] logout done | refresh_jti=%s | access_jti=%s", claims.ID, accessTokenJTI)
	
	usuarioID, _ := uuid.Parse(claims.Subject)
	s.auditSvc.Registrar(ctx, ports.RegistrarAuditoriaInput{
		UsuarioID: usuarioID,
		Accion:    ports.AccionLogout,
		Resultado: ports.ResultadoExito,
		Detalles: map[string]any{
			"reason":        "user_requested",
			"resource_type": "AUTH",
			"resource_id":   nil,
		},
	})
	
	return nil
}

// RevocarSesionesUsuario revoca todas las sesiones activas (access y refresh) de un usuario en un solo llamado.
func (s *authServiceImpl) RevocarSesionesUsuario(ctx context.Context, usuarioID uuid.UUID) error {
	log.Printf("[AUTH] revoking all sessions for user=%s", usuarioID)
	if err := s.repo.InvalidarSesionesDeUsuario(ctx, usuarioID); err != nil {
		return fmt.Errorf("error al revocar sesiones: %w", err)
	}
	return nil
}

// ==========================================
// Recuperación de Contraseña
// ==========================================

// SolicitarRecuperacionContrasena genera un código temporal de 6 dígitos, lo persiste en el usuario
// y lo envía usando el servicio de email (simulado o real).
func (s *authServiceImpl) SolicitarRecuperacionContrasena(ctx context.Context, email string) error {
	usuario, err := s.repo.BuscarUsuarioPorEmail(ctx, email)
	
	// Prevenir enumeración y ataques de timing (Timing Attacks)
	// Si el usuario no existe o está inactivo, realizamos un trabajo computacional similar 
	// (como hashear una clave dummy) para que el tiempo de respuesta sea indistinguible.
	if err != nil || !usuario.Activo {
		crypto.HashContrasena("dummy-hash-to-prevent-timing-attacks")
		log.Printf("[AUTH] recuperacion solicitada para email no existente o inactivo: %s", email)
		return nil
	}

	// Generar código numérico criptográficamente seguro de 6 dígitos
	max := big.NewInt(1000000)
	n, _ := rand.Int(rand.Reader, max)
	codigo := fmt.Sprintf("%06d", n.Int64())
	
	// Expiración estricta de 10 minutos (600 segundos)
	expiracion := time.Now().Add(10 * time.Minute)

	if err := s.repo.ActualizarCodigoRecuperacion(ctx, usuario.ID, &codigo, &expiracion); err != nil {
		return fmt.Errorf("error al guardar código de recuperación: %w", err)
	}

	if err := s.emailService.EnviarCodigoRecuperacion(email, codigo); err != nil {
		return fmt.Errorf("error al enviar el correo de recuperación: %w", err)
	}

	log.Printf("[AUTH] código de recuperación generado para %s", email)
	return nil
}

// ConfirmarRecuperacionContrasena valida que el código temporal sea correcto y no esté expirado,
// actualiza la contraseña y limpia el código. Invalida también todas las sesiones previas.
func (s *authServiceImpl) ConfirmarRecuperacionContrasena(ctx context.Context, email, codigo, nuevaContrasena string) error {
	usuario, err := s.repo.BuscarUsuarioPorEmail(ctx, email)
	if err != nil {
		return fmt.Errorf("código inválido o expirado")
	}

	if usuario.CodigoRecuperacion == nil {
		return fmt.Errorf("código inválido o expirado")
	}

	if *usuario.CodigoRecuperacion != codigo {
		intentos := usuario.IntentosRecuperacion + 1
		if intentos >= 3 {
			// Invalidar el código por demasiados intentos (fuerza bruta)
			_ = s.repo.ActualizarCodigoRecuperacion(ctx, usuario.ID, nil, nil)
			return fmt.Errorf("demasiados intentos fallidos, el código ha sido invalidado")
		}
		// Sumar el intento fallido
		_ = s.repo.ActualizarIntentosRecuperacion(ctx, usuario.ID, intentos)
		return fmt.Errorf("código inválido")
	}

	if usuario.ExpiracionCodigo == nil || time.Now().After(*usuario.ExpiracionCodigo) {
		return fmt.Errorf("el código ha expirado")
	}

	// Validar complejidad de la nueva contraseña
	if err := crypto.ValidarComplejidadContrasena(nuevaContrasena); err != nil {
		return err
	}

	// Validar que la nueva contraseña difiera de la anterior
	if crypto.VerificarContrasena(usuario.ContrasenaHash, nuevaContrasena) {
		return fmt.Errorf("la nueva contraseña no puede ser igual a la actual")
	}

	hash, err := crypto.HashContrasena(nuevaContrasena)
	if err != nil {
		return fmt.Errorf("error al procesar la nueva contraseña: %w", err)
	}

	// Actualizar contraseña y limpiar el código
	if err := s.repo.ActualizarContrasenaYLimpiarCodigo(ctx, usuario.ID, hash); err != nil {
		return fmt.Errorf("error al guardar la nueva contraseña: %w", err)
	}

	// Invalidar sesiones para forzar re-login con la nueva clave
	if err := s.repo.InvalidarSesionesDeUsuario(ctx, usuario.ID); err != nil {
		log.Printf("[AUTH] advertencia: error al invalidar sesiones tras recuperar contraseña %s: %v", usuario.ID, err)
	}

	log.Printf("[AUTH] contraseña recuperada exitosamente para %s", email)
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

