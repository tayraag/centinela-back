package services

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
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

// ttlPre2FA es la vida del token temporal entre el login y la verificación 2FA.
const ttlPre2FA = 5 * time.Minute

// authServiceImpl implementa ports.AuthService.
//
// Sesiones: "1 sesión = 1 fila". La fase pre-2FA vive solo en el almacén
// efímero (Redis). Al superar el 2FA se crea la única fila en sesiones_activas
// (fuente de verdad) y se replica en el almacén; cada renovación actualiza esa
// misma fila y el logout la desactiva.
type authServiceImpl struct {
	repo         ports.AuthRepository
	sesiones     almacenSesiones
	bus          busEventos
	emailService ports.EmailService
	auditSvc     ports.AuditService
}

// NewAuthService crea una nueva instancia del servicio de autenticación.
func NewAuthService(repo ports.AuthRepository, kv ports.KeyValueStore, emailService ports.EmailService, auditSvc ports.AuditService) ports.AuthService {
	return &authServiceImpl{
		repo:         repo,
		sesiones:     almacenSesiones{kv: kv},
		bus:          busEventos{kv: kv},
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

	// 4. Registrar la sesión temporal pre-2FA SOLO en el almacén efímero, nunca en
	//    PostgreSQL: SET auth:pre2fa:<jti> <usuario_id> EX 300
	jti := uuid.New().String()
	if err := s.sesiones.guardarPre2FA(ctx, jti, usuario.ID, ttlPre2FA); err != nil {
		log.Printf("[AUTH] login failed | email=%s | reason=session_store_error | err=%v", email, err)
		return nil, fmt.Errorf("no se pudo iniciar la sesión, intentá nuevamente")
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

	jwtTemporal, err := crypto.FirmarToken(claims, jwtSecret, ttlPre2FA)
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

	// 1. Obtener el usuario dueño de la sesión temporal (auth:pre2fa:<jti>)
	usuarioID, err := s.usuarioDePre2FA(ctx, jtiTemporal)
	if err != nil {
		return nil, err
	}

	// 2. Buscar el usuario para obtener su email
	usuario, err := s.repo.BuscarUsuarioPorID(ctx, usuarioID)
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

	// 1. Obtener el usuario dueño de la sesión temporal (auth:pre2fa:<jti>)
	usuarioID, err := s.usuarioDePre2FA(ctx, jtiTemporal)
	if err != nil {
		return nil, err
	}

	// 2. Buscar usuario y validar TOTP
	usuario, err := s.repo.BuscarUsuarioPorID(ctx, usuarioID)
	if err != nil {
		return nil, fmt.Errorf("usuario no encontrado: %w", err)
	}
	if usuario.SecretoTotpCifrado == "" {
		return nil, fmt.Errorf("el usuario no tiene TOTP configurado, obtenga primero el QR")
	}

	log.Printf("[2FA] totp verify attempt | user=%s", usuario.EmailUsuario)
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

	// 4. Consumir la sesión temporal de inmediato con GETDEL auth:pre2fa:<jti>
	//    (lee y borra en un solo paso): si llegan dos verificaciones a la vez
	//    con el mismo token, solo una la obtiene.
	if _, err := s.sesiones.consumirPre2FA(ctx, jtiTemporal); err != nil {
		if errors.Is(err, errNoEncontrada) {
			return nil, fmt.Errorf("sesión ya fue verificada")
		}
		log.Printf("[2FA] verify failed | user=%s | reason=session_store_error | err=%v", usuario.EmailUsuario, err)
		return nil, fmt.Errorf("no se pudo completar la verificación, intentá nuevamente")
	}

	// 5. Emitir access y refresh token, ambos atados a la misma sesión (claim "sid")
	sesionID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("error al generar identificador de sesión: %w", err)
	}
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
	accessClaims.SesionID = sesionID.String()

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
	refreshClaims.SesionID = sesionID.String()

	refreshToken, err := crypto.FirmarToken(refreshClaims, jwtSecret, refreshTTL)
	if err != nil {
		return nil, fmt.Errorf("error al emitir refresh token: %w", err)
	}

	// 7. Crear la ÚNICA fila de la sesión y, en el mismo acto (misma transacción),
	//    UPDATE usuarios SET fecha_ultimo_acceso = NOW()
	ahora := time.Now()
	sesion := &domain.SesionActiva{
		ID:              sesionID,
		UsuarioID:       usuario.ID,
		JtiAccess:       jtiAccess,
		JtiRefresh:      jtiRefresh,
		Activa:          true,
		FechaExpiracion: ahora.Add(refreshTTL),
	}
	if err := s.repo.CrearSesionYRegistrarAcceso(ctx, sesion, ahora); err != nil {
		return nil, fmt.Errorf("error al crear la sesión: %w", err)
	}

	// 8. Replicar la sesión en el almacén efímero: SET auth:session:<id> <payload> EX <ttl_refresh>
	s.replicarSesion(ctx, sesion)

	log.Printf("[AUTH] tokens issued | user=%s | session=%s | access_ttl=%s", usuario.EmailUsuario, sesionID, accessTTL)

	s.auditSvc.Registrar(ctx, ports.RegistrarAuditoriaInput{
		UsuarioID: usuario.ID,
		Accion:    ports.AccionVerificar2FA,
		Resultado: ports.ResultadoExito,
		Detalles:  map[string]any{"sesion_id": sesionID.String()},
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

	// 2. Localizar la fila de la sesión y verificar que siga viva y que el
	//    refresh sea el de esa sesión
	sesionID, err := uuid.Parse(claims.SesionID)
	if err != nil {
		return nil, fmt.Errorf("refresh token sin sesión asociada: %w", ports.ErrSesionRevocada)
	}
	sesion, err := s.repo.BuscarSesionPorID(ctx, sesionID)
	if err != nil {
		return nil, fmt.Errorf("sesión de refresh no válida o revocada: %w", err)
	}
	if !sesion.Activa || sesion.JtiRefresh != claims.ID || !time.Now().Before(sesion.FechaExpiracion) {
		return nil, fmt.Errorf("sesión de refresh no válida o revocada: %w", ports.ErrSesionRevocada)
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
	accessClaims.SesionID = sesionID.String()

	accessToken, err := crypto.FirmarToken(accessClaims, jwtSecret, accessTTL)
	if err != nil {
		return nil, fmt.Errorf("error al emitir access token: %w", err)
	}

	// 5. UPDATE sobre la fila existente (prohibido INSERT): el nuevo access
	//    reemplaza al anterior, que deja de ser válido
	if err := s.repo.RotarJtiAccess(ctx, sesionID, claims.ID, jtiAccess); err != nil {
		return nil, fmt.Errorf("sesión de refresh no válida o revocada: %w", err)
	}

	// 6. Actualizar payload y TTL en el almacén efímero
	sesion.JtiAccess = jtiAccess
	s.replicarSesion(ctx, sesion)

	log.Printf("[AUTH] refresh ok | user=%s | session=%s", usuario.EmailUsuario, sesionID)

	return &ports.TokenResult{
		AccessToken:  accessToken,
		RefreshToken: refreshToken, // El refresh token se mantiene igual
		ExpiresIn:    int64(accessTTL.Seconds()),
	}, nil
}

// ==========================================
// CerrarSesion
// ==========================================

// CerrarSesion invalida la sesión del refresh token: la borra del almacén
// efímero y la marca activa = false en PostgreSQL. El access token de esa
// sesión deja de funcionar en el mismo momento.
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

	sesionID, err := uuid.Parse(claims.SesionID)
	if err != nil {
		log.Printf("[AUTH] logout failed | reason=token_without_session")
		return fmt.Errorf("refresh token sin sesión asociada: %w", ports.ErrSesionRevocada)
	}

	// 2. Marcar activa = false sobre la fila única de la sesión
	if err := s.repo.DesactivarSesion(ctx, sesionID, claims.ID); err != nil {
		log.Printf("[AUTH] logout failed | session=%s | err=%v", sesionID, err)
		return fmt.Errorf("no se pudo cerrar la sesión: %w", err)
	}

	// 3. DEL auth:session:<session_id>
	if err := s.sesiones.eliminarSesiones(ctx, sesionID); err != nil {
		log.Printf("[AUTH] logout: sesión %s cerrada en PostgreSQL pero no en el almacén efímero: %v", sesionID, err)
		return fmt.Errorf("no se pudo cerrar la sesión por completo, intentá nuevamente")
	}

	log.Printf("[AUTH] logout done | session=%s", sesionID)

	// 4. Cortar el stream de eventos de esta sesión (GET /api/events)
	if usuarioID, err := uuid.Parse(claims.Subject); err == nil {
		s.bus.avisarLogout(ctx, usuarioID, sesionID)
	}

	usuarioID, _ := uuid.Parse(claims.Subject)
	s.auditSvc.Registrar(ctx, ports.RegistrarAuditoriaInput{
		UsuarioID: usuarioID,
		Accion:    ports.AccionLogout,
		Resultado: ports.ResultadoExito,
		Detalles: map[string]any{
			"sesion_id":     sesionID.String(),
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
	if err := revocarSesionesDeUsuario(ctx, s.repo, s.sesiones, s.bus, usuarioID); err != nil {
		return fmt.Errorf("error al revocar sesiones: %w", err)
	}
	return nil
}

// ==========================================
// Verificación de sesión (usada por los middlewares)
// ==========================================

// VerificarSesionPreAuth comprueba que el token temporal siga vigente (auth:pre2fa:<jti>).
func (s *authServiceImpl) VerificarSesionPreAuth(ctx context.Context, jti string) error {
	_, err := s.usuarioDePre2FA(ctx, jti)
	return err
}

// VerificarSesionAccess comprueba que la sesión esté activa y que el access
// token sea el último emitido para ella. Primero consulta el almacén efímero;
// si la sesión no está (se reinició, se desalojó o Redis no responde) consulta
// PostgreSQL, que es la fuente de verdad, y vuelve a replicarla.
func (s *authServiceImpl) VerificarSesionAccess(ctx context.Context, sesionID uuid.UUID, jtiAccess string) error {
	cacheada, err := s.sesiones.obtenerSesion(ctx, sesionID)
	switch {
	case err == nil:
		if cacheada.JtiAccess == jtiAccess && time.Now().Before(cacheada.FechaExpiracion) {
			return nil
		}
		return ports.ErrSesionRevocada
	case !errors.Is(err, errNoEncontrada):
		log.Printf("[AUTH] almacén de sesiones no disponible, se valida contra PostgreSQL: %v", err)
	}

	sesion, err := s.repo.BuscarSesionPorID(ctx, sesionID)
	if err != nil {
		return err
	}
	if !sesion.Activa || sesion.JtiAccess != jtiAccess || !time.Now().Before(sesion.FechaExpiracion) {
		return ports.ErrSesionRevocada
	}
	s.replicarSesion(ctx, sesion)
	return nil
}

// usuarioDePre2FA lee auth:pre2fa:<jti>. Los errores del almacén se loguean y
// al cliente le llega un mensaje genérico.
func (s *authServiceImpl) usuarioDePre2FA(ctx context.Context, jti string) (uuid.UUID, error) {
	usuarioID, err := s.sesiones.obtenerPre2FA(ctx, jti)
	if err == nil {
		return usuarioID, nil
	}
	if !errors.Is(err, errNoEncontrada) {
		log.Printf("[AUTH] error al leer la sesión temporal: %v", err)
	}
	return uuid.Nil, fmt.Errorf("sesión temporal inválida o expirada, iniciá sesión nuevamente: %w", ports.ErrSesionRevocada)
}

// replicarSesion guarda la sesión en el almacén efímero con TTL hasta que vence
// el refresh. Si falla no se corta el flujo: PostgreSQL es la fuente de verdad y
// VerificarSesionAccess la vuelve a replicar en el próximo request.
func (s *authServiceImpl) replicarSesion(ctx context.Context, sesion *domain.SesionActiva) {
	ttl := time.Until(sesion.FechaExpiracion)
	if ttl <= 0 {
		return
	}
	err := s.sesiones.guardarSesion(ctx, sesionCacheada{
		SesionID:        sesion.ID,
		UsuarioID:       sesion.UsuarioID,
		JtiAccess:       sesion.JtiAccess,
		JtiRefresh:      sesion.JtiRefresh,
		FechaExpiracion: sesion.FechaExpiracion,
	}, ttl)
	if err != nil {
		log.Printf("[AUTH] advertencia: no se pudo replicar la sesión %s en el almacén efímero: %v", sesion.ID, err)
	}
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
	if err := revocarSesionesDeUsuario(ctx, s.repo, s.sesiones, s.bus, usuario.ID); err != nil {
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
