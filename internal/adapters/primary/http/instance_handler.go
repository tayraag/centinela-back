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

// listarInstanciasResponse fue eliminado ya que el contrato original exige
// devolver []InstanciaListadaDTO directamente para no romper el front.

// ==========================================
// Handler
// ==========================================

// InstanceHandler maneja los endpoints HTTP de instancias Proxmox.
type InstanceHandler struct {
	proxmox     ports.ProxmoxPort
	userRepo    ports.UserRepository
	seguimiento ports.SeguimientoTareas
	inventario  ports.InventarioService // resuelve las IPs del listado
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
	inventario ports.InventarioService,
) *InstanceHandler {
	return &InstanceHandler{
		inventario:  inventario,
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

// estadosRequeridosPorAccion es la matriz de estados operativos: el estado en
// el que debe estar la instancia para que la acción tenga sentido.
var estadosRequeridosPorAccion = map[string]string{
	"start":    "stopped",
	"stop":     "running",
	"shutdown": "running",
	"reboot":   "running",
}

// validarEstadoParaAccion consulta el estado actual (lectura) y aborta con 409
// INSTANCE_INVALID_STATE si es incompatible con la acción, sin emitir ninguna
// orden de escritura a Proxmox. Devuelve la instancia consultada.
func (h *InstanceHandler) validarEstadoParaAccion(c *gin.Context, vmid int, accion string) (*ports.InstanciaProxmoxDTO, bool) {
	instancia, err := h.proxmox.ObtenerInstancia(c.Request.Context(), vmid)
	if err != nil {
		mapearErrorProxmox(c, err)
		return nil, false
	}
	if instancia.Estado != estadosRequeridosPorAccion[accion] {
		SendError(c, http.StatusConflict, "INSTANCE_INVALID_STATE",
			"La instancia se encuentra en un estado incompatible para la acción solicitada")
		return nil, false
	}
	return instancia, true
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
// @Description  OPERATIVO. Lee en vivo el inventario de Proxmox VE (VMs y contenedores). Un ADMIN recibe el cluster completo; un OPERATOR recibe únicamente las instancias que tiene asignadas. Incluye telemetría (CPU, RAM), nivel de acceso y tarea activa. ip y activeTask pueden ser null.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Success      200 {array} InstanciaInventarioResponse
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
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

	// Resolver las IPs en paralelo, solo de las instancias visibles para el usuario.
	// Nunca falla: la que no se pueda resolver (apagada, sin agente) queda en null.
	ips := h.inventario.ResolverIPs(c.Request.Context(), instancias)

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

		var nivel string
		if rol == "ADMIN" {
			nivel = ports.NivelAccesoFullAccess
		} else {
			nivel = nivelPorVmid[inst.Vmid]
		}

		cpu := inst.Cpu
		mem := inst.Mem
		maxMem := inst.MaxMem

		resultado = append(resultado, ports.InstanciaListadaDTO{
			ID:          inst.Vmid,
			Name:        inst.Nombre,
			Type:        tipo,
			Node:        inst.Nodo,
			Status:      inst.Estado,
			Ip:          ips[inst.Vmid],
			CpuUsage:    &cpu,
			RamUsage:    &mem,
			MaxRam:      &maxMem,
			NivelAcceso: nivel,
			ActiveTask:  activeTask,
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
// @Description  Devuelve el estado actual (running/stopped, tipo, nodo, telemetría) de una VM o contenedor leído en vivo desde Proxmox VE.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Param        vmid path int true "VMID de la instancia"
// @Success      200 {object} ports.InstanciaProxmoxDTO
// @Failure      400 {object} ErrorResponse "INVALID_VMID — el vmid de la ruta no es un número entero"
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "WRONG_TOKEN_TYPE | 2FA_REQUIRED | PASSWORD_CHANGE_REQUIRED | NO_ROLE | NO_USER | INVALID_USER_ID | INSTANCE_ACCESS_DENIED — el OPERATOR no tiene este vmid asignado"
// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND — la instancia no existe en Proxmox"
// @Failure      500 {object} ErrorResponse "INTERNAL_ERROR — al verificar permisos de acceso o error inesperado de Proxmox"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE — Proxmox caído, sin red o token rechazado"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — Proxmox no respondió a tiempo"
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
// @Description  OPERATIVO. Dispara el arranque de una VM o contenedor en Proxmox VE. La operación es asíncrona: responde 202 Accepted con el UPID de la tarea creada en Proxmox. No espera a que la instancia quede encedida.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Param        vmid path int true "VMID de la instancia"
// @Success      202 {object} AccionAceptadaResponse "Acción aceptada: upid de la tarea en Proxmox y tareaId. tareaId se omite si no se pudo registrar la tarea"
// @Failure      400 {object} ErrorResponse "INVALID_VMID — el vmid de la ruta no es un número entero"
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "WRONG_TOKEN_TYPE | 2FA_REQUIRED | PASSWORD_CHANGE_REQUIRED | NO_ROLE | NO_USER | INVALID_USER_ID | INSTANCE_ACCESS_DENIED — el OPERATOR no tiene FULL_ACCESS sobre este vmid | INSTANCE_PROTECTED — infraestructura de El Centinela"
// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND — la instancia no existe en Proxmox"
// @Failure      409 {object} ErrorResponse "INSTANCE_INVALID_STATE — la instancia no está stopped | INSTANCE_BUSY — la instancia está ejecutando otra tarea; reintentar al recibir TASK_FINISHED"
// @Failure      500 {object} ErrorResponse "INTERNAL_ERROR — al verificar permisos de acceso o error inesperado de Proxmox"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE — Proxmox caído, sin red o token rechazado; la orden NO llegó a aplicarse"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — Proxmox no respondió a tiempo; la acción puede haberse aplicado"
// @Router       /instances/{vmid}/start [post]
func (h *InstanceHandler) IniciarInstancia(c *gin.Context) {
	vmid, ok := extraerVmid(c)
	if !ok {
		return
	}
	if _, ok := h.validarEstadoParaAccion(c, vmid, "start"); !ok {
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
	h.registrarAuditVM(c, vmid, "", "START", "PENDING", map[string]any{
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
// @Description  OPERATIVO. Dispara el apagado forzado de una VM o contenedor en Proxmox VE. La operación es asíncrona: responde 202 Accepted con el UPID de la tarea creada en Proxmox. No espera a que la instancia quede detenida.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Param        vmid path int true "VMID de la instancia"
// @Success      202 {object} AccionAceptadaResponse "Acción aceptada: upid de la tarea en Proxmox y tareaId. tareaId se omite si no se pudo registrar la tarea"
// @Failure      400 {object} ErrorResponse "INVALID_VMID — el vmid de la ruta no es un número entero"
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "WRONG_TOKEN_TYPE | 2FA_REQUIRED | PASSWORD_CHANGE_REQUIRED | NO_ROLE | NO_USER | INVALID_USER_ID | INSTANCE_ACCESS_DENIED | INSTANCE_PROTECTED — infraestructura de El Centinela"
// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND — la instancia no existe en Proxmox"
// @Failure      409 {object} ErrorResponse "INSTANCE_INVALID_STATE — la instancia no está running | INSTANCE_BUSY — la instancia está ejecutando otra tarea; reintentar al recibir TASK_FINISHED"
// @Failure      500 {object} ErrorResponse "INTERNAL_ERROR — al verificar permisos de acceso o error inesperado de Proxmox"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE — Proxmox caído, sin red o token rechazado; la orden NO llegó a aplicarse"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — Proxmox no respondió a tiempo; la acción puede haberse aplicado"
// @Router       /instances/{vmid}/stop [post]
func (h *InstanceHandler) DetenerInstancia(c *gin.Context) {
	vmid, ok := extraerVmid(c)
	if !ok {
		return
	}
	if _, ok := h.validarEstadoParaAccion(c, vmid, "stop"); !ok {
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
	h.registrarAuditVM(c, vmid, "", "STOP", "PENDING", map[string]any{
		"upid": upid, "action": "stop", "resource_type": "vm_or_lxc",
	})
	h.responderTarea(c, vmid, "stop", upid)
}

// ==========================================
// POST /api/instances/:vmid/status/:action
// ==========================================

// accionesValidas son las acciones soportadas por el endpoint genérico de ciclo de vida.
var accionesValidas = map[string]bool{
	"start":    true,
	"stop":     true,
	"shutdown": true,
	"reboot":   true,
}

// CambiarEstado ejecuta una acción de ciclo de vida (start/stop/shutdown/reboot) sobre
// una instancia. Complementa los endpoints /start y /stop (que se mantienen
// por retrocompatibilidad) y agrega soporte para reboot y shutdown.
//
// @Summary      Cambiar estado de instancia
// @Description  OPERATIVO. Ejecuta una acción de ciclo de vida (start, stop, shutdown, reboot) sobre una VM o contenedor. El endpoint genérico complementa a /start y /stop manteniendo retrocompatibilidad. La acción Pause NO está implementada: use POST /instances/{vmid}/pause, que hoy responde 404 NOT_FOUND.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Param        vmid   path int    true "VMID de la instancia"
// @Param        action path string true "Acción a ejecutar (start | stop | shutdown | reboot). pause NO es válida: responde 400 INVALID_ACTION"
// @Success      202 {object} AccionAceptadaResponse "Acción aceptada: upid de la tarea en Proxmox y tareaId. tareaId se omite si no se pudo registrar la tarea"
// @Failure      400 {object} ErrorResponse "INVALID_VMID | INVALID_ACTION — acción distinta de start, stop, shutdown o reboot"
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "WRONG_TOKEN_TYPE | 2FA_REQUIRED | PASSWORD_CHANGE_REQUIRED | NO_ROLE | NO_USER | INVALID_USER_ID | INSTANCE_ACCESS_DENIED | INSTANCE_PROTECTED"
// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND — la instancia no existe en Proxmox"
// @Failure      409 {object} ErrorResponse "INSTANCE_INVALID_STATE — start exige stopped; stop, shutdown y reboot exigen running | INSTANCE_BUSY — la instancia está ejecutando otra tarea; reintentar al recibir TASK_FINISHED"
// @Failure      500 {object} ErrorResponse "INTERNAL_ERROR — al verificar permisos de acceso o error inesperado de Proxmox"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE — Proxmox caído, sin red o token rechazado; la orden NO llegó a aplicarse"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — Proxmox no respondió a tiempo; la acción puede haberse aplicado"
// @Router       /instances/{vmid}/status/{action} [post]
func (h *InstanceHandler) CambiarEstado(c *gin.Context) {
	// Contrato objetivo de la acción Pause. NO OPERATIVO: la ruta NO está registrada
	// en el router y hoy responde 404 NOT_FOUND. Se documenta junto a las acciones
	// reales para que el frontend no la confunda con shutdown ni con reboot.
	// @Summary      Pausar instancia (planificado)
	// @Description  PLANIFICADO; NO OPERATIVO. Contrato objetivo para pausar una VM o contenedor en Proxmox VE. La ruta no está registrada y actualmente responde 404 NOT_FOUND; pause tampoco es una acción válida en POST /instances/{vmid}/status/{action} (responde 400 INVALID_ACTION).
	// @Tags         Instancias Proxmox
	// @Produce      json
	// @Security     BearerAuth
	// @Param        vmid path int true "VMID de la instancia"
	// @Success      202 {object} AccionAceptadaResponse "Acción aceptada: upid de la tarea en Proxmox y tareaId"
	// @Failure      400 {object} ErrorResponse "INVALID_VMID"
	// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
	// @Failure      403 {object} ErrorResponse "WRONG_TOKEN_TYPE | 2FA_REQUIRED | PASSWORD_CHANGE_REQUIRED | NO_ROLE | NO_USER | INVALID_USER_ID | INSTANCE_ACCESS_DENIED | INSTANCE_PROTECTED"
	// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND"
	// @Failure      409 {object} ErrorResponse "INSTANCE_BUSY"
	// @Failure      500 {object} ErrorResponse "INTERNAL_ERROR"
	// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE"
	// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT"
	// @x-implementation-status "planned"
	// @Router       /instances/{vmid}/pause [post]

	vmid, ok := extraerVmid(c)
	if !ok {
		return
	}

	accion := strings.ToLower(c.Param("action"))
	if !accionesValidas[accion] {
		SendError(c, http.StatusBadRequest, "INVALID_ACTION",
			fmt.Sprintf("Acción '%s' no válida. Opciones: start, stop, shutdown, reboot.", accion))
		return
	}

	// Obtener la instancia primero para saber su nodo y tipo (necesario para shutdown y reboot)
	// y validar la matriz de estados antes de cualquier orden de escritura.
	instancia, ok := h.validarEstadoParaAccion(c, vmid, accion)
	if !ok {
		return
	}
	var err error

	var (
		upid        string
		accionAudit string
	)

	switch accion {
	case "start":
		upid, err = h.proxmox.IniciarInstancia(c.Request.Context(), vmid)
		accionAudit = "START"
	case "stop":
		upid, err = h.proxmox.DetenerInstancia(c.Request.Context(), vmid)
		accionAudit = "STOP"
	case "shutdown":
		upid, err = h.proxmox.Shutdown(c.Request.Context(), instancia.Nodo, vmid, instancia.Tipo)
		accionAudit = "SHUTDOWN"
	case "reboot":
		upid, err = h.proxmox.Reboot(c.Request.Context(), instancia.Nodo, vmid, instancia.Tipo)
		accionAudit = "REBOOT"
	}

	if err != nil {
		h.registrarAuditVM(c, vmid, instancia.Nombre, accionAudit, ports.ResultadoFalla, map[string]any{
			"action": accion, "error": err.Error(),
		})
		mapearErrorProxmox(c, err)
		return
	}
	h.registrarAuditVM(c, vmid, instancia.Nombre, accionAudit, "PENDING", map[string]any{
		"upid": upid, "action": accion, "resource_type": instancia.Tipo,
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
// @Description  OPERATIVO. Elimina permanentemente una VM o contenedor de Proxmox. La instancia debe estar detenida (stopped) antes de invocar este endpoint; si está encendida se responde 409. Responde 204 No Content sin cuerpo: la eliminación es síncrona y NO devuelve upid ni tareaId, a diferencia de las acciones de energía.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Param        vmid path int true "VMID de la instancia"
// @Success      204 "Instancia eliminada correctamente. Sin cuerpo de respuesta"
// @Failure      400 {object} ErrorResponse "INVALID_VMID — el vmid de la ruta no es un número entero"
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "WRONG_TOKEN_TYPE | 2FA_REQUIRED | PASSWORD_CHANGE_REQUIRED | NO_ROLE | INVALID_ROLE | INSUFFICIENT_PERMISSIONS | INVALID_VMID | INSTANCE_PROTECTED — solo ADMIN puede eliminar instancias"
// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND — la instancia no existe en Proxmox"
// @Failure      409 {object} ErrorResponse "INSTANCE_NOT_STOPPED — la instancia debe estar detenida"
// @Failure      500 {object} ErrorResponse "INTERNAL_ERROR — error inesperado de Proxmox"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE — Proxmox caído, sin red o token rechazado"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — Proxmox no respondió a tiempo"
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
