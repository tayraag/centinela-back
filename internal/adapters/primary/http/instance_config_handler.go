package http

import (
	"net/http"
	"strconv"

	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
)

// memoriaMinimaMB es el mínimo de RAM que acepta Proxmox para una instancia.
const memoriaMinimaMB = 16

// InstanceConfigHandler expone el cambio de CPU y RAM de una instancia.
type InstanceConfigHandler struct {
	servicio ports.ConfiguracionInstancias
	audit    ports.AuditService
}

// NewInstanceConfigHandler crea el handler de PUT /api/instances/:vmid/config.
func NewInstanceConfigHandler(servicio ports.ConfiguracionInstancias, audit ports.AuditService) *InstanceConfigHandler {
	return &InstanceConfigHandler{servicio: servicio, audit: audit}
}

// ConfigInstanciaRequest es el cuerpo de PUT /api/instances/:vmid/config. Al
// menos uno de los dos campos; los que vengan, enteros positivos.
type ConfigInstanciaRequest struct {
	Cores *int `json:"cores" minimum:"1" example:"4"`
	RamMb *int `json:"ramMb" minimum:"16" example:"4096"`
}

// ConfigInstanciaResponse es la respuesta 200 del cambio de configuración.
type ConfigInstanciaResponse struct {
	RequiresReboot bool   `json:"requiresReboot" binding:"required" example:"true"`
	Message        string `json:"message,omitempty" example:"Recursos modificados; los cambios se aplicarán tras reiniciar la instancia."`
}

// ActualizarConfig cambia los cores y la RAM de una instancia.
//
// @Summary      Cambiar CPU y RAM de una instancia
// @Description  ADMIN o FULL_ACCESS sobre la instancia. Cambia `cores` y/o `ramMb` (al menos uno, enteros positivos; `ramMb` mínimo 16). Los incrementos se validan contra el margen del nodo (RAM con 15% de headroom y vCPU), comparando con lo que la instancia ya tiene asignado (DeltaRAM): si no alcanza responde 409 RESOURCE_QUOTA_EXCEEDED sin escribir en Proxmox. Las reducciones no se validan. Si la instancia está apagada el cambio queda aplicado (`requiresReboot: false`). Si está encendida y Proxmox lo dejó pendiente porque la instancia no admite hot-plug (lo habitual en VMs QEMU), responde `requiresReboot: true`: se aplica al reiniciarla. En LXC, CPU y RAM se aplican en caliente. Pasa por la guarda de exclusión mutua (409 INSTANCE_BUSY si la instancia tiene otra operación en curso).
// @Tags         Instancias Proxmox
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        vmid path int                    true "VMID de la instancia"
// @Param        body body ConfigInstanciaRequest true "Nuevos cores y/o RAM"
// @Success      200 {object} ConfigInstanciaResponse
// @Failure      400 {object} ErrorResponse "INVALID_VMID | INVALID_REQUEST — sin cores ni ramMb, o valores no enteros, menores a 1 o RAM menor a 16 MiB"
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "INSTANCE_ACCESS_DENIED — se requiere FULL_ACCESS (READ_ONLY no alcanza) | INSTANCE_PROTECTED — infraestructura de El Centinela"
// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND"
// @Failure      409 {object} ErrorResponse "RESOURCE_QUOTA_EXCEEDED — el incremento supera el margen del nodo; no se escribió en Proxmox | INSTANCE_BUSY — otra operación en curso o la config cambió en el medio"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE"
// @Failure      503 {object} ErrorResponse "INSTANCE_LOCK_UNAVAILABLE | PROVISIONING_COORDINATION_UNAVAILABLE — no se pudo coordinar la validación de capacidad"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT"
// @Router       /instances/{vmid}/config [put]
func (h *InstanceConfigHandler) ActualizarConfig(c *gin.Context) {
	vmid, ok := extraerVmid(c)
	if !ok {
		return
	}
	var req ConfigInstanciaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		SendError(c, http.StatusBadRequest, "INVALID_REQUEST", "El cuerpo debe ser JSON con cores y/o ramMb enteros.")
		return
	}
	if req.Cores == nil && req.RamMb == nil {
		SendError(c, http.StatusBadRequest, "INVALID_REQUEST", "Se requiere al menos cores o ramMb.")
		return
	}
	if req.Cores != nil && *req.Cores < 1 {
		SendError(c, http.StatusBadRequest, "INVALID_REQUEST", "cores debe ser un entero positivo.")
		return
	}
	if req.RamMb != nil && *req.RamMb < memoriaMinimaMB {
		SendError(c, http.StatusBadRequest, "INVALID_REQUEST", "ramMb debe ser un entero de al menos 16.")
		return
	}
	cores, ramMb := 0, 0
	if req.Cores != nil {
		cores = *req.Cores
	}
	if req.RamMb != nil {
		ramMb = *req.RamMb
	}

	resultado, err := h.servicio.Reconfigurar(c.Request.Context(), vmid, cores, ramMb)
	if err != nil {
		h.auditar(c, vmid, ports.ResultadoFalla, map[string]any{"cores": cores, "ramMb": ramMb, "error": err.Error()})
		mapearErrorProxmox(c, err)
		return
	}
	h.auditar(c, vmid, ports.ResultadoExito, map[string]any{
		"antes":          map[string]any{"cores": resultado.Anterior.Cores, "ramMb": resultado.Anterior.MemoriaMB},
		"solicitado":     map[string]any{"cores": cores, "ramMb": ramMb},
		"requiresReboot": resultado.RequiresReboot,
	})
	respuesta := ConfigInstanciaResponse{RequiresReboot: resultado.RequiresReboot}
	if resultado.RequiresReboot {
		respuesta.Message = "Recursos modificados; los cambios se aplicarán tras reiniciar la instancia."
	}
	c.JSON(http.StatusOK, respuesta)
}

func (h *InstanceConfigHandler) auditar(c *gin.Context, vmid int, resultado string, detalles map[string]any) {
	h.audit.Registrar(c.Request.Context(), ports.RegistrarAuditoriaInput{
		UsuarioID:   extraerUserID(c),
		Accion:      ports.AccionConfigurarInstancia,
		InstanciaID: strconv.Itoa(vmid),
		Resultado:   resultado,
		Detalles:    detalles,
	})
}
