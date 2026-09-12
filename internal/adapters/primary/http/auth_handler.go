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
type loginRequest struct {
	Email     string `json:"email" binding:"required,email"`
	Contrasena string `json:"contrasena" binding:"required"`
}

// Login valida credenciales de email+contraseña y retorna un JWT temporal pre-2FA.
// @Summary      Login de usuario
// @Description  Recibe email y contraseña, retorna un JWT temporal para el flujo 2FA.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body body loginRequest true "Credenciales"
// @Success      200 {object} ports.LoginResult
// @Failure      400 {object} map[string]string
// @Failure      401 {object} map[string]string
// @Router       /auth/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
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
// Requiere un JWT temporal válido (middleware RequirePreAuth).
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
// Requiere un JWT temporal válido (middleware RequirePreAuth).
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
// Requiere access token con rol ADMIN (middlewares RequireAuth + RequireRole).
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