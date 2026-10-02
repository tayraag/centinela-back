package http

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
)

// InstanceHandler maneja los endpoints HTTP de instancias Proxmox.
type InstanceHandler struct {
	proxmox     ports.ProxmoxPort
	userRepo    ports.UserRepository
	seguimiento ports.SeguimientoTareas
}

// NewInstanceHandler crea un nuevo InstanceHandler con el cliente de Proxmox,
// el repositorio de usuarios (para el filtrado RBAC del listado) y el
// seguimiento de tareas (para publicar TASK_FINISHED al terminar start/stop).
func NewInstanceHandler(proxmox ports.ProxmoxPort, userRepo ports.UserRepository, seguimiento ports.SeguimientoTareas) *InstanceHandler {
	return &InstanceHandler{proxmox: proxmox, userRepo: userRepo, seguimiento: seguimiento}
}

// responderTarea registra la tarea para seguirla y responde 202 con el UPID y
// el tareaId (el mismo que llega en el evento TASK_FINISHED). Si no se puede
// registrar, la acción ya se disparó en Proxmox igual: se responde sin tareaId.
func (h *InstanceHandler) responderTarea(c *gin.Context, vmid int, accion, upid string) {
	respuesta := gin.H{"upid": upid}
	tareaID, err := h.seguimiento.Seguir(c.Request.Context(), extraerUserID(c), vmid, accion, upid)
	if err != nil {
		log.Printf("[TAREAS] no se pudo registrar la tarea %s de %d: %v", accion, vmid, err)
	} else {
		respuesta["tareaId"] = tareaID.String()
	}
	c.JSON(http.StatusAccepted, respuesta)
}

// extraerVmid parsea el parámetro de ruta :vmid. El guard RequireInstanceAccess
// ya lo valida antes de llegar acá, pero se vuelve a parsear porque no queda
// guardado en el contexto de gin.
func extraerVmid(c *gin.Context) (int, bool) {
	vmid, err := strconv.Atoi(c.Param("vmid"))
	if err != nil {
		SendError(c, http.StatusBadRequest, "INVALID_VMID", "El identificador de instancia debe ser un número entero válido.")
		return 0, false
	}
	return vmid, true
}

// mapearErrorProxmox traduce los errores de ports.ProxmoxPort al contrato HTTP.
// El detalle del error nunca llega al cliente, pero se registra en el log para
// poder diagnosticar la causa real (token rechazado, red, URL mal configurada).
func mapearErrorProxmox(c *gin.Context, err error) {
	ruta := c.Request.Method + " " + c.Request.URL.Path
	switch {
	case errors.Is(err, ports.ErrInstanciaNoEncontrada):
		SendError(c, http.StatusNotFound, "INSTANCE_NOT_FOUND", "La instancia no existe en Proxmox.")
	case errors.Is(err, ports.ErrInstanciaOcupada):
		log.Printf("⏳ Instancia ocupada [%s]: %v", ruta, err)
		SendError(c, http.StatusConflict, "INSTANCE_BUSY", "La instancia se encuentra ejecutando otra tarea. Aguarde a que finalice.")
	case errors.Is(err, ports.ErrProxmoxTimeout):
		log.Printf("⏱️  Proxmox no respondió a tiempo [%s]: %v", ruta, err)
		// Distinto de PROXMOX_UNAVAILABLE: la orden pudo haber llegado y aplicarse,
		// así que el front no debe sugerir reintentar sin verificar el estado.
		SendError(c, http.StatusGatewayTimeout, "PROXMOX_TIMEOUT", "Proxmox no respondió a tiempo; la acción puede haberse aplicado")
	case errors.Is(err, ports.ErrProxmoxCredenciales):
		log.Printf("🔑 Proxmox rechazó el API Token [%s]: %v", ruta, err)
		SendError(c, http.StatusBadGateway, "PROXMOX_UNAVAILABLE", "Error al consultar la infraestructura subyacente.")
	case errors.Is(err, ports.ErrProxmoxNoDisponible):
		log.Printf("⚠️  Proxmox no disponible [%s]: %v", ruta, err)
		SendError(c, http.StatusBadGateway, "PROXMOX_UNAVAILABLE", "Error al consultar la infraestructura subyacente.")
	default:
		log.Printf("❌ Error inesperado de Proxmox [%s]: %v", ruta, err)
		SendError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Error inesperado al comunicarse con Proxmox VE.")
	}
}

// ==========================================
// GET /api/instances
// ==========================================

// ListarInstancias devuelve el inventario de VMs y contenedores visible para
// el usuario autenticado: un ADMIN ve todo el cluster, un OPERATOR solo las
// instancias que tiene asignadas.
//
// @Summary      Listar instancias
// @Description  Lee en vivo el inventario de Proxmox VE (VMs y contenedores). Un ADMIN recibe el cluster completo; un OPERATOR recibe únicamente las instancias que tiene asignadas.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Success      200 {array} ports.InstanciaListadaDTO
// @Failure      500 {object} ErrorResponse "INTERNAL_ERROR — error al consultar los permisos del usuario"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE — Proxmox caído, sin red o token rechazado"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — Proxmox no respondió a tiempo; la acción puede haberse aplicado"
// @Router       /instances [get]
func (h *InstanceHandler) ListarInstancias(c *gin.Context) {
	instancias, err := h.proxmox.ListarInstancias(c.Request.Context())
	if err != nil {
		mapearErrorProxmox(c, err)
		return
	}

	rolVal, _ := c.Get(middleware.ContextKeyRol)
	rol, _ := rolVal.(string)

	if rol != "ADMIN" {
		userID := extraerUserID(c)
		vmidsPermitidos, err := h.userRepo.ListarPermisosDeUsuario(c.Request.Context(), userID)
		if err != nil {
			SendError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Error al consultar los permisos del usuario.")
			return
		}
		permitido := make(map[int]bool, len(vmidsPermitidos))
		for _, vmid := range vmidsPermitidos {
			permitido[vmid] = true
		}

		filtradas := instancias[:0]
		for _, instancia := range instancias {
			if permitido[instancia.Vmid] {
				filtradas = append(filtradas, instancia)
			}
		}
		instancias = filtradas
	}

	resultado := make([]ports.InstanciaListadaDTO, 0, len(instancias))
	for _, instancia := range instancias {
		tipo := instancia.Tipo
		if tipo == ports.TipoInstanciaQemu {
			tipo = "vm"
		}
		resultado = append(resultado, ports.InstanciaListadaDTO{
			ID:     instancia.Vmid,
			Name:   instancia.Nombre,
			Type:   tipo,
			Node:   instancia.Nodo,
			Status: instancia.Estado,
		})
	}
	c.JSON(http.StatusOK, resultado)
}

// ==========================================
// GET /api/instances/:vmid
// ==========================================

// ObtenerInstancia devuelve el estado actual de una instancia Proxmox.
//
// @Summary      Obtener instancia
// @Description  Devuelve el estado actual (running/stopped, tipo, nodo) de una VM o contenedor leído en vivo desde Proxmox VE.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Param        vmid path int true "VMID de la instancia"
// @Success      200 {object} ports.InstanciaProxmoxDTO
// @Failure      400 {object} ErrorResponse "INVALID_VMID"
// @Failure      403 {object} ErrorResponse "INSTANCE_ACCESS_DENIED — el OPERATOR no tiene este vmid asignado"
// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE — Proxmox caído, sin red o token rechazado"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — Proxmox no respondió a tiempo; la acción puede haberse aplicado"
// @Router       /instances/{vmid} [get]
func (h *InstanceHandler) ObtenerInstancia(c *gin.Context) {
	vmid, ok := extraerVmid(c)
	if !ok {
		return
	}

	instancia, err := h.proxmox.ObtenerInstancia(c.Request.Context(), vmid)
	if err != nil {
		mapearErrorProxmox(c, err)
		return
	}
	c.JSON(http.StatusOK, instancia)
}

// ==========================================
// POST /api/instances/:vmid/start
// ==========================================

// IniciarInstancia arranca una instancia Proxmox.
//
// @Summary      Iniciar instancia
// @Description  Dispara el arranque de una VM o contenedor en Proxmox VE. La operación es asíncrona: devuelve el UPID de la tarea que Proxmox crea para seguir su progreso.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Param        vmid path int true "VMID de la instancia"
// @Success      202 {object} map[string]string "upid de la tarea en Proxmox y tareaId (llega en el evento TASK_FINISHED al terminar)"
// @Failure      400 {object} ErrorResponse "INVALID_VMID"
// @Failure      403 {object} ErrorResponse "INSTANCE_ACCESS_DENIED — el OPERATOR no tiene este vmid asignado"
// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND"
// @Failure      409 {object} ErrorResponse "INSTANCE_BUSY — la instancia está ejecutando otra tarea (Proxmox la tiene bloqueada)"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE — Proxmox caído, sin red o token rechazado"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — Proxmox no respondió a tiempo; la acción puede haberse aplicado"
// @Router       /instances/{vmid}/start [post]
func (h *InstanceHandler) IniciarInstancia(c *gin.Context) {
	vmid, ok := extraerVmid(c)
	if !ok {
		return
	}

	upid, err := h.proxmox.IniciarInstancia(c.Request.Context(), vmid)
	if err != nil {
		mapearErrorProxmox(c, err)
		return
	}
	h.responderTarea(c, vmid, "start", upid)
}

// ==========================================
// POST /api/instances/:vmid/stop
// ==========================================

// DetenerInstancia detiene (apagado forzado) una instancia Proxmox.
//
// @Summary      Detener instancia
// @Description  Dispara el apagado forzado de una VM o contenedor en Proxmox VE. La operación es asíncrona: devuelve el UPID de la tarea que Proxmox crea para seguir su progreso.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Param        vmid path int true "VMID de la instancia"
// @Success      202 {object} map[string]string "upid de la tarea en Proxmox y tareaId (llega en el evento TASK_FINISHED al terminar)"
// @Failure      400 {object} ErrorResponse "INVALID_VMID"
// @Failure      403 {object} ErrorResponse "INSTANCE_ACCESS_DENIED — el OPERATOR no tiene este vmid asignado; INSTANCE_PROTECTED — la instancia es infraestructura de El Centinela"
// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND"
// @Failure      409 {object} ErrorResponse "INSTANCE_BUSY — la instancia está ejecutando otra tarea (Proxmox la tiene bloqueada)"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE — Proxmox caído, sin red o token rechazado"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — Proxmox no respondió a tiempo; la acción puede haberse aplicado"
// @Router       /instances/{vmid}/stop [post]
func (h *InstanceHandler) DetenerInstancia(c *gin.Context) {
	vmid, ok := extraerVmid(c)
	if !ok {
		return
	}

	upid, err := h.proxmox.DetenerInstancia(c.Request.Context(), vmid)
	if err != nil {
		mapearErrorProxmox(c, err)
		return
	}
	h.responderTarea(c, vmid, "stop", upid)
}
