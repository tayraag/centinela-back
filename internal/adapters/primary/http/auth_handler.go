package http

import (
	"net/http"

	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// AuthHandler maneja los endpoints HTTP del flujo de autenticación y 2FA.
type AuthHandler struct {
	service ports.AuthService
}

// NewAuthHandler crea un nuevo AuthHandler con el servicio de autenticación inyectado.
func NewAuthHandler(service ports.AuthService) *AuthHandler {
	return &AuthHandler{service: service}
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

// LogoutRequest define el body esperado para cerrar sesión.
type LogoutRequest struct {
	RefreshToken string `json:"refreshToken" binding:"required"`
}

// Logout invalida la sesión del usuario a partir de su refresh token.
//
// @Summary      Cerrar sesión (logout)
// @Description  Invalida la sesión asociada al refresh token recibido. El frontend debe descartar los tokens locales. Responde 204 sin body.
// @Tags         Autenticación
// @Accept       json
// @Produce      json
// @Param        body body LogoutRequest true "Token de refresco de la sesión a cerrar"
// @Success      204 "Sin contenido"
// @Failure      400 {object} ErrorResponse "Formato de petición inválido"
// @Failure      401 {object} ErrorResponse "Refresh token inválido o sesión ya cerrada"
// @Router       /auth/logout [post]
func (h *AuthHandler) Logout(c *gin.Context) {
	var req LogoutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		SendError(c, http.StatusBadRequest, "INVALID_REQUEST", "El formato de la petición es incorrecto. Se requiere refreshToken.")
		return
	}

	if err := h.service.CerrarSesion(c.Request.Context(), req.RefreshToken); err != nil {
		SendError(c, http.StatusUnauthorized, "AUTH_FAILED", "sesión inválida o ya cerrada")
		return
	}

	c.Status(http.StatusNoContent)
}

// ==========================================
// GET /api/auth/2fa/qr
// ==========================================

// ObtenerQR genera el QR de vinculación TOTP para el usuario.
//
// @Summary      Obtener QR de vinculación 2FA
// @Description  Genera el secreto TOTP, lo cifra y devuelve el QR en Base64 más el secreto manual. Solo disponible con JWT temporal (pre-auth). Llamar únicamente si `totpVinculado` es false.
// @Tags         Autenticación 2FA
// @Produce      json
// @Security     BearerPreAuth
// @Success      200 {object} ports.QRResult
// @Failure      400 {object} ErrorResponse "TOTP ya vinculado o error interno"
// @Failure      401 {object} ErrorResponse "Token pre-auth inválido o expirado"
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
		SendError(c, http.StatusBadRequest, "QR_ERROR", err.Error())
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
// @Failure      401 {object} ErrorResponse "Código TOTP incorrecto"
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

	c.JSON(http.StatusOK, result)
}

// ==========================================
// POST /api/auth/refresh
// ==========================================

// refreshRequest define el body para la renovación de tokens.
type refreshRequest struct {
	RefreshToken string `json:"refreshToken" binding:"required"`
}

// RefrescarToken valida un refresh token y emite un nuevo access token.
//
// @Summary      Renovar access token
// @Description  Recibe un refresh token válido y emite un nuevo access token (8h). El refresh token no cambia.
// @Tags         Autenticación
// @Accept       json
// @Produce      json
// @Param        body body refreshRequest true "Refresh token"
// @Success      200 {object} ports.TokenResult
// @Failure      400 {object} ErrorResponse "Body inválido"
// @Failure      401 {object} ErrorResponse "Refresh token inválido o expirado"
// @Router       /auth/refresh [post]
func (h *AuthHandler) RefrescarToken(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		SendError(c, http.StatusBadRequest, "INVALID_REQUEST", "Se requiere el campo refreshToken.")
		return
	}

	result, err := h.service.RefrescarToken(c.Request.Context(), req.RefreshToken)
	if err != nil {
		SendError(c, http.StatusUnauthorized, "REFRESH_FAILED", err.Error())
		return
	}

	c.JSON(http.StatusOK, result)
}

// ==========================================
// POST /api/auth/2fa/relink
// ==========================================

// relinkRequest define el body para el reset de 2FA de un usuario.
type relinkRequest struct {
	UsuarioID string `json:"usuarioId" binding:"required,uuid"`
}

// SolicitarRevinculacion permite a un administrador resetear el 2FA de otro usuario.
//
// @Summary      Admin: resetear 2FA de un usuario (endpoint legacy)
// @Description  Invalida el secreto TOTP del usuario indicado. En su próximo login, el usuario deberá escanear un nuevo QR. Usar en su lugar `POST /users/{id}/2fa/reset`.
// @Tags         Autenticación 2FA
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body body relinkRequest true "UUID del usuario a resetear"
// @Success      204 "Sin contenido"
// @Failure      400 {object} ErrorResponse "UUID inválido"
// @Failure      401 {object} ErrorResponse "No autenticado"
// @Failure      403 {object} ErrorResponse "Sin permisos o reset fallido"
// @Router       /auth/2fa/relink [post]
func (h *AuthHandler) SolicitarRevinculacion(c *gin.Context) {
	var req relinkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		SendError(c, http.StatusBadRequest, "INVALID_REQUEST", "Se requiere un usuarioId válido (UUID).")
		return
	}

	targetID, err := uuid.Parse(req.UsuarioID)
	if err != nil {
		SendError(c, http.StatusBadRequest, "INVALID_UUID", "El usuarioId no es un UUID válido.")
		return
	}

	jti, _ := c.Get(middleware.ContextKeyJTI)
	jtiStr, ok := jti.(string)
	if !ok || jtiStr == "" {
		SendError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo obtener el identificador de sesión.")
		return
	}

	if err := h.service.SolicitarRevinculacion(c.Request.Context(), jtiStr, targetID); err != nil {
		SendError(c, http.StatusForbidden, "RELINK_FAILED", err.Error())
		return
	}

	c.Status(http.StatusNoContent)
}
