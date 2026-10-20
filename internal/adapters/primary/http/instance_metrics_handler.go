package http

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
)

// InstanceMetricsResponse conserva la referencia pública en vmid.
type InstanceMetricsResponse struct {
	VMID      string                   `json:"vmid"`
	Timeframe string                   `json:"timeframe"`
	Data      []domain.PuntoMetricaDTO `json:"data"`
}

type InstanceMetricsHandler struct {
	proxmox ports.ProxmoxPort
	cache   ports.KeyValueStore
}

func NewInstanceMetricsHandler(proxmox ports.ProxmoxPort, cache ports.KeyValueStore) *InstanceMetricsHandler {
	return &InstanceMetricsHandler{proxmox: proxmox, cache: cache}
}

// ==========================================
// GET /api/instances/:instanceRef/metrics
// ==========================================

// ObtenerMetricas sirve el histórico inicial del detalle de instancia.
//
// @Summary      Histórico de métricas de instancia
// @Description  OPERATIVO. Requiere READ_ONLY o FULL_ACCESS sobre la instancia; ADMIN tiene acceso implícito. Usa referencias públicas inst_..., nunca VMID numérico. Caché de 30 segundos por instancia y timeframe: X-Cache HIT evita consultar Proxmox y MISS indica una lectura nueva. Si se omite timeframe se usa 2h; 2h se consulta como hour en Proxmox y 24h como day, conservando la ventana que devuelve Proxmox. CPU en porcentaje de 0 a 100, RAM en bytes, red y disco en bytes/s. CPU y RAM usada pueden ser null cuando no hay datos; data siempre es un arreglo, vacío si no hay muestras.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Param        instanceRef path string true "Referencia pública de la instancia (inst_...)"
// @Param        timeframe query string false "Ventana histórica" Enums(2h,24h) default(2h)
// @Success      200 {object} InstanceMetricsResponse
// @Header       200 {string} X-Cache "HIT o MISS"
// @Failure      400 {object} ErrorResponse "INVALID_TIMEFRAME — el timeframe debe ser '2h' o '24h'; un valor vacío también es inválido"
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "WRONG_TOKEN_TYPE | 2FA_REQUIRED | PASSWORD_CHANGE_REQUIRED | NO_ROLE | NO_USER | INVALID_USER_ID | INSTANCE_ACCESS_DENIED — el OPERATOR no tiene esta instancia asignada"
// @Failure      404 {object} ErrorResponse "RESOURCE_NOT_FOUND — referencia inválida o instancia inexistente en Proxmox (misma respuesta)"
// @Failure      500 {object} ErrorResponse "INTERNAL_ERROR — al verificar permisos de acceso | PROXMOX_ERROR — error de Proxmox no clasificado; el mensaje es siempre el texto canónico y el detalle queda en el log"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE — Proxmox caído, sin red o token rechazado"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — Proxmox no respondió a tiempo"
// @Router       /instances/{instanceRef}/metrics [get]
func (h *InstanceMetricsHandler) ObtenerMetricas(c *gin.Context) {
	vmid, ok := middleware.VmidResuelto(c)
	if !ok {
		middleware.AbortRecursoNoEncontrado(c)
		return
	}
	timeframe := "2h"
	if value, exists := c.GetQuery("timeframe"); exists {
		timeframe = value
	} else if _, exists := c.Request.URL.Query()["timeframe"]; exists {
		timeframe = ""
	}
	if timeframe != "2h" && timeframe != "24h" {
		SendError(c, http.StatusBadRequest, "INVALID_TIMEFRAME", "El timeframe debe ser '2h' o '24h'")
		return
	}
	ctx := c.Request.Context()
	key := fmt.Sprintf("cache:instance:%d:metrics:%s", vmid, timeframe)
	var data []domain.PuntoMetricaDTO
	if cached, err := h.cache.Get(ctx, key); err == nil {
		if json.Unmarshal([]byte(cached), &data) == nil && data != nil {
			c.Header("X-Cache", "HIT")
			c.JSON(http.StatusOK, InstanceMetricsResponse{c.Param("instanceRef"), timeframe, data})
			return
		}
	}
	instance, err := h.proxmox.ObtenerInstancia(ctx, vmid)
	if err != nil {
		mapearErrorProxmox(c, err)
		return
	}
	rrdTimeframe := "hour"
	if timeframe == "24h" {
		rrdTimeframe = "day"
	}
	data, err = h.proxmox.ObtenerMetricasRRD(ctx, instance.Nodo, instance.Tipo, vmid, rrdTimeframe)
	if err != nil {
		mapearErrorProxmox(c, err)
		return
	}
	if data == nil {
		data = []domain.PuntoMetricaDTO{}
	}
	if err := h.cache.Set(ctx, key, data, 30*time.Second); err != nil {
		log.Printf("[METRICS] No se pudo cachear el histórico de %d: %v", vmid, err)
	}
	c.Header("X-Cache", "MISS")
	c.JSON(http.StatusOK, InstanceMetricsResponse{c.Param("instanceRef"), timeframe, data})
}
