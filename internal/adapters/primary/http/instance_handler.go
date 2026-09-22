package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// InstanceHandler maneja los endpoints HTTP de instancias Proxmox.
// Placeholder: la integración real con Proxmox VE (internal/adapters/secondary/proxmox)
// todavía no está implementada. Estos métodos existen para que el guard de
// autorización por recurso (RequireInstanceAccess) tenga rutas reales donde aplicarse.
type InstanceHandler struct{}

// NewInstanceHandler crea un nuevo InstanceHandler.
func NewInstanceHandler() *InstanceHandler {
	return &InstanceHandler{}
}

// ==========================================
// GET /api/instances/:vmid
// ==========================================

// ObtenerInstancia devuelve el detalle de una instancia Proxmox.
//
// @Summary      Obtener instancia
// @Description  Placeholder: la integración con Proxmox VE todavía no está implementada.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Param        vmid path int true "VMID de la instancia"
// @Success      501 {object} ErrorResponse
// @Failure      403 {object} ErrorResponse "INSTANCE_ACCESS_DENIED — el OPERATOR no tiene este vmid asignado"
// @Router       /instances/{vmid} [get]
func (h *InstanceHandler) ObtenerInstancia(c *gin.Context) {
	SendError(c, http.StatusNotImplemented, "NOT_IMPLEMENTED", "La integración con Proxmox VE todavía no está implementada.")
}

// ==========================================
// POST /api/instances/:vmid/start
// ==========================================

// IniciarInstancia arranca una instancia Proxmox.
//
// @Summary      Iniciar instancia
// @Description  Placeholder: la integración con Proxmox VE todavía no está implementada.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Param        vmid path int true "VMID de la instancia"
// @Success      501 {object} ErrorResponse
// @Failure      403 {object} ErrorResponse "INSTANCE_ACCESS_DENIED — el OPERATOR no tiene este vmid asignado"
// @Router       /instances/{vmid}/start [post]
func (h *InstanceHandler) IniciarInstancia(c *gin.Context) {
	SendError(c, http.StatusNotImplemented, "NOT_IMPLEMENTED", "La integración con Proxmox VE todavía no está implementada.")
}

// ==========================================
// POST /api/instances/:vmid/stop
// ==========================================

// DetenerInstancia detiene una instancia Proxmox.
//
// @Summary      Detener instancia
// @Description  Placeholder: la integración con Proxmox VE todavía no está implementada.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Param        vmid path int true "VMID de la instancia"
// @Success      501 {object} ErrorResponse
// @Failure      403 {object} ErrorResponse "INSTANCE_ACCESS_DENIED — el OPERATOR no tiene este vmid asignado"
// @Router       /instances/{vmid}/stop [post]
func (h *InstanceHandler) DetenerInstancia(c *gin.Context) {
	SendError(c, http.StatusNotImplemented, "NOT_IMPLEMENTED", "La integración con Proxmox VE todavía no está implementada.")
}
