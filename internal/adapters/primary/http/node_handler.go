package http

import (
	"net/http"
	"time"

	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
)

// NodeHandler expone el estado consolidado del nodo de Proxmox.
type NodeHandler struct {
	service ports.NodoService
}

// NewNodeHandler crea el handler del estado del nodo.
func NewNodeHandler(service ports.NodoService) *NodeHandler {
	return &NodeHandler{service: service}
}

// ==========================================
// GET /api/node/status
// ==========================================

// ObtenerEstado devuelve la salud física del hipervisor (CPU, RAM, disco, uptime)
// y el resumen de instancias.
//
// @Summary      Consultar estado consolidado del nodo
// @Description  OPERATIVO. Disponible para ADMIN y OPERATOR autenticados (no se filtra por permisos de instancia). La lectura se cachea en Redis 10 s (node:status:current): mientras está vigente se responde sin consultar Proxmox. Incluye también instancesSummary, que agrupa el recuento de VMs y contenedores LXC según su estado (running, stopped, paused). Si Proxmox no responde se devuelve la última lectura conocida (node:status:last_known) con `stale: true`; `fetchedAt` indica cuándo se obtuvo. Solo si no hay ninguna lectura previa responde 502 o 504. RAM y almacenamiento en GB (1024³ bytes) con 2 decimales; `storage` es el disco raíz del nodo; `cores` son los hilos lógicos sobre los que se calcula `usagePercent`.
// @Tags         Estado del nodo
// @Produce      json
// @Security     BearerAuth
// @Success      200 {object} EstadoNodeResponse
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "WRONG_TOKEN_TYPE | 2FA_REQUIRED | PASSWORD_CHANGE_REQUIRED"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE — Proxmox caído, sin red o token rechazado, y sin lectura previa"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — Proxmox no respondió a tiempo y no hay lectura previa"
// @Router       /node/status [get]
func (h *NodeHandler) ObtenerEstado(c *gin.Context) {
	estado, stale, err := h.service.ObtenerEstado(c.Request.Context())
	if err != nil {
		mapearErrorProxmox(c, err)
		return
	}
	c.JSON(http.StatusOK, aEstadoNodeResponse(estado, stale))
}

func aEstadoNodeResponse(e *ports.EstadoNodo, stale bool) EstadoNodeResponse {
	capacidad := func(m ports.MetricaCapacidad) MetricaCapacidadNode {
		return MetricaCapacidadNode{UsedGb: m.UsedGb, TotalGb: m.TotalGb, UsagePercent: m.UsagePercent}
	}
	resumen := func(r ports.ResumenEstado) ResumenEstadoInstancias {
		return ResumenEstadoInstancias{Running: r.Running, Stopped: r.Stopped, Paused: r.Paused, Total: r.Total}
	}
	return EstadoNodeResponse{
		CPU:           MetricaCPUNode{UsagePercent: e.CPU.UsagePercent, Cores: e.CPU.Cores},
		RAM:           capacidad(e.RAM),
		Storage:       capacidad(e.Storage),
		UptimeSeconds: e.UptimeSeconds,
		InstancesSummary: ResumenInstanciasNode{
			VMs: resumen(e.InstancesSummary.VMs),
			LXC: resumen(e.InstancesSummary.LXC),
		},
		Stale:     stale,
		FetchedAt: e.FetchedAt.UTC().Format(time.RFC3339),
	}
}
