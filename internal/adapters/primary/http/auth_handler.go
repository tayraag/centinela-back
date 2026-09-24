package http

import (
	"net/http"
	"os"
	"strings"

	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
)

// AuthHandler maneja los endpoints HTTP del flujo de autenticación y 2FA.
type AuthHandler struct {
	service ports.AuthService
}

// NewAuthHandler crea un nuevo AuthHandler con el servicio de autenticación inyectado.
func NewAuthHandler(service ports.AuthService) *AuthHandler {
	return &AuthHandler{service: service}
}

// setRefreshCookie encapsula la lógica para emitir o borrar la cookie segura del refresh token.
func setRefreshCookie(c *gin.Context, token string, maxAge int) {
	// En producción usar Secure=true. En desarrollo local puede ser false mediante variable de entorno.
	secure := true
	if os.Getenv("COOKIE_SECURE") == "false" {
		secure = false
	}
	
	// Previene envíos cross-site (protección CSRF combinada con XSS protection del HttpOnly)
	c.SameSite(http.SameSiteStrictMode)
	
	// c.SetCookie(name, value, maxAge, path, domain, secure, httpOnly)
	c.SetCookie("centinela_refresh", token, maxAge, "/api/auth", "", secure, true)
}

// ==========================================
// POST /api/auth/login
// ==========================================

// loginRequest define el body esperado para el endpoint de login.
type LoginRequest struct {
	Email      string `json:"email" binding:"required,email"`
	Contrasena string `json:"password" binding:"required"`
}

// Login valida credenciales de email+contraseña y retorna un JWT temporal pre-2FA.
//
// @Summary      Login de usuario
// @Description  Valida email y contraseña. Si son correctas emite un JWT temporal (5 min) para continuar el flujo 2FA. El campo `totpVinculado` indica si el usuario debe escanear el QR (false) o ingresar el código TOTP (true). El campo `cambioContrasenaRequerido` indica si la contraseña es temporal y debe cambiarse.
// @Tags         Autenticación
// @Accept       json
// @Produce      json
// @Param        body body LoginRequest true "Credenciales de acceso"
// @Success      200 {object} ports.LoginResult
// @Failure      400 {object} ErrorResponse "Formato de petición inválido"
// @Failure      401 {object} ErrorResponse "Credenciales incorrectas"
// @Router       /auth/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		SendError(c, http.StatusBadRequest, "INVALID_REQUEST", "El formato de la petición es incorrecto. Se requieren email y password.")
		return
	}

	// Normalizar email a minúsculas para login case-insensitive
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	result, err := h.service.Login(c.Request.Context(), req.Email, req.Contrasena)
	if err != nil {
		SendError(c, http.StatusUnauthorized, "AUTH_FAILED", err.Error())
		return
	}

	c.JSON(http.StatusOK, result)
}

// ==========================================
// POST /api/auth/logout
// ==========================================

// Logout invalida la sesión del usuario a partir de su refresh token en la cookie.
//
// @Summary      Cerrar sesión (logout)
// @Description  Invalida la sesión asociada al refresh token en la cookie `centinela_refresh`. Emite la eliminación de la cookie y responde 204.
// @Tags         Autenticación
// @Produce      json
// @Success      204 "Sin contenido"
// @Failure      401 {object} ErrorResponse "Refresh token inválido, sesión ya cerrada o cookie ausente"
// @Router       /auth/logout [post]
func (h *AuthHandler) Logout(c *gin.Context) {
	refreshToken, err := c.Cookie("centinela_refresh")
	if err != nil || refreshToken == "" {
		SendError(c, http.StatusUnauthorized, "REFRESH_COOKIE_MISSING", "Cookie de refresh token no provista.")
		return
	}

	jti, _ := c.Get(middleware.ContextKeyJTI)
	jtiStr, _ := jti.(string)

	if err := h.service.CerrarSesion(c.Request.Context(), refreshToken, jtiStr); err != nil {
		SendError(c, http.StatusUnauthorized, "AUTH_FAILED", "sesión inválida o ya cerrada")
		return
	}

	// Borrar la cookie emitiendo Max-Age=-1
	setRefreshCookie(c, "", -1)

	c.Status(http.StatusNoContent)
}

// ==========================================
// GET /api/auth/2fa/qr
// ==========================================

// ObtenerQR genera el QR de vinculación TOTP para el usuario.
//
// @Summary      Obtener QR de vinculación 2FA
// @Description  Genera el secreto TOTP, lo cifra y devuelve el QR en Base64 más el secreto manual. Solo disponible con JWT temporal (pre-auth). Llamar únicamente si `totpVinculado` es false. Si el usuario ya tiene 2FA activo, se rechaza con 409.
// @Tags         Autenticación 2FA
// @Produce      json
// @Security     BearerPreAuth
// @Success      200 {object} ports.QRResult
// @Failure      400 {object} ErrorResponse "Error interno al generar el QR"
// @Failure      401 {object} ErrorResponse "Token pre-auth inválido o expirado"
// @Failure      409 {object} ErrorResponse "El 2FA ya está activo, requiere reset administrativo"
// @Router       /auth/2fa/qr [get]
func (h *AuthHandler) ObtenerQR(c *gin.Context) {
	jti, _ := c.Get(middleware.ContextKeyJTI)
	jtiStr, ok := jti.(string)
	if !ok || jtiStr == "" {
		SendError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo obtener el identificador de sesión.")
		return
	}

	result, err := h.service.ObtenerQRParaVinculacion(c.Request.Context(), jtiStr)
	if err != nil {
		if strings.Contains(err.Error(), "el doble factor ya está activo") {
			SendError(c, http.StatusConflict, "TWO_FACTOR_ALREADY_ENABLED", err.Error())
		} else {
			SendError(c, http.StatusBadRequest, "QR_ERROR", err.Error())
		}
		return
	}

	c.JSON(http.StatusOK, result)
}

// ==========================================
// POST /api/auth/2fa/verify
// ==========================================

// verificarTotpRequest define el body para la verificación de código TOTP.
type verificarTotpRequest struct {
	Codigo string `json:"codigo" binding:"required,len=6"`
}

// VerificarTotp valida el código TOTP y emite access + refresh tokens.
//
// @Summary      Verificar código TOTP → obtener tokens definitivos
// @Description  Recibe el código de 6 dígitos del autenticador. Si es correcto emite el `accessToken` (8h) y el `refreshToken` (30 días) con 2FA completado. Funciona tanto para la primera vinculación como para logins posteriores.
// @Tags         Autenticación 2FA
// @Accept       json
// @Produce      json
// @Security     BearerPreAuth
// @Param        body body verificarTotpRequest true "Código TOTP de 6 dígitos"
// @Success      200 {object} ports.TokenResult
// @Failure      400 {object} ErrorResponse "Código inválido (no tiene 6 dígitos)"
// @Failure      401 {object} ErrorResponse "Código TOTP incorrecto o ya utilizado (anti-replay)"
// @Router       /auth/2fa/verify [post]
func (h *AuthHandler) VerificarTotp(c *gin.Context) {
	var req verificarTotpRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		SendError(c, http.StatusBadRequest, "INVALID_REQUEST", "El código TOTP debe tener exactamente 6 dígitos.")
		return
	}

	jti, _ := c.Get(middleware.ContextKeyJTI)
	jtiStr, ok := jti.(string)
	if !ok || jtiStr == "" {
		SendError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo obtener el identificador de sesión.")
		return
	}

	result, err := h.service.VerificarTotp(c.Request.Context(), jtiStr, req.Codigo)
	if err != nil {
		SendError(c, http.StatusUnauthorized, "TOTP_FAILED", err.Error())
		return
	}

	// Emitir la cookie segura con el refresh token. Tiempo de vida: 7 días.
	setRefreshCookie(c, result.RefreshToken, 7*24*3600)

	c.JSON(http.StatusOK, result)
}

// ==========================================
// POST /api/auth/refresh
// ==========================================

// RefrescarToken valida un refresh token de la cookie y emite un nuevo access token.
//
// @Summary      Renovar access token
// @Description  Lee el refresh token desde la cookie HTTP-Only, valida la sesión y emite un nuevo access token. Rota la cookie emitiendo un nuevo refresh token.
// @Tags         Autenticación
// @Produce      json
// @Success      200 {object} ports.TokenResult
// @Failure      401 {object} ErrorResponse "Cookie no provista, o refresh token inválido/expirado"
// @Router       /auth/refresh [post]
func (h *AuthHandler) RefrescarToken(c *gin.Context) {
	refreshToken, err := c.Cookie("centinela_refresh")
	if err != nil || refreshToken == "" {
		SendError(c, http.StatusUnauthorized, "REFRESH_COOKIE_MISSING", "Cookie de refresh token no provista.")
		return
	}

	result, err := h.service.RefrescarToken(c.Request.Context(), refreshToken)
	if err != nil {
		// Mensaje fijo: el error del servicio puede traer el detalle interno de la
		// librería de JWT (por ejemplo "token is malformed"), que no es asunto del cliente.
		SendError(c, http.StatusUnauthorized, "REFRESH_FAILED", "El refresh token es inválido o expiró. Iniciá sesión nuevamente.")
		return
	}

	// Rotar la cookie con el nuevo refresh token emitido
	setRefreshCookie(c, result.RefreshToken, 7*24*3600)

	c.JSON(http.StatusOK, result)
}

// ==========================================
// Recuperación de Contraseña
// ==========================================

type solicitarRecuperacionRequest struct {
	Email string `json:"email" binding:"required,email"`
}

// SolicitarRecuperacion inicia el flujo enviando un código temporal al correo del usuario.
//
// @Summary      Solicitar recuperación de contraseña
// @Description  Genera un código de 6 dígitos válido por 15 minutos y lo envía al correo del usuario. Retorna 200 OK incluso si el correo no existe para evitar enumeración.
// @Tags         Autenticación
// @Accept       json
// @Produce      json
// @Param        body body solicitarRecuperacionRequest true "Email del usuario"
// @Success      200 {object} map[string]string
// @Failure      400 {object} ErrorResponse "Email inválido"
// @Router       /auth/password/forgot [post]
func (h *AuthHandler) SolicitarRecuperacion(c *gin.Context) {
	var req solicitarRecuperacionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		SendError(c, http.StatusBadRequest, "INVALID_REQUEST", "Email inválido o faltante.")
		return
	}

	if err := h.service.SolicitarRecuperacionContrasena(c.Request.Context(), req.Email); err != nil {
		SendError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Error al procesar la solicitud.")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Si el correo está registrado, recibirás un código de recuperación en unos minutos.",
	})
}

type confirmarRecuperacionRequest struct {
	Email           string `json:"email" binding:"required,email"`
	Codigo          string `json:"codigo" binding:"required,len=6"`
	NuevaContrasena string `json:"nuevaContrasena" binding:"required,min=8,max=12"`
}

// ConfirmarRecuperacion valida el código de recuperación y establece la nueva contraseña.
//
// @Summary      Confirmar recuperación de contraseña
// @Description  Valida el código de 6 dígitos enviado por email y establece la nueva contraseña (8-12 chars, mayúscula, número, especial). Invalida el código tras el uso o tras 3 intentos fallidos. La nueva contraseña no puede coincidir con la anterior.
// @Tags         Autenticación
// @Accept       json
// @Produce      json
// @Param        body body confirmarRecuperacionRequest true "Datos de recuperación"
// @Success      200 {object} map[string]string
// @Failure      400 {object} ErrorResponse "Datos inválidos, código incorrecto, demasiados intentos, contraseña débil o igual a la actual"
// @Router       /auth/password/reset [post]
func (h *AuthHandler) ConfirmarRecuperacion(c *gin.Context) {
	var req confirmarRecuperacionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		SendError(c, http.StatusBadRequest, "INVALID_REQUEST", "Formato inválido. Se requiere email, código de 6 dígitos y nueva contraseña válida.")
		return
	}

	err := h.service.ConfirmarRecuperacionContrasena(c.Request.Context(), req.Email, req.Codigo, req.NuevaContrasena)
	if err != nil {
		SendError(c, http.StatusBadRequest, "RESET_FAILED", err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Contraseña actualizada exitosamente. Ya puedes iniciar sesión.",
	})
}
