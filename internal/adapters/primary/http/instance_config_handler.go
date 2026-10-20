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

// NewInstanceConfigHandler crea el handler de PUT /api/instances/:instanceRef/config.
func NewInstanceConfigHandler(servicio ports.ConfiguracionInstancias, audit ports.AuditService) *InstanceConfigHandler {
	return &InstanceConfigHandler{servicio: servicio, audit: audit}
}

// ConfigInstanciaRequest es el cuerpo de PUT /api/instances/:instanceRef/config. Al
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
// @Param        instanceRef path string               true "Referencia pública de la instancia (inst_...)"
// @Param        body        body ConfigInstanciaRequest true "Nuevos cores y/o RAM"
// @Success      200 {object} ConfigInstanciaResponse
// @Failure      400 {object} ErrorResponse "INVALID_REQUEST — sin cores ni ramMb, o valores no enteros, menores a 1 o RAM menor a 16 MiB"
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "INSTANCE_ACCESS_DENIED — se requiere FULL_ACCESS (READ_ONLY no alcanza) | INSTANCE_PROTECTED — infraestructura de El Centinela"
// @Failure      404 {object} ErrorResponse "RESOURCE_NOT_FOUND — referencia inválida o instancia inexistente (misma respuesta)"
// @Failure      409 {object} ErrorResponse "RESOURCE_QUOTA_EXCEEDED — el incremento supera el margen del nodo; no se escribió en Proxmox | INSTANCE_BUSY — otra operación en curso o la config cambió en el medio"
// @Failure      500 {object} ErrorResponse "AUDIT_PERSISTENCE_FAILED — la operación terminó, pero PostgreSQL no pudo confirmar su auditoría; verificar el estado antes de reintentar | PROXMOX_ERROR — error de Proxmox no clasificado; el mensaje es siempre el texto canónico y el detalle queda en el log"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE | CONFIG_APPLIED_VERIFICATION_FAILED — Proxmox aceptó el cambio, pero no se pudo determinar si requiere reinicio"
// @Failure      503 {object} ErrorResponse "INSTANCE_LOCK_UNAVAILABLE | PROVISIONING_COORDINATION_UNAVAILABLE — no se pudo coordinar la validación de capacidad"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT"
// @Router       /instances/{instanceRef}/config [put]
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
		resultadoAuditoria := ports.ResultadoFailed
		aplicada := resultado != nil && resultado.Aplicada
		if aplicada {
			resultadoAuditoria = ports.ResultadoSuccess
		}
		if auditErr := h.auditar(c, vmid, resultadoAuditoria, detallesReconfiguracion(vmid, resultado, cores, ramMb, err)); auditErr != nil {
			mensaje := "La reconfiguración falló y no pudo persistirse su auditoría. El estado de la instancia debe verificarse antes de reintentar."
			if aplicada {
				mensaje = "La configuración fue aplicada, pero no pudieron confirmarse su verificación ni su auditoría. Verifique el estado antes de reintentar."
			}
			SendError(c, http.StatusInternalServerError, "AUDIT_PERSISTENCE_FAILED", mensaje)
			return
		}
		mapearErrorProxmox(c, err)
		return
	}
	if auditErr := h.auditar(c, vmid, ports.ResultadoSuccess, detallesReconfiguracion(vmid, resultado, cores, ramMb, nil)); auditErr != nil {
		SendError(c, http.StatusInternalServerError, "AUDIT_PERSISTENCE_FAILED", "La configuración fue aplicada, pero no pudo confirmarse su auditoría. Verifique el estado de la instancia antes de reintentar.")
		return
	}
	respuesta := ConfigInstanciaResponse{RequiresReboot: resultado.RequiresReboot}
	if resultado.RequiresReboot {
		respuesta.Message = "Recursos modificados; los cambios se aplicarán tras reiniciar la instancia."
	}
	c.JSON(http.StatusOK, respuesta)
}

func detallesReconfiguracion(vmid int, resultado *ports.ResultadoReconfiguracion, cores, ramMb int, fallo error) map[string]any {
	detalles := map[string]any{"resource_id": strconv.Itoa(vmid)}
	if resultado == nil {
		detalles["valoresNuevos"] = map[string]any{"cores": cores, "ramMb": ramMb}
	} else {
		detalles["resource_type"] = resultado.RecursoTipo
		detalles["valoresAnteriores"] = map[string]any{
			"cores": resultado.Anterior.Cores, "ramMb": resultado.Anterior.MemoriaMB,
		}
		detalles["valoresNuevos"] = map[string]any{
			"cores": resultado.Solicitada.Cores, "ramMb": resultado.Solicitada.MemoriaMB,
		}
		detalles["deltaRamMb"] = resultado.Solicitada.MemoriaMB - resultado.Anterior.MemoriaMB
		if resultado.VerificacionPendiente {
			detalles["requiresReboot"] = nil
			detalles["verificationStatus"] = "FAILED"
		} else {
			detalles["requiresReboot"] = resultado.RequiresReboot
		}
	}
	if fallo != nil {
		detalles["error"] = fallo.Error()
	}
	return detalles
}

func (h *InstanceConfigHandler) auditar(c *gin.Context, vmid int, resultado string, detalles map[string]any) error {
	recursoTipo, _ := detalles["resource_type"].(string)
	return h.audit.RegistrarEstricto(c.Request.Context(), ports.RegistrarAuditoriaInput{
		UsuarioID:   extraerUserID(c),
		Accion:      ports.AccionConfigurarInstancia,
		InstanciaID: strconv.Itoa(vmid),
		RecursoTipo: recursoTipo,
		RecursoID:   strconv.Itoa(vmid),
		Resultado:   resultado,
		Detalles:    detalles,
	})
}
