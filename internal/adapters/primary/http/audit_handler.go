package http

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// AuditHandler maneja los endpoints HTTP del registro de auditoría (acceso solo ADMIN).
type AuditHandler struct {
	service ports.AuditService
}

// NewAuditHandler crea un nuevo AuditHandler con el servicio inyectado.
func NewAuditHandler(service ports.AuditService) *AuditHandler {
	return &AuditHandler{service: service}
}

// ==========================================
// GET /api/audit
// ==========================================

// ListarAuditoria devuelve el log de auditoría con filtros, paginación y ordenamiento.
//
// @Summary      Listar log de auditoría
// @Description  Devuelve el log de auditoría de la organización con soporte de filtros, paginación y ordenamiento. Solo accesible por ADMIN.
// @Tags         Auditoría (Admin)
// @Produce      json
// @Security     BearerAuth
// @Param        usuarioId   query string  false "Filtrar por UUID de usuario"
// @Param        accion      query string  false "Filtrar por tipo de acción (ej: LOGIN, CREAR_USUARIO)"
// @Param        instanciaId query string  false "Filtrar por ID de instancia Proxmox"
// @Param        resultado   query string  false "Filtrar por resultado: EXITO o FALLA"
// @Param        desde       query string  false "Fecha desde (YYYY-MM-DD)"
// @Param        hasta       query string  false "Fecha hasta (YYYY-MM-DD)"
// @Param        pagina      query int     false "Número de página (default 1)"
// @Param        tamano      query int     false "Registros por página (default 50, máx 200)"
// @Param        ordenarPor  query string  false "Campo de ordenamiento: fechaHora (default), accion, resultado"
// @Param        direccion   query string  false "Dirección: desc (default) o asc"
// @Success      200 {object} ports.PaginaAuditoria
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Router       /admin/audit [get]
func (h *AuditHandler) ListarAuditoria(c *gin.Context) {
	orgID := extraerOrgID(c)

	filtros := parsearFiltrosAuditoria(c)
	opciones := parsearOpcionesAuditoria(c)

	pagina, err := h.service.ListarAuditoria(c.Request.Context(), orgID, filtros, opciones)
	if err != nil {
		SendError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Error al obtener el log de auditoría.")
		return
	}
	c.JSON(http.StatusOK, pagina)
}

// ==========================================
// GET /api/audit/export
// ==========================================

// ExportarAuditoria exporta el log de auditoría en CSV o JSON según el parámetro `formato`.
//
// @Summary      Exportar log de auditoría
// @Description  Exporta el log de auditoría de la organización. Aplica los mismos filtros que el listado. Retorna el archivo como adjunto descargable. Solo accesible por ADMIN.
// @Tags         Auditoría (Admin)
// @Produce      text/csv
// @Produce      application/json
// @Security     BearerAuth
// @Param        usuarioId   query string  false "Filtrar por UUID de usuario"
// @Param        accion      query string  false "Filtrar por tipo de acción"
// @Param        instanciaId query string  false "Filtrar por ID de instancia"
// @Param        resultado   query string  false "EXITO o FALLA"
// @Param        desde       query string  false "Fecha desde (YYYY-MM-DD)"
// @Param        hasta       query string  false "Fecha hasta (YYYY-MM-DD)"
// @Param        formato     query string  false "Formato de exportación: csv (default) o json"
// @Success      200 {file} binary "Archivo CSV o JSON descargable"
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Router       /admin/audit/export [get]
func (h *AuditHandler) ExportarAuditoria(c *gin.Context) {
	orgID := extraerOrgID(c)
	filtros := parsearFiltrosAuditoria(c)
	formato := c.DefaultQuery("formato", "csv")
	fecha := time.Now().Format("2006-01-02")

	switch formato {
	case "json":
		data, err := h.service.ExportarJSON(c.Request.Context(), orgID, filtros)
		if err != nil {
			SendError(c, http.StatusInternalServerError, "EXPORT_ERROR", "Error al exportar auditoría en JSON.")
			return
		}
		filename := fmt.Sprintf("auditoria_%s.json", fecha)
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		c.Data(http.StatusOK, "application/json", data)

	default: // "csv"
		data, err := h.service.ExportarCSV(c.Request.Context(), orgID, filtros)
		if err != nil {
			SendError(c, http.StatusInternalServerError, "EXPORT_ERROR", "Error al exportar auditoría en CSV.")
			return
		}
		filename := fmt.Sprintf("auditoria_%s.csv", fecha)
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		c.Data(http.StatusOK, "text/csv; charset=utf-8", data)
	}
}

// ==========================================
// Helpers de parseo
// ==========================================

// parsearFiltrosAuditoria extrae y valida los query params de filtrado.
func parsearFiltrosAuditoria(c *gin.Context) ports.FiltrosAuditoria {
	filtros := ports.FiltrosAuditoria{
		Accion:      c.Query("accion"),
		InstanciaID: c.Query("instanciaId"),
		Resultado:   c.Query("resultado"),
	}

	if uidStr := c.Query("usuarioId"); uidStr != "" {
		if uid, err := uuid.Parse(uidStr); err == nil {
			filtros.UsuarioID = &uid
		}
	}

	if desdeStr := c.Query("desde"); desdeStr != "" {
		if t, err := time.Parse(time.DateOnly, desdeStr); err == nil {
			filtros.Desde = &t
		}
	}
	if hastaStr := c.Query("hasta"); hastaStr != "" {
		if t, err := time.Parse(time.DateOnly, hastaStr); err == nil {
			filtros.Hasta = &t
		}
	}

	return filtros
}

// parsearOpcionesAuditoria extrae y valida los query params de paginación y ordenamiento.
func parsearOpcionesAuditoria(c *gin.Context) ports.OpcionesAuditoria {
	opciones := ports.OpcionesAuditoria{
		Pagina:     1,
		TamanoPag:  50,
		OrdenarPor: "fechaHora",
		Direccion:  "desc",
	}

	if paginaStr := c.Query("pagina"); paginaStr != "" {
		if p, err := strconv.Atoi(paginaStr); err == nil && p > 0 {
			opciones.Pagina = p
		}
	}
	if tamanoStr := c.Query("tamano"); tamanoStr != "" {
		if t, err := strconv.Atoi(tamanoStr); err == nil && t > 0 {
			opciones.TamanoPag = t
		}
	}
	if op := c.Query("ordenarPor"); op != "" {
		opciones.OrdenarPor = op
	}
	if dir := c.Query("direccion"); dir == "asc" || dir == "desc" {
		opciones.Direccion = dir
	}

	return opciones
}
