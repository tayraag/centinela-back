package http

import (
	"net/http"
	"strings"
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
//
// @Summary      Listar roles disponibles
// @Description  Devuelve los roles definidos en el sistema (ADMIN y OPERATOR). Útil para poblar selectores en el frontend al crear o editar usuarios.
// @Tags         Usuarios (Admin)
// @Produce      json
// @Security     BearerAuth
// @Success      200 {array} ports.RolDTO
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Router       /roles [get]
func (h *UserHandler) ObtenerRoles(c *gin.Context) {
	c.JSON(http.StatusOK, rolesDisponibles)
}

// ==========================================
// GET /api/users
// ==========================================

// ListarUsuarios devuelve el listado de usuarios con estadísticas y filtros opcionales.
//
// @Summary      Listar usuarios de la organización
// @Description  Devuelve todos los usuarios de la organización del admin autenticado, con un resumen (`summary`) de totales por rol. Soporta filtros opcionales: `?rol=ADMIN|OPERATOR`, `?activo=true|false`, `?buscar=texto` (nombre o email).
// @Tags         Usuarios (Admin)
// @Produce      json
// @Security     BearerAuth
// @Param        rol     query string  false "Filtrar por rol: ADMIN o OPERATOR"
// @Param        activo  query boolean false "Filtrar por estado: true o false"
// @Param        buscar  query string  false "Buscar por nombre o email (case-insensitive)"
// @Success      200 {object} ports.ListaUsuariosResult
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Router       /admin/users [get]
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
		SendError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Error al listar usuarios.")
		return
	}
	c.JSON(http.StatusOK, resultado)
}

// ==========================================
// POST /api/users
// ==========================================

// CrearUsuario crea un nuevo usuario en la organización del admin autenticado.
//
// @Summary      Crear usuario
// @Description  Crea un nuevo usuario con contraseña temporal generada automáticamente. La contraseña se envía al usuario por email. El usuario deberá cambiarla en su primer login.
// @Tags         Usuarios (Admin)
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body body ports.CrearUsuarioInput true "Datos del nuevo usuario"
// @Success      201 {object} ports.CrearUsuarioResult
// @Failure      400 {object} ErrorResponse "Datos inválidos"
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      409 {object} ErrorResponse "Email o username ya registrado"
// @Router       /admin/users [post]
func (h *UserHandler) CrearUsuario(c *gin.Context) {
	var input ports.CrearUsuarioInput
	if err := c.ShouldBindJSON(&input); err != nil {
		SendError(c, http.StatusBadRequest, "INVALID_REQUEST", "Datos inválidos: se requieren nombreCompleto, nombreUsuario, emailUsuario y rol (ADMIN|OPERATOR).")
		return
	}

	// Normalizar email a minúsculas
	input.EmailUsuario = strings.ToLower(strings.TrimSpace(input.EmailUsuario))

	orgID := extraerOrgID(c)
	actorID := extraerUserID(c)
	resultado, err := h.service.CrearUsuario(c.Request.Context(), orgID, actorID, input)
	if err != nil {
		if strings.Contains(err.Error(), "EMAIL_DELIVERY_FAILED") {
			SendError(c, http.StatusBadGateway, "EMAIL_DELIVERY_FAILED", "Usuario no creado. El servidor de correo no está disponible.")
			return
		}
		SendError(c, http.StatusConflict, "USER_CONFLICT", err.Error())
		return
	}
	c.JSON(http.StatusCreated, resultado)
}

// ==========================================
// GET /api/users/:id
// ==========================================

// ObtenerUsuario devuelve el perfil completo de un usuario con sus instancias asignadas.
//
// @Summary      Obtener usuario por ID
// @Description  Devuelve el perfil detallado del usuario incluyendo sus instancias Proxmox permitidas (`instanciasPermitidas`). Solo devuelve usuarios de la misma organización del admin.
// @Tags         Usuarios (Admin)
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "UUID del usuario"
// @Success      200 {object} ports.UsuarioDetalleDTO
// @Failure      400 {object} ErrorResponse "UUID inválido"
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      404 {object} ErrorResponse "Usuario no encontrado"
// @Router       /admin/users/{id} [get]
func (h *UserHandler) ObtenerUsuario(c *gin.Context) {
	id, ok := parsearUUID(c, "id")
	if !ok {
		return
	}
	orgID := extraerOrgID(c)

	detalle, err := h.service.ObtenerUsuario(c.Request.Context(), id, orgID)
	if err != nil {
		SendError(c, http.StatusNotFound, "USER_NOT_FOUND", "Usuario no encontrado.")
		return
	}
	c.JSON(http.StatusOK, detalle)
}

// ==========================================
// PUT /api/users/:id
// ==========================================

// ActualizarUsuario actualiza parcialmente los datos de un usuario.
//
// @Summary      Actualizar usuario
// @Description  Actualiza parcialmente los datos de un usuario. Solo se modifican los campos presentes en el body (semántica PATCH). Campos posibles: `nombreCompleto`, `emailUsuario`, `rol`, `activo`.
// @Tags         Usuarios (Admin)
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id   path string true "UUID del usuario"
// @Param        body body ports.ActualizarUsuarioInput true "Campos a actualizar"
// @Success      200 {object} ports.UsuarioResumenDTO
// @Failure      400 {object} ErrorResponse "Datos inválidos"
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Failure      409 {object} ErrorResponse "Email ya registrado"
// @Router       /admin/users/{id} [put]
func (h *UserHandler) ActualizarUsuario(c *gin.Context) {
	id, ok := parsearUUID(c, "id")
	if !ok {
		return
	}
	orgID := extraerOrgID(c)
	actorID := extraerUserID(c)

	var input ports.ActualizarUsuarioInput
	if err := c.ShouldBindJSON(&input); err != nil {
		SendError(c, http.StatusBadRequest, "INVALID_REQUEST", "Datos de actualización inválidos.")
		return
	}

	if input.EmailUsuario != "" {
		input.EmailUsuario = strings.ToLower(strings.TrimSpace(input.EmailUsuario))
	}

	usuario, err := h.service.ActualizarUsuario(c.Request.Context(), id, orgID, actorID, input)
	if err != nil {
		SendError(c, http.StatusConflict, "UPDATE_CONFLICT", err.Error())
		return
	}
	c.JSON(http.StatusOK, usuario)
}

// ==========================================
// DELETE /api/users/:id
// ==========================================

// EliminarUsuario realiza un soft-delete del usuario y cierra todas sus sesiones.
//
// @Summary      Eliminar usuario (soft-delete)
// @Description  Marca al usuario como inactivo (`activo=false`) sin borrarlo físicamente, e invalida todas sus sesiones activas. Un admin no puede eliminarse a sí mismo.
// @Tags         Usuarios (Admin)
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "UUID del usuario"
// @Success      204 "Sin contenido"
// @Failure      400 {object} ErrorResponse "No puede eliminarse a sí mismo"
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Router       /admin/users/{id} [delete]
func (h *UserHandler) EliminarUsuario(c *gin.Context) {
	id, ok := parsearUUID(c, "id")
	if !ok {
		return
	}
	orgID := extraerOrgID(c)
	actorID := extraerUserID(c)

	// Prevenir que el admin se elimine a sí mismo
	if id == extraerUserID(c) {
		SendError(c, http.StatusBadRequest, "SELF_DELETE_NOT_ALLOWED", "No puede eliminar su propio usuario.")
		return
	}

	if err := h.service.EliminarUsuario(c.Request.Context(), id, orgID, actorID); err != nil {
		SendError(c, http.StatusNotFound, "USER_NOT_FOUND", "Usuario no encontrado.")
		return
	}
	c.Status(http.StatusNoContent)
}

// ==========================================
// PUT /api/admin/users/:id/permissions
// ==========================================

// asignarPermisosRequest define el body para la asignación de permisos de instancia.
type asignarPermisosRequest struct {
	Vmids []int `json:"vmids" binding:"required"`
}

// AsignarPermisos reemplaza todos los permisos de instancia de un usuario operador.
//
// @Summary      Asignar instancias Proxmox al usuario
// @Description  Reemplaza atómicamente todos los permisos de instancia del usuario. Envía un array de VMIDs: `{"vmids": [100, 102]}`. Para quitar todos los permisos, enviar un array vacío: `{"vmids": []}`.
// @Tags         Usuarios (Admin)
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id   path string true "UUID del usuario"
// @Param        body body asignarPermisosRequest true "Lista de VMIDs a asignar"
// @Success      204 "Sin contenido"
// @Failure      400 {object} map[string]string
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Router       /admin/users/{id}/permissions [put]
func (h *UserHandler) AsignarPermisos(c *gin.Context) {
	id, ok := parsearUUID(c, "id")
	if !ok {
		return
	}
	orgID := extraerOrgID(c)
	actorID := extraerUserID(c)

	var req asignarPermisosRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		SendError(c, http.StatusBadRequest, "INVALID_REQUEST", "Se requiere el campo vmids (array de enteros).")
		return
	}

	if err := h.service.AsignarPermisos(c.Request.Context(), id, orgID, actorID, req.Vmids); err != nil {
		SendError(c, http.StatusNotFound, "USER_NOT_FOUND", err.Error())
		return
	}
	c.Status(http.StatusNoContent)
}

// ==========================================
// GET /api/admin/users/:id/permissions
// ==========================================

// permisosResponse es la respuesta del endpoint GET /permissions.
type permisosResponse struct {
	Vmids []int `json:"vmids"`
}

// ObtenerPermisos devuelve la lista de VMIDs asignados a un usuario.
//
// @Summary      Obtener permisos de instancia del usuario
// @Description  Devuelve el conjunto de VMIDs de Proxmox a los que tiene acceso el usuario. Si no tiene permisos asignados, devuelve un array vacío.
// @Tags         Usuarios (Admin)
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "UUID del usuario"
// @Success      200 {object} permisosResponse
// @Failure      400 {object} ErrorResponse "UUID inválido"
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      404 {object} ErrorResponse "Usuario no encontrado"
// @Router       /admin/users/{id}/permissions [get]
func (h *UserHandler) ObtenerPermisos(c *gin.Context) {
	id, ok := parsearUUID(c, "id")
	if !ok {
		return
	}
	orgID := extraerOrgID(c)

	vmids, err := h.service.ObtenerPermisos(c.Request.Context(), id, orgID)
	if err != nil {
		SendError(c, http.StatusNotFound, "USER_NOT_FOUND", "Usuario no encontrado.")
		return
	}

	// Devolver siempre un array (nunca null) para consistencia con el front
	if vmids == nil {
		vmids = []int{}
	}
	c.JSON(http.StatusOK, permisosResponse{Vmids: vmids})
}

// ==========================================
// GET /api/users/:id/activity
// ==========================================

// ListarActividad devuelve el historial de acciones auditadas de un usuario.
//
// @Summary      Actividad auditada del usuario
// @Description  Devuelve hasta 100 registros de auditoría del usuario, ordenados del más reciente al más antiguo. Soporta filtros: `?accion=START|STOP|...`, `?desde=2026-01-01`, `?hasta=2026-12-31`.
// @Tags         Usuarios (Admin)
// @Produce      json
// @Security     BearerAuth
// @Param        id     path  string false "UUID del usuario"
// @Param        accion query string false "Filtrar por tipo de acción"
// @Param        desde  query string false "Fecha desde (YYYY-MM-DD)"
// @Param        hasta  query string false "Fecha hasta (YYYY-MM-DD)"
// @Success      200 {array} ports.ActividadDTO
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Router       /admin/users/{id}/activity [get]
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
		SendError(c, http.StatusNotFound, "USER_NOT_FOUND", err.Error())
		return
	}
	c.JSON(http.StatusOK, actividad)
}

// ==========================================
// POST /api/users/:id/2fa/reset
// ==========================================

// ResetearTotp invalida el 2FA del usuario, forzando revinculación en el próximo login.
//
// @Summary      Resetear 2FA del usuario
// @Description  Invalida el secreto TOTP del usuario. En su próximo login deberá escanear un nuevo QR para vincular 2FA. También invalida todas sus sesiones activas.
// @Tags         Usuarios (Admin)
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "UUID del usuario"
// @Success      200 {object} map[string]string
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Failure      404 {object} map[string]string
// @Router       /admin/users/{id}/2fa/reset [post]
func (h *UserHandler) ResetearTotp(c *gin.Context) {
	id, ok := parsearUUID(c, "id")
	if !ok {
		return
	}
	orgID := extraerOrgID(c)
	actorID := extraerUserID(c)

	if err := h.service.ResetearTotp(c.Request.Context(), id, orgID, actorID); err != nil {
		SendError(c, http.StatusNotFound, "USER_NOT_FOUND", err.Error())
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
//
// @Summary      Restablecer contraseña (admin)
// @Description  Genera una nueva contraseña temporal segura, la hashea, la persiste y se la envía al usuario por email. Establece `must_change_password=true` e invalida todas las sesiones activas del usuario. Solo su hash queda en base de datos. Requiere rol ADMIN.
// @Tags         Usuarios (Admin)
// @Produce      json
// @Security     BearerAuth
// @Param        id path string true "UUID del usuario"
// @Success      200 {object} map[string]string
// @Failure      401 {object} map[string]string
// @Failure      403 {object} ErrorResponse "OPERATOR recibe 403 Forbidden"
// @Failure      404 {object} ErrorResponse "Usuario no encontrado en la organización"
// @Router       /admin/users/{id}/password/reset [post]
func (h *UserHandler) ResetearContrasena(c *gin.Context) {
	id, ok := parsearUUID(c, "id")
	if !ok {
		return
	}
	orgID := extraerOrgID(c)
	actorID := extraerUserID(c)

	_, err := h.service.ResetearContrasena(c.Request.Context(), id, orgID, actorID)
	if err != nil {
		if strings.Contains(err.Error(), "EMAIL_DELIVERY_FAILED") {
			SendError(c, http.StatusBadGateway, "EMAIL_DELIVERY_FAILED", "Error al enviar la contraseña por correo.")
			return
		}
		SendError(c, http.StatusNotFound, "USER_NOT_FOUND", err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "Contraseña restablecida. Se ha enviado un correo al usuario.",
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
		SendError(c, http.StatusBadRequest, "INVALID_UUID", "El identificador proporcionado no es válido.")
		return uuid.Nil, false
	}
	return id, true
}
