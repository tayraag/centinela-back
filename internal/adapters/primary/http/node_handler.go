package http

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// NodeHandler expone el estado consolidado del nodo de Proxmox y sus recursos (storages, templates).
type NodeHandler struct {
	service  ports.NodoService
	proxmox  ports.ProxmoxPort
	nodo     string
	permisos permisosDeUsuario // instancias asignadas a un OPERATOR
}

// permisosDeUsuario es lo que usa el handler de ports.UserRepository.
type permisosDeUsuario interface {
	ListarPermisosConNivel(ctx context.Context, usuarioID uuid.UUID) ([]ports.PermisoInstanciaInput, error)
}

// NewNodeHandler crea el handler del estado del nodo y recursos de almacenamiento.
func NewNodeHandler(service ports.NodoService, proxmox ports.ProxmoxPort, nodo string, permisos permisosDeUsuario) *NodeHandler {
	return &NodeHandler{
		service:  service,
		proxmox:  proxmox,
		nodo:     nodo,
		permisos: permisos,
	}
}

// esAdmin: la telemetría completa del host es solo para ADMIN (fail-closed:
// sin rol en el contexto se trata como restringido).
func esAdmin(c *gin.Context) bool {
	rol, _ := c.Get(middleware.ContextKeyRol)
	return rol == "ADMIN"
}

// ==========================================
// GET /api/node/status
// ==========================================

// ObtenerEstado devuelve la salud física del hipervisor (CPU, RAM, disco, uptime)
// y el resumen de instancias.
//
// @Summary      Consultar estado consolidado del nodo
// @Description  OPERATIVO. Disponible para ADMIN y OPERATOR autenticados, con la respuesta segregada por rol. ADMIN recibe la telemetría completa del host. OPERATOR recibe `storage: null` y `allocatable.freeStorageGb: null` (no ve la capacidad física de almacenamiento), e `instancesSummary` cuenta solo las instancias que tiene asignadas; CPU, RAM y uptime del host se mantienen. La lectura se cachea en Redis 10 s (node:status:current): mientras está vigente se responde sin consultar Proxmox. Incluye `allocatable`, con núcleos físicos totales, almacenamiento disponible y RAM libre segura luego de reservar estrictamente 15% para Proxmox. Incluye también instancesSummary, que agrupa el recuento de VMs y contenedores LXC según su estado (running, stopped, paused). Si Proxmox no responde se devuelve la última lectura conocida (node:status:last_known) con `stale: true`; `fetchedAt` indica cuándo se obtuvo. Solo si no hay ninguna lectura previa responde 502 o 504. RAM y almacenamiento en GB (1024³ bytes) con 2 decimales; `storage` es el disco raíz del nodo; `cores` son los hilos lógicos sobre los que se calcula `usagePercent`.
// @Tags         Estado del nodo
// @Produce      json
// @Security     BearerAuth
// @Success      200 {object} EstadoNodeResponse
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "WRONG_TOKEN_TYPE | 2FA_REQUIRED | PASSWORD_CHANGE_REQUIRED"
// @Failure      500 {object} ErrorResponse "PROXMOX_ERROR — error de Proxmox no clasificado; el mensaje es siempre el texto canónico y el detalle queda en el log"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE — Proxmox caído, sin red o token rechazado, y sin lectura previa"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — Proxmox no respondió a tiempo y no hay lectura previa"
// @Router       /node/status [get]
func (h *NodeHandler) ObtenerEstado(c *gin.Context) {
	estado, stale, err := h.service.ObtenerEstado(c.Request.Context())
	if err != nil {
		mapearErrorProxmox(c, err)
		return
	}
	respuesta := aEstadoNodeResponse(estado, stale)
	if !esAdmin(c) {
		// Sin capacidad física de almacenamiento y sin contar instancias ajenas.
		permitidas, err := h.instanciasAsignadas(c)
		if err != nil {
			log.Printf("[NODO] no se pudieron leer los permisos de %s: %v", extraerUserID(c), err)
			SendError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Error al consultar los permisos del usuario.")
			return
		}
		respuesta.Storage = nil
		respuesta.Allocatable.FreeStorageGB = nil
		respuesta.InstancesSummary = aResumenInstanciasNode(ports.ResumirInstancias(estado.Instancias, func(vmid int) bool { return permitidas[vmid] }))
	}
	c.JSON(http.StatusOK, respuesta)
}

// instanciasAsignadas devuelve los VMID sobre los que el usuario tiene permiso.
func (h *NodeHandler) instanciasAsignadas(c *gin.Context) (map[int]bool, error) {
	permisos, err := h.permisos.ListarPermisosConNivel(c.Request.Context(), extraerUserID(c))
	if err != nil {
		return nil, err
	}
	permitidas := make(map[int]bool, len(permisos))
	for _, p := range permisos {
		permitidas[p.Vmid] = true
	}
	return permitidas, nil
}

func aResumenInstanciasNode(r ports.ResumenInstancias) ResumenInstanciasNode {
	resumen := func(r ports.ResumenEstado) ResumenEstadoInstancias {
		return ResumenEstadoInstancias{Running: r.Running, Stopped: r.Stopped, Paused: r.Paused, Total: r.Total}
	}
	return ResumenInstanciasNode{VMs: resumen(r.VMs), LXC: resumen(r.LXC)}
}

func aEstadoNodeResponse(e *ports.EstadoNodo, stale bool) EstadoNodeResponse {
	capacidad := func(m ports.MetricaCapacidad) MetricaCapacidadNode {
		return MetricaCapacidadNode{UsedGb: m.UsedGb, TotalGb: m.TotalGb, UsagePercent: m.UsagePercent}
	}
	storage, libreStorage := capacidad(e.Storage), e.Allocatable.FreeStorageGB
	return EstadoNodeResponse{
		CPU:     MetricaCPUNode{UsagePercent: e.CPU.UsagePercent, Cores: e.CPU.Cores},
		RAM:     capacidad(e.RAM),
		Storage: &storage,
		Allocatable: CuotaAsignableNode{
			MaxCPUCores: e.Allocatable.MaxCPUCores, FreeRamMB: e.Allocatable.FreeRamMB,
			FreeStorageGB: &libreStorage, HeadroomPercent: e.Allocatable.HeadroomPercent,
		},
		UptimeSeconds:    e.UptimeSeconds,
		InstancesSummary: aResumenInstanciasNode(e.InstancesSummary),
		Stale:            stale,
		FetchedAt:        e.FetchedAt.UTC().Format(time.RFC3339),
	}
}

// ==========================================
// GET /api/node/history
// ==========================================

// ObtenerHistorial devuelve la serie histórica del host para el gráfico del Dashboard.
//
// @Summary      Histórico de uso del nodo
// @Description  Devuelve la serie histórica del host (CPU, memoria y rootfs) normalizada a porcentajes. Para quien no es ADMIN se omite `storagePercent` en todos los puntos (no ve la capacidad física de almacenamiento). Es lo que hace que el gráfico del Dashboard ya esté formado al entrar, en vez de arrancar de cero y esperar minutos a que se llene. La fuente es el RRD de Proxmox, cacheado en Redis para no golpear la infraestructura en cada visita.
// @Tags         Nodo
// @Produce      json
// @Security     BearerAuth
// @Success      200 {array} ports.PuntoHistorialNodo "Muestras de más vieja a más nueva"
// @Failure      500 {object} ErrorResponse "PROXMOX_ERROR — error de Proxmox no clasificado; el mensaje es siempre el texto canónico y el detalle queda en el log"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE — Proxmox caído, sin red o token rechazado"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — Proxmox no respondió a tiempo"
// @Router       /node/history [get]
func (h *NodeHandler) ObtenerHistorial(c *gin.Context) {
	serie, err := h.service.ObtenerHistorial(c.Request.Context())
	if err != nil {
		mapearErrorProxmox(c, err)
		return
	}
	if serie == nil {
		// Un array vacío y no null: el frontend siempre recibe una lista.
		serie = []ports.PuntoHistorialNodo{}
	}
	if !esAdmin(c) {
		// Copia: la serie puede venir de la caché compartida.
		sinDisco := make([]ports.PuntoHistorialNodo, len(serie))
		for i, p := range serie {
			p.StoragePercent = nil
			sinDisco[i] = p
		}
		serie = sinDisco
	}
	c.JSON(http.StatusOK, serie)
}

// ==========================================
// GET /api/node/storages
// ==========================================

// ObtenerStorages devuelve la lista de storages activos en el nodo.
//
// @Summary      Consultar catálogo de almacenamiento del nodo
// @Description  ADMIN ONLY. Retorna las unidades de almacenamiento activas en el nodo, filtradas y normalizadas con sus capacidades en bytes y tipos de contenidos permitidos.
// @Tags         Estado del nodo
// @Produce      json
// @Security     BearerAuth
// @Success      200 {array}   StorageResponse
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "WRONG_TOKEN_TYPE | 2FA_REQUIRED | INSUFFICIENT_PERMISSIONS"
// @Failure      500 {object} ErrorResponse "PROXMOX_ERROR — error de Proxmox no clasificado; el mensaje es siempre el texto canónico y el detalle queda en el log"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE — Proxmox caído, sin red o token rechazado"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — Proxmox no respondió a tiempo"
// @Router       /node/storages [get]
func (h *NodeHandler) ObtenerStorages(c *gin.Context) {
	storages, err := h.proxmox.ObtenerStorages(c.Request.Context(), h.nodo)
	if err != nil {
		mapearErrorProxmox(c, err)
		return
	}

	resultado := make([]StorageResponse, 0, len(storages))
	for _, st := range storages {
		if st.Active != 1 {
			continue
		}
		var contentTypes []string
		if st.Content != "" {
			parts := strings.Split(st.Content, ",")
			contentTypes = make([]string, 0, len(parts))
			for _, p := range parts {
				trimmed := strings.TrimSpace(p)
				if trimmed != "" {
					contentTypes = append(contentTypes, trimmed)
				}
			}
		} else {
			contentTypes = []string{}
		}

		resultado = append(resultado, StorageResponse{
			Name:         st.Storage,
			Type:         st.Type,
			TotalBytes:   st.Total,
			FreeBytes:    st.Avail,
			ContentTypes: contentTypes,
		})
	}

	c.JSON(http.StatusOK, resultado)
}

// ==========================================
// GET /api/node/templates
// ==========================================

// ObtenerTemplates consolida y normaliza imágenes ISO y plantillas LXC disponibles en el nodo.
//
// @Summary      Consultar catálogo de plantillas e imágenes ISO
// @Description  ADMIN ONLY. Consolida imágenes ISO de instalación y plantillas LXC de todos los storages activos que admiten dichos contenidos, formateando sus identificadores, nombres y tamaños.
// @Tags         Estado del nodo
// @Produce      json
// @Security     BearerAuth
// @Success      200 {array}   TemplateResponse
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "WRONG_TOKEN_TYPE | 2FA_REQUIRED | INSUFFICIENT_PERMISSIONS"
// @Failure      500 {object} ErrorResponse "PROXMOX_ERROR — error de Proxmox no clasificado; el mensaje es siempre el texto canónico y el detalle queda en el log"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE — Proxmox caído, sin red o token rechazado"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — Proxmox no respondió a tiempo"
// @Router       /node/templates [get]
func (h *NodeHandler) ObtenerTemplates(c *gin.Context) {
	ctx := c.Request.Context()
	storages, err := h.proxmox.ObtenerStorages(ctx, h.nodo)
	if err != nil {
		mapearErrorProxmox(c, err)
		return
	}

	resultado := make([]TemplateResponse, 0)
	for _, st := range storages {
		if st.Active != 1 {
			continue
		}
		// Solo consultar contenido si el storage admite iso o vztmpl
		admiteISO := strings.Contains(st.Content, "iso")
		admiteVztmpl := strings.Contains(st.Content, "vztmpl")
		if !admiteISO && !admiteVztmpl {
			continue
		}

		items, err := h.proxmox.ObtenerContenidoStorage(ctx, h.nodo, st.Storage)
		if err != nil {
			mapearErrorProxmox(c, err)
			return
		}

		for _, item := range items {
			var tipo string
			switch item.Content {
			case "iso":
				tipo = "ISO"
			case "vztmpl":
				tipo = "LXC_TEMPLATE"
			default:
				continue
			}

			// Extraer nombre amigable a partir del volid: ej. "local:iso/ubuntu.iso" -> "ubuntu.iso"
			nombre := extraerNombreTemplate(item.Volid)

			resultado = append(resultado, TemplateResponse{
				ID:        item.Volid,
				Name:      nombre,
				Type:      tipo,
				SizeBytes: item.Size,
			})
		}
	}

	c.JSON(http.StatusOK, resultado)
}

func extraerNombreTemplate(volid string) string {
	parts := strings.Split(volid, "/")
	if len(parts) > 1 {
		return parts[len(parts)-1]
	}
	partsColon := strings.Split(volid, ":")
	if len(partsColon) > 1 {
		return partsColon[len(partsColon)-1]
	}
	return volid
}
