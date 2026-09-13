package http

import (
	"net/http"
	"time"

	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// UserHandler maneja los endpoints HTTP de gestión de usuarios (acceso solo ADMIN).
type UserHandler struct {
	service ports.UserService
}

// NewUserHandler crea un nuevo UserHandler con el servicio inyectado.
func NewUserHandler(service ports.UserService) *UserHandler {
	return &UserHandler{service: service}
}

// rolesDisponibles es la lista hardcodeada de roles del sistema.
// Los roles son constantes del RF y no requieren tabla en BD.
var rolesDisponibles = []ports.RolDTO{
	{Valor: "ADMIN", Descripcion: "Acceso total al sistema y gestión de usuarios."},
	{Valor: "OPERATOR", Descripcion: "Acceso restringido a instancias asignadas por un administrador."},
}

// ==========================================
// GET /api/roles
// ==========================================

// ObtenerRoles devuelve la lista de roles disponibles en el sistema.
// Usado por el front para llenar el selector al crear/editar usuarios.
func (h *UserHandler) ObtenerRoles(c *gin.Context) {
	c.JSON(http.StatusOK, rolesDisponibles)
}

// ==========================================
// GET /api/users
// ==========================================

// ListarUsuarios devuelve el listado de usuarios con estadísticas y filtros opcionales.
// Query params: ?rol=ADMIN|OPERATOR, ?activo=true|false, ?buscar=texto
func (h *UserHandler) ListarUsuarios(c *gin.Context) {
	orgID := extraerOrgID(c)
	solicitanteID := extraerUserID(c)

	filtros := ports.FiltrosUsuario{
		Rol:    c.Query("rol"),
		Buscar: c.Query("buscar"),
	}
	if activoStr := c.Query("activo"); activoStr != "" {
		activo := activoStr == "true"
		filtros.Activo = &activo
	}

	resultado, err := h.service.ListarUsuarios(c.Request.Context(), orgID, solicitanteID, filtros)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"errorCode": "INTERNAL_ERROR",
			"message":   "Error al listar usuarios.",
		})
		return
	}
	c.JSON(http.StatusOK, resultado)
}

// ==========================================
// POST /api/users
// ==========================================

// CrearUsuario crea un nuevo usuario en la organización del admin autenticado.
// Devuelve la contraseña temporal en la respuesta (Plan A, sin SMTP).
func (h *UserHandler) CrearUsuario(c *gin.Context) {
	var input ports.CrearUsuarioInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"errorCode": "INVALID_REQUEST",
			"message":   "Datos inválidos: se requieren nombreCompleto, nombreUsuario, emailUsuario y rol (ADMIN|OPERATOR).",
		})
		return
	}

	orgID := extraerOrgID(c)
	resultado, err := h.service.CrearUsuario(c.Request.Context(), orgID, input)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{
			"errorCode": "USER_CONFLICT",
			"message":   err.Error(),
		})
		return
	}
	c.JSON(http.StatusCreated, resultado)
}

// ==========================================
// GET /api/users/:id
// ==========================================

// ObtenerUsuario devuelve el perfil completo de un usuario con sus instancias asignadas.
func (h *UserHandler) ObtenerUsuario(c *gin.Context) {
	id, ok := parsearUUID(c, "id")
	if !ok {
		return
	}
	orgID := extraerOrgID(c)

	detalle, err := h.service.ObtenerUsuario(c.Request.Context(), id, orgID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"errorCode": "USER_NOT_FOUND",
			"message":   "Usuario no encontrado.",
		})
		return
	}
	c.JSON(http.StatusOK, detalle)
}

// ==========================================
// PUT /api/users/:id
// ==========================================

// ActualizarUsuario actualiza parcialmente los datos de un usuario.
// Solo actualiza los campos presentes en el body (semántica PATCH).
func (h *UserHandler) ActualizarUsuario(c *gin.Context) {
	id, ok := parsearUUID(c, "id")
	if !ok {
		return
	}
	orgID := extraerOrgID(c)

	var input ports.ActualizarUsuarioInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"errorCode": "INVALID_REQUEST",
			"message":   "Datos de actualización inválidos.",
		})
		return
	}

	usuario, err := h.service.ActualizarUsuario(c.Request.Context(), id, orgID, input)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{
			"errorCode": "UPDATE_CONFLICT",
			"message":   err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, usuario)
}

// ==========================================
// DELETE /api/users/:id
// ==========================================

// EliminarUsuario realiza un soft-delete del usuario y cierra todas sus sesiones.
func (h *UserHandler) EliminarUsuario(c *gin.Context) {
	id, ok := parsearUUID(c, "id")
	if !ok {
		return
	}
	orgID := extraerOrgID(c)

	// Prevenir que el admin se elimine a sí mismo
	if id == extraerUserID(c) {
		c.JSON(http.StatusBadRequest, gin.H{
			"errorCode": "SELF_DELETE_NOT_ALLOWED",
			"message":   "No puede eliminar su propio usuario.",
		})
		return
	}

	if err := h.service.EliminarUsuario(c.Request.Context(), id, orgID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"errorCode": "USER_NOT_FOUND",
			"message":   "Usuario no encontrado.",
		})
		return
	}
	c.Status(http.StatusNoContent)
}

// ==========================================
// PUT /api/users/:id/instances
// ==========================================

// asignarPermisosRequest define el body para la asignación de permisos de instancia.
type asignarPermisosRequest struct {
	Vmids []int `json:"vmids" binding:"required"`
}

// AsignarPermisos reemplaza todos los permisos de instancia de un usuario operador.
func (h *UserHandler) AsignarPermisos(c *gin.Context) {
	id, ok := parsearUUID(c, "id")
	if !ok {
		return
	}
	orgID := extraerOrgID(c)

	var req asignarPermisosRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"errorCode": "INVALID_REQUEST",
			"message":   "Se requiere el campo vmids (array de enteros).",
		})
		return
	}

	if err := h.service.AsignarPermisos(c.Request.Context(), id, orgID, req.Vmids); err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"errorCode": "USER_NOT_FOUND",
			"message":   err.Error(),
		})
		return
	}
	c.Status(http.StatusNoContent)
}

// ==========================================
// GET /api/users/:id/activity
// ==========================================

// ListarActividad devuelve el historial de acciones auditadas de un usuario.
// Query params: ?accion=START|STOP|..., ?desde=2026-01-01, ?hasta=2026-12-31
func (h *UserHandler) ListarActividad(c *gin.Context) {
	id, ok := parsearUUID(c, "id")
	if !ok {
		return
	}
	orgID := extraerOrgID(c)

	filtros := ports.FiltrosActividad{
		Accion: c.Query("accion"),
	}
	if desdeStr := c.Query("desde"); desdeStr != "" {
		if t, err := time.Parse(time.DateOnly, desdeStr); err == nil {
			filtros.Desde = &t
		}
	}
	if hastaStr := c.Query("hasta"); hastaStr != "" {
		if t, err := time.Parse(time.DateOnly, hastaStr); err == nil {
			filtros.Hasta = &t
		}
	}

	actividad, err := h.service.ListarActividad(c.Request.Context(), id, orgID, filtros)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"errorCode": "USER_NOT_FOUND",
			"message":   err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, actividad)
}

// ==========================================
// POST /api/users/:id/2fa/reset
// ==========================================

// ResetearTotp invalida el 2FA del usuario, forzando revinculación en el próximo login.
func (h *UserHandler) ResetearTotp(c *gin.Context) {
	id, ok := parsearUUID(c, "id")
	if !ok {
		return
	}
	orgID := extraerOrgID(c)

	if err := h.service.ResetearTotp(c.Request.Context(), id, orgID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"errorCode": "USER_NOT_FOUND",
			"message":   err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "TOTP reseteado. El usuario deberá vincularlo en su próximo acceso.",
	})
}

// ==========================================
// POST /api/users/:id/password/reset
// ==========================================

// ResetearContrasena genera una nueva contraseña temporal para el usuario.
func (h *UserHandler) ResetearContrasena(c *gin.Context) {
	id, ok := parsearUUID(c, "id")
	if !ok {
		return
	}
	orgID := extraerOrgID(c)

	contrasenaTemp, err := h.service.ResetearContrasena(c.Request.Context(), id, orgID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"errorCode": "USER_NOT_FOUND",
			"message":   err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message":        "Contraseña restablecida. El usuario deberá cambiarla en su próximo acceso.",
		"contrasenaTemp": contrasenaTemp,
	})
}

// ==========================================
// Helpers del handler
// ==========================================

// extraerOrgID extrae el OrganizacionID del contexto Gin (inyectado por el middleware).
func extraerOrgID(c *gin.Context) uuid.UUID {
	val, _ := c.Get(middleware.ContextKeyOrgID)
	id, _ := val.(uuid.UUID)
	return id
}

// extraerUserID extrae el UserID del contexto Gin (inyectado por el middleware).
func extraerUserID(c *gin.Context) uuid.UUID {
	val, _ := c.Get(middleware.ContextKeyUserID)
	idStr, _ := val.(string)
	id, _ := uuid.Parse(idStr)
	return id
}

// parsearUUID parsea el parámetro de ruta como UUID y responde 400 si es inválido.
func parsearUUID(c *gin.Context, param string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(param))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"errorCode": "INVALID_UUID",
			"message":   "El identificador proporcionado no es válido.",
		})
		return uuid.Nil, false
	}
	return id, true
}
