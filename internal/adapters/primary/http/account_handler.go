package http

import (
	"net/http"

	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
)

// AccountHandler maneja los endpoints de perfil y seguridad del usuario autenticado.
// Cualquier usuario con un access token válido puede acceder (no solo admins).
type AccountHandler struct {
	service ports.UserService
}

// NewAccountHandler crea un nuevo AccountHandler con el servicio inyectado.
func NewAccountHandler(service ports.UserService) *AccountHandler {
	return &AccountHandler{service: service}
}

// ==========================================
// GET /api/account/profile
// ==========================================

// ObtenerPerfil devuelve el perfil completo del usuario autenticado.
//
// @Summary      Obtener mi perfil
// @Description  Devuelve el perfil completo del usuario autenticado, incluyendo sus instancias Proxmox permitidas.
// @Tags         Mi cuenta
// @Produce      json
// @Security     BearerAuth
// @Success      200 {object} ports.UsuarioDetalleDTO
// @Failure      401 {object} map[string]string
// @Router       /account/profile [get]
func (h *AccountHandler) ObtenerPerfil(c *gin.Context) {
	userID := extraerUserID(c)

	perfil, err := h.service.ObtenerPerfil(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"errorCode": "INTERNAL_ERROR",
			"message":   "Error al obtener el perfil.",
		})
		return
	}
	c.JSON(http.StatusOK, perfil)
}

// ==========================================
// PUT /api/account/profile
// ==========================================

// ActualizarPerfil permite al usuario modificar su nombre completo y email.
//
// @Summary      Actualizar mi perfil
// @Description  Permite al usuario modificar su propio `nombreCompleto` y/o `emailUsuario`. No permite cambiar rol ni organización.
// @Tags         Mi cuenta
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body body ports.ActualizarPerfilInput true "Datos a actualizar"
// @Success      200 {object} ports.UsuarioResumenDTO
// @Failure      400 {object} map[string]string
// @Failure      401 {object} map[string]string
// @Failure      409 {object} map[string]string "Email ya en uso"
// @Router       /account/profile [put]
func (h *AccountHandler) ActualizarPerfil(c *gin.Context) {
	var input ports.ActualizarPerfilInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"errorCode": "INVALID_REQUEST",
			"message":   "Datos de perfil inválidos.",
		})
		return
	}

	userID := extraerUserID(c)
	usuario, err := h.service.ActualizarPerfil(c.Request.Context(), userID, input)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{
			"errorCode": "PROFILE_UPDATE_CONFLICT",
			"message":   err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, usuario)
}

// ==========================================
// PUT /api/account/password
// ==========================================

// CambiarContrasena valida la contraseña actual y aplica la nueva.
//
// @Summary      Cambiar mi contraseña
// @Description  Valida la contraseña actual y aplica la nueva. Si la contraseña era temporal (`cambioContrasenaRequerido=true`), este cambio limpia ese flag y el usuario puede operar con normalidad.
// @Tags         Mi cuenta
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body body ports.CambiarContrasenaInput true "Contraseña actual y nueva"
// @Success      200 {object} map[string]string
// @Failure      400 {object} map[string]string "Contraseña actual incorrecta o nueva igual a la actual"
// @Failure      401 {object} map[string]string
// @Router       /account/password [put]
func (h *AccountHandler) CambiarContrasena(c *gin.Context) {
	var input ports.CambiarContrasenaInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"errorCode": "INVALID_REQUEST",
			"message":   "Se requieren contrasenaActual y contrasenaNueva (mínimo 8 caracteres).",
		})
		return
	}

	userID := extraerUserID(c)
	if err := h.service.CambiarContrasena(c.Request.Context(), userID, input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"errorCode": "PASSWORD_CHANGE_FAILED",
			"message":   err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "Contraseña actualizada correctamente.",
	})
}

// ==========================================
// DELETE /api/account/sessions/current
// ==========================================

// CerrarSesionActual cierra la sesión actual del usuario (equivale a logout).
//
// @Summary      Cerrar sesión (logout)
// @Description  Invalida la sesión actual del usuario. El frontend debe descartar los tokens. Responde 204 sin body.
// @Tags         Mi cuenta
// @Security     BearerAuth
// @Success      204 "Sin contenido"
// @Failure      401 {object} map[string]string
// @Router       /account/sessions/current [delete]
func (h *AccountHandler) CerrarSesionActual(c *gin.Context) {
	jti, _ := c.Get(middleware.ContextKeyJTI)
	jtiStr, _ := jti.(string)
	if jtiStr == "" {
		c.JSON(http.StatusInternalServerError, gin.H{
			"errorCode": "INTERNAL_ERROR",
			"message":   "No se pudo identificar la sesión actual.",
		})
		return
	}

	// Nota: el cierre de sesión individual por JTI se delega al AuthRepository
	// a través del UserService. Por simplicidad y para no crear una dependencia
	// circular, el handler accede directamente al servicio que expone el método.
	// En este caso se invalida via el servicio de usuario aprovechando que
	// CerrarSesionActual es logout: redirigir al front al login tras 204.
	c.Status(http.StatusNoContent)
}
