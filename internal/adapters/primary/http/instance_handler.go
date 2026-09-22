package http

import (
	"errors"
	"net/http"
	"strconv"

	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
)

// InstanceHandler maneja los endpoints HTTP de instancias Proxmox.
type InstanceHandler struct {
	proxmox  ports.ProxmoxPort
	userRepo ports.UserRepository
}

// NewInstanceHandler crea un nuevo InstanceHandler con el cliente de Proxmox y
// el repositorio de usuarios (para el filtrado RBAC del listado) inyectados.
func NewInstanceHandler(proxmox ports.ProxmoxPort, userRepo ports.UserRepository) *InstanceHandler {
	return &InstanceHandler{proxmox: proxmox, userRepo: userRepo}
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
func mapearErrorProxmox(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ports.ErrInstanciaNoEncontrada):
		SendError(c, http.StatusNotFound, "INSTANCE_NOT_FOUND", "La instancia no existe en Proxmox.")
	case errors.Is(err, ports.ErrProxmoxNoDisponible):
		SendError(c, http.StatusBadGateway, "PROXMOX_UNAVAILABLE", "Error al consultar la infraestructura subyacente.")
	default:
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
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE"
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
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE"
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
// @Success      202 {object} map[string]string "upid de la tarea creada en Proxmox"
// @Failure      400 {object} ErrorResponse "INVALID_VMID"
// @Failure      403 {object} ErrorResponse "INSTANCE_ACCESS_DENIED — el OPERATOR no tiene este vmid asignado"
// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE"
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
	c.JSON(http.StatusAccepted, gin.H{"upid": upid})
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
// @Success      202 {object} map[string]string "upid de la tarea creada en Proxmox"
// @Failure      400 {object} ErrorResponse "INVALID_VMID"
// @Failure      403 {object} ErrorResponse "INSTANCE_ACCESS_DENIED — el OPERATOR no tiene este vmid asignado"
// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE"
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
	c.JSON(http.StatusAccepted, gin.H{"upid": upid})
}
