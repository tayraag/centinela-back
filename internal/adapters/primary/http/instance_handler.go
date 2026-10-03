package http

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
)

// ==========================================
// Tipos de respuesta
// ==========================================

// instanciasSummaryDTO resume el inventario visible para el header del panel.
type instanciasSummaryDTO struct {
	Total   int `json:"total"`
	Running int `json:"running"`
	Stopped int `json:"stopped"`
}

// listarInstanciasResponse envuelve el listado con su resumen agregado.
// La estructura base { id, name, type, node, status } de cada instancia
// se mantiene intacta para el selector del frontend.
type listarInstanciasResponse struct {
	Instances []ports.InstanciaListadaDTO `json:"instances"`
	Summary   instanciasSummaryDTO        `json:"summary"`
}

// ==========================================
// Handler
// ==========================================

// InstanceHandler maneja los endpoints HTTP de instancias Proxmox.
type InstanceHandler struct {
	proxmox     ports.ProxmoxPort
	userRepo    ports.UserRepository
	seguimiento ports.SeguimientoTareas
	tareas      ports.TareaRepository
	audit       ports.AuditService
}

// NewInstanceHandler crea un nuevo InstanceHandler.
// Recibe el cliente de Proxmox, el repositorio de usuarios (RBAC del listado),
// el seguimiento de tareas (publica TASK_FINISHED) y el servicio de auditoría.
func NewInstanceHandler(
	proxmox ports.ProxmoxPort,
	userRepo ports.UserRepository,
	seguimiento ports.SeguimientoTareas,
	tareas ports.TareaRepository,
	audit ports.AuditService,
) *InstanceHandler {
	return &InstanceHandler{
		proxmox:     proxmox,
		userRepo:    userRepo,
		seguimiento: seguimiento,
		tareas:      tareas,
		audit:       audit,
	}
}

// ==========================================
// Helpers privados
// ==========================================

// registrarAuditVM registra una acción sobre una instancia en la tabla auditoria.
// Los errores se descartan silenciosamente para no interrumpir la operación principal.
func (h *InstanceHandler) registrarAuditVM(c *gin.Context, vmid int, instanciaNombre, accion, resultado string, detalles map[string]any) {
	h.audit.Registrar(c.Request.Context(), ports.RegistrarAuditoriaInput{
		UsuarioID:       extraerUserID(c),
		Accion:          accion,
		InstanciaID:     strconv.Itoa(vmid),
		InstanciaNombre: instanciaNombre,
		Resultado:       resultado,
		Detalles:        detalles,
	})
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
// el usuario autenticado con campos de telemetría extendidos y resumen del cluster.
// Un ADMIN ve todo el cluster; un OPERATOR solo sus instancias asignadas.
//
// @Summary      Listar instancias
// @Description  Lee en vivo el inventario de Proxmox VE (VMs y contenedores). Un ADMIN recibe el cluster completo; un OPERATOR recibe únicamente las instancias que tiene asignadas. Incluye telemetría (CPU, RAM) y tareas activas.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Success      200 {object} listarInstanciasResponse
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

	// nivelPorVmid mapea vmid → nivelAcceso para el usuario autenticado.
	// Para ADMIN está vacío (nivel implícito: acceso total, omitempty en JSON).
	nivelPorVmid := map[int]string{}

	if rol != "ADMIN" {
		userID := extraerUserID(c)
		permisos, err := h.userRepo.ListarPermisosConNivel(c.Request.Context(), userID)
		if err != nil {
			SendError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Error al consultar los permisos del usuario.")
			return
		}
		for _, p := range permisos {
			nivelPorVmid[p.Vmid] = p.NivelAcceso
		}
		// Filtrar solo las instancias con permiso explícito.
		filtradas := make([]ports.InstanciaProxmoxDTO, 0, len(instancias))
		for _, inst := range instancias {
			if _, ok := nivelPorVmid[inst.Vmid]; ok {
				filtradas = append(filtradas, inst)
			}
		}
		instancias = filtradas
	}

	// Obtener tareas activas en un único query sobre la lista visible.
	vmids := make([]int, 0, len(instancias))
	for _, inst := range instancias {
		vmids = append(vmids, inst.Vmid)
	}
	tareasActivas, err := h.tareas.BuscarTareasActivasPorVmids(c.Request.Context(), vmids)
	if err != nil {
		// No crítico: loguear y continuar sin datos de tareas.
		log.Printf("⚠️  [INSTANCES] error al buscar tareas activas: %v", err)
		tareasActivas = map[int]string{}
	}

	// Construir respuesta final con todos los campos.
	var running, stopped int
	resultado := make([]ports.InstanciaListadaDTO, 0, len(instancias))
	for _, inst := range instancias {
		tipo := inst.Tipo
		if tipo == ports.TipoInstanciaQemu {
			tipo = "vm"
		}

		var activeTask *string
		if tid, ok := tareasActivas[inst.Vmid]; ok {
			activeTask = &tid
		}

		resultado = append(resultado, ports.InstanciaListadaDTO{
			ID:          inst.Vmid,
			Name:        inst.Nombre,
			Type:        tipo,
			Node:        inst.Nodo,
			Status:      inst.Estado,
			CpuUsage:    inst.Cpu,
			RamUsage:    inst.Mem,
			MaxRam:      inst.MaxMem,
			NivelAcceso: nivelPorVmid[inst.Vmid], // "" para ADMIN (omitempty)
			ActiveTask:  activeTask,
		})

		if inst.Estado == "running" {
			running++
		} else {
			stopped++
		}
	}

	c.JSON(http.StatusOK, listarInstanciasResponse{
		Instances: resultado,
		Summary: instanciasSummaryDTO{
			Total:   len(resultado),
			Running: running,
			Stopped: stopped,
		},
	})
}

// ==========================================
// GET /api/instances/:vmid
// ==========================================

// ObtenerInstancia devuelve el estado actual de una instancia Proxmox.
//
// @Summary      Obtener instancia
// @Description  Devuelve el estado actual (running/stopped, tipo, nodo, telemetría) de una VM o contenedor leído en vivo desde Proxmox VE.
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
// @Failure      403 {object} ErrorResponse "INSTANCE_ACCESS_DENIED — el OPERATOR no tiene FULL_ACCESS sobre este vmid"
// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND"
// @Failure      409 {object} ErrorResponse "INSTANCE_BUSY — la instancia está ejecutando otra tarea"
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
		h.registrarAuditVM(c, vmid, "", ports.AccionIniciarVM, ports.ResultadoFalla, map[string]any{
			"action": "start", "error": err.Error(),
		})
		mapearErrorProxmox(c, err)
		return
	}
	h.registrarAuditVM(c, vmid, "", ports.AccionIniciarVM, ports.ResultadoExito, map[string]any{
		"upid": upid, "action": "start", "resource_type": "vm_or_lxc",
	})
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
// @Failure      403 {object} ErrorResponse "INSTANCE_ACCESS_DENIED — el OPERATOR no tiene FULL_ACCESS sobre este vmid; INSTANCE_PROTECTED — infraestructura de El Centinela"
// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND"
// @Failure      409 {object} ErrorResponse "INSTANCE_BUSY — la instancia está ejecutando otra tarea"
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
		h.registrarAuditVM(c, vmid, "", ports.AccionDetenerVM, ports.ResultadoFalla, map[string]any{
			"action": "stop", "error": err.Error(),
		})
		mapearErrorProxmox(c, err)
		return
	}
	h.registrarAuditVM(c, vmid, "", ports.AccionDetenerVM, ports.ResultadoExito, map[string]any{
		"upid": upid, "action": "stop", "resource_type": "vm_or_lxc",
	})
	h.responderTarea(c, vmid, "stop", upid)
}

// ==========================================
// POST /api/instances/:vmid/status/:action
// ==========================================

// accionesValidas son las acciones soportadas por el endpoint genérico de ciclo de vida.
var accionesValidas = map[string]bool{
	"start":  true,
	"stop":   true,
	"reboot": true,
}

// CambiarEstado ejecuta una acción de ciclo de vida (start/stop/reboot) sobre
// una instancia. Complementa los endpoints /start y /stop (que se mantienen
// por retrocompatibilidad) y agrega soporte para reboot.
//
// @Summary      Cambiar estado de instancia
// @Description  Ejecuta una acción de ciclo de vida (start, stop, reboot) sobre una VM o contenedor. El endpoint genérico complementa a /start y /stop manteniendo retrocompatibilidad.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Param        vmid   path int    true "VMID de la instancia"
// @Param        action path string true "Acción a ejecutar (start | stop | reboot)"
// @Success      202 {object} map[string]string "upid y tareaId"
// @Failure      400 {object} ErrorResponse "INVALID_VMID | INVALID_ACTION"
// @Failure      403 {object} ErrorResponse "INSTANCE_ACCESS_DENIED — se requiere FULL_ACCESS"
// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND"
// @Failure      409 {object} ErrorResponse "INSTANCE_BUSY"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT"
// @Router       /instances/{vmid}/status/{action} [post]
func (h *InstanceHandler) CambiarEstado(c *gin.Context) {
	vmid, ok := extraerVmid(c)
	if !ok {
		return
	}

	accion := strings.ToLower(c.Param("action"))
	if !accionesValidas[accion] {
		SendError(c, http.StatusBadRequest, "INVALID_ACTION",
			fmt.Sprintf("Acción '%s' no válida. Opciones: start, stop, reboot.", accion))
		return
	}

	var (
		upid        string
		err         error
		accionAudit string
	)

	switch accion {
	case "start":
		upid, err = h.proxmox.IniciarInstancia(c.Request.Context(), vmid)
		accionAudit = ports.AccionIniciarVM
	case "stop":
		upid, err = h.proxmox.DetenerInstancia(c.Request.Context(), vmid)
		accionAudit = ports.AccionDetenerVM
	case "reboot":
		upid, err = h.proxmox.ReiniciarInstancia(c.Request.Context(), vmid)
		accionAudit = ports.AccionReiniciarVM
	}

	if err != nil {
		h.registrarAuditVM(c, vmid, "", accionAudit, ports.ResultadoFalla, map[string]any{
			"action": accion, "error": err.Error(),
		})
		mapearErrorProxmox(c, err)
		return
	}
	h.registrarAuditVM(c, vmid, "", accionAudit, ports.ResultadoExito, map[string]any{
		"upid": upid, "action": accion, "resource_type": "vm_or_lxc",
	})
	h.responderTarea(c, vmid, accion, upid)
}

// ==========================================
// DELETE /api/instances/:vmid
// ==========================================

// EliminarInstancia elimina permanentemente una instancia de Proxmox.
// Solo puede ser invocado por ADMIN (el middleware RequireRole lo garantiza).
// Rechaza la operación con 409 si la instancia está encendida.
//
// @Summary      Eliminar instancia
// @Description  Elimina permanentemente una VM o contenedor de Proxmox. La instancia debe estar detenida (stopped) antes de invocar este endpoint; si está encendida se responde 409.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Param        vmid path int true "VMID de la instancia"
// @Success      204 "Instancia eliminada correctamente"
// @Failure      400 {object} ErrorResponse "INVALID_VMID"
// @Failure      403 {object} ErrorResponse "INSUFFICIENT_PERMISSIONS — solo ADMIN puede eliminar instancias"
// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND"
// @Failure      409 {object} ErrorResponse "INSTANCE_NOT_STOPPED — la instancia debe estar detenida"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT"
// @Router       /instances/{vmid} [delete]
func (h *InstanceHandler) EliminarInstancia(c *gin.Context) {
	vmid, ok := extraerVmid(c)
	if !ok {
		return
	}

	// Verificar el estado actual de la instancia antes de intentar eliminarla.
	instancia, err := h.proxmox.ObtenerInstancia(c.Request.Context(), vmid)
	if err != nil {
		mapearErrorProxmox(c, err)
		return
	}

	// Proxmox rechazará el DELETE si la instancia está encendida; prevenimos
	// con un 409 explícito para dar un mensaje de error claro al frontend.
	if instancia.Estado != "stopped" {
		SendError(c, http.StatusConflict, "INSTANCE_NOT_STOPPED",
			"La instancia debe estar detenida antes de poder eliminarla.")
		return
	}

	if err := h.proxmox.EliminarInstancia(c.Request.Context(), vmid); err != nil {
		h.registrarAuditVM(c, vmid, instancia.Nombre, ports.AccionEliminarVM, ports.ResultadoFalla, map[string]any{
			"error": err.Error(), "resource_type": instancia.Tipo,
		})
		mapearErrorProxmox(c, err)
		return
	}

	h.registrarAuditVM(c, vmid, instancia.Nombre, ports.AccionEliminarVM, ports.ResultadoExito, map[string]any{
		"resource_type": instancia.Tipo,
	})
	c.Status(http.StatusNoContent)
}
