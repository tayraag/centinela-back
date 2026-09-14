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
	Contrasena string `json:"contrasena" binding:"required"`
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
// @Failure      400 {object} map[string]string "Formato de petición inválido"
// @Failure      401 {object} map[string]string "Credenciales incorrectas"
// @Router       /auth/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"errorCode": "INVALID_REQUEST",
			"message":   "El formato de la petición es incorrecto. Se requieren email y contrasena.",
		})
		return
	}

	result, err := h.service.Login(c.Request.Context(), req.Email, req.Contrasena)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"errorCode": "AUTH_FAILED",
			"message":   err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, result)
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
// @Failure      400 {object} map[string]string "TOTP ya vinculado o error interno"
// @Failure      401 {object} map[string]string "Token pre-auth inválido o expirado"
// @Router       /auth/2fa/qr [get]
func (h *AuthHandler) ObtenerQR(c *gin.Context) {
	jti, _ := c.Get(middleware.ContextKeyJTI)
	jtiStr, ok := jti.(string)
	if !ok || jtiStr == "" {
		c.JSON(http.StatusInternalServerError, gin.H{
			"errorCode": "INTERNAL_ERROR",
			"message":   "No se pudo obtener el identificador de sesión.",
		})
		return
	}

	result, err := h.service.ObtenerQRParaVinculacion(c.Request.Context(), jtiStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"errorCode": "QR_ERROR",
			"message":   err.Error(),
		})
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
// @Failure      400 {object} map[string]string "Código inválido (no tiene 6 dígitos)"
// @Failure      401 {object} map[string]string "Código TOTP incorrecto"
// @Router       /auth/2fa/verify [post]
func (h *AuthHandler) VerificarTotp(c *gin.Context) {
	var req verificarTotpRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"errorCode": "INVALID_REQUEST",
			"message":   "El código TOTP debe tener exactamente 6 dígitos.",
		})
		return
	}

	jti, _ := c.Get(middleware.ContextKeyJTI)
	jtiStr, ok := jti.(string)
	if !ok || jtiStr == "" {
		c.JSON(http.StatusInternalServerError, gin.H{
			"errorCode": "INTERNAL_ERROR",
			"message":   "No se pudo obtener el identificador de sesión.",
		})
		return
	}

	result, err := h.service.VerificarTotp(c.Request.Context(), jtiStr, req.Codigo)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"errorCode": "TOTP_FAILED",
			"message":   err.Error(),
		})
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
// @Failure      400 {object} map[string]string "Body inválido"
// @Failure      401 {object} map[string]string "Refresh token inválido o expirado"
// @Router       /auth/refresh [post]
func (h *AuthHandler) RefrescarToken(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"errorCode": "INVALID_REQUEST",
			"message":   "Se requiere el campo refreshToken.",
		})
		return
	}

	result, err := h.service.RefrescarToken(c.Request.Context(), req.RefreshToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"errorCode": "REFRESH_FAILED",
			"message":   err.Error(),
		})
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
// @Failure      400 {object} map[string]string "UUID inválido"
// @Failure      401 {object} map[string]string "No autenticado"
// @Failure      403 {object} map[string]string "Sin permisos o reset fallido"
// @Router       /auth/2fa/relink [post]
func (h *AuthHandler) SolicitarRevinculacion(c *gin.Context) {
	var req relinkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"errorCode": "INVALID_REQUEST",
			"message":   "Se requiere un usuarioId válido (UUID).",
		})
		return
	}

	targetID, err := uuid.Parse(req.UsuarioID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"errorCode": "INVALID_UUID",
			"message":   "El usuarioId no es un UUID válido.",
		})
		return
	}

	jti, _ := c.Get(middleware.ContextKeyJTI)
	jtiStr, ok := jti.(string)
	if !ok || jtiStr == "" {
		c.JSON(http.StatusInternalServerError, gin.H{
			"errorCode": "INTERNAL_ERROR",
			"message":   "No se pudo obtener el identificador de sesión.",
		})
		return
	}

	if err := h.service.SolicitarRevinculacion(c.Request.Context(), jtiStr, targetID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{
			"errorCode": "RELINK_FAILED",
			"message":   err.Error(),
		})
		return
	}

	c.Status(http.StatusNoContent)
}