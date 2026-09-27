package http

import (
	"net/http"
	"strings"

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
		SendError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Error al obtener el perfil.")
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
// @Failure      409 {object} ErrorResponse "Email ya en uso"
// @Router       /account/profile [put]
func (h *AccountHandler) ActualizarPerfil(c *gin.Context) {
	var input ports.ActualizarPerfilInput
	if err := c.ShouldBindJSON(&input); err != nil {
		SendError(c, http.StatusBadRequest, "INVALID_REQUEST", "Datos de perfil inválidos.")
		return
	}

	if input.EmailUsuario != "" {
		input.EmailUsuario = strings.ToLower(strings.TrimSpace(input.EmailUsuario))
	}

	userID := extraerUserID(c)
	usuario, err := h.service.ActualizarPerfil(c.Request.Context(), userID, input)
	if err != nil {
		SendError(c, http.StatusConflict, "PROFILE_UPDATE_CONFLICT", err.Error())
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
// @Description  Valida la contraseña actual y aplica la nueva. Reglas de complejidad: entre 8 y 12 caracteres, al menos una mayúscula, un número y un carácter especial (!@#$%^&*-_=+). Si la contraseña era temporal (`cambioContrasenaRequerido=true`), este cambio limpia ese flag y desbloquea el acceso al resto de la plataforma.
// @Tags         Mi cuenta
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body body ports.CambiarContrasenaInput true "Contraseña actual y nueva"
// @Success      200 {object} map[string]string
// @Failure      400 {object} ErrorResponse "Formato inválido, contraseña actual incorrecta, nueva igual a la actual o no cumple las reglas de complejidad"
// @Failure      401 {object} map[string]string
// @Failure      403 {object} ErrorResponse "PASSWORD_CHANGE_REQUIRED — solo este endpoint y logout son accesibles mientras el flag esté activo"
// @Router       /account/password [put]
func (h *AccountHandler) CambiarContrasena(c *gin.Context) {
	var input ports.CambiarContrasenaInput
	if err := c.ShouldBindJSON(&input); err != nil {
		SendError(c, http.StatusBadRequest, "INVALID_REQUEST", "Se requieren contrasenaActual y contrasenaNueva (entre 8 y 12 caracteres, con mayúscula, número y carácter especial).")
		return
	}

	userID := extraerUserID(c)
	if err := h.service.CambiarContrasena(c.Request.Context(), userID, input); err != nil {
		SendError(c, http.StatusBadRequest, "PASSWORD_CHANGE_FAILED", err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "Contraseña actualizada correctamente.",
	})
}

// ==========================================
// DELETE /api/account/sessions/current
