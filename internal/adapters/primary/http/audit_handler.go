package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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

// ItemAuditoriaResponse conserva los campos anteriores y agrega los alias RF-08.
type ItemAuditoriaResponse struct {
	ports.AuditoriaDTO
	UsuarioNombre string          `json:"usuarioNombre"`
	RecursoTipo   string          `json:"recursoTipo"`
	RecursoID     string          `json:"recursoId"`
	Detalle       json.RawMessage `json:"detalle" swaggertype:"object"`
}

// PaginaAuditoriaResponse es el contrato HTTP del listado administrativo.
type PaginaAuditoriaResponse struct {
	Items      []ItemAuditoriaResponse `json:"items"`
	Total      int64                   `json:"total" example:"1420"`
	Page       int                     `json:"page" example:"1"`
	Pagina     int                     `json:"pagina" example:"1"`
	Limit      int                     `json:"limit" example:"25"`
	TotalPages int64                   `json:"totalPages" example:"57"`
}

// ==========================================
// GET /api/admin/audit
// ==========================================

// ListarAuditoria devuelve el log de auditoría con filtros, paginación y ordenamiento.
//
// @Summary      Listar log de auditoría
// @Description  Solo ADMIN; OPERATOR recibe 403. Devuelve eventos de la organización, incluidos ALERT_TRIGGERED/ALERT_RESOLVED sin actor con alcance persistido. El actor de sistema aparece como [sistema], con usuarioId null. Las alertas históricas sin alcance siguen excluidas.
// @Description  Sin desde se consulta desde el inicio del trimestre actual UTC; si hasta es anterior, se toma un año antes de hasta. hasta con fecha sola incluye todo ese día; RFC3339 conserva el instante exacto. Los filtros inválidos devuelven 400 INVALID_QUERY.
// @Description  recursoId de VM/LXC e instanciaId usan la referencia pública inst_... del inventario, nunca el VMID. Una referencia inválida para VM/LXC devuelve vacío. Se conservan aliases pagina/tamano y los campos anteriores de cada item; page/limit prevalecen sobre sus aliases.
// @Tags         Auditoría (Admin)
// @Produce      json
// @Security     BearerAuth
// @Param        usuarioId   query string  false "Filtrar por UUID de usuario"
// @Param        accion      query string  false "Filtrar por tipo de acción (ej: LOGIN, SNAPSHOT_CREATE, SNAPSHOT_ROLLBACK, SNAPSHOT_DELETE, ALERT_TRIGGERED, CONFIG_UPDATED)"
// @Param        instanciaId query string false "Referencia pública inst_... de la instancia"
// @Param        resultado   query string  false "Filtrar por resultado: PENDIENTE, SUCCESS, FAILED (infraestructura); EXITO o FALLA (otras acciones)"
// @Param        desde       query string  false "Fecha desde (RFC3339 o YYYY-MM-DD)"
// @Param        hasta       query string  false "Fecha hasta (RFC3339 o YYYY-MM-DD)"
// @Param        recursoTipo query string false "Tipo del recurso" Enums(VM,LXC,HOST,USER)
// @Param        recursoId query string false "Referencia pública inst_... para VM/LXC; identificador propio para HOST/USER"
// @Param        page query int false "Página solicitada" default(1) minimum(1)
// @Param        limit query int false "Tamaño de página; valores mayores a 100 se recortan a 100" default(25) minimum(1)
// @Param        pagina query int false "Alias anterior de page; omitir si usa page" minimum(1)
// @Param        tamano query int false "Alias anterior de limit; mismo máximo de 100" minimum(1)
// @Param        ordenarPor  query string  false "Campo de ordenamiento: fechaHora (default), accion, resultado"
// @Param        direccion   query string  false "Dirección: desc (default) o asc"
// @Param        excluirAccion query []string false "Acciones a excluir; repetibles o separadas por coma" collectionFormat(multi)
// @Success      200 {object} PaginaAuditoriaResponse
// @Failure      400 {object} ErrorResponse "INVALID_QUERY"
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Router       /admin/audit [get]
func (h *AuditHandler) ListarAuditoria(c *gin.Context) {
	orgID := extraerOrgID(c)

	filtros := parsearFiltrosAuditoria(c)
	opciones := parsearOpcionesAuditoria(c)
	if err := validarConsultaAuditoria(c, &filtros, &opciones); err != nil {
		SendError(c, http.StatusBadRequest, "INVALID_QUERY", err.Error())
		return
	}

	pagina, err := h.service.ListarAuditoria(c.Request.Context(), orgID, filtros, opciones)
	if err != nil {
		SendError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Error al obtener el log de auditoría.")
		return
	}
	items := make([]ItemAuditoriaResponse, 0, len(pagina.Items))
	for _, item := range pagina.Items {
		items = append(items, ItemAuditoriaResponse{AuditoriaDTO: item, UsuarioNombre: item.NombreUsuario, RecursoTipo: item.RecursoTipo, RecursoID: item.RecursoID, Detalle: item.Detalles})
	}

	totalPages := pagina.Total / int64(opciones.TamanoPag)
	if pagina.Total%int64(opciones.TamanoPag) != 0 {
		totalPages++
	}
	c.JSON(http.StatusOK, PaginaAuditoriaResponse{Items: items, Total: pagina.Total, Page: opciones.Pagina, Pagina: opciones.Pagina, Limit: opciones.TamanoPag, TotalPages: totalPages})
}

// ==========================================
// GET /api/admin/audit/export
// ==========================================

// ExportarAuditoria exporta el log de auditoría en CSV o JSON según el parámetro `formato`.
//
// @Summary      Exportar log de auditoría
// @Description  Solo ADMIN; OPERATOR recibe 403. Exporta hasta 100000 registros con los filtros y rango temporal del listado, sin paginación. formato=json devuelve JSON; cualquier otro valor devuelve CSV. Incluye alertas de sistema con alcance de la organización; las históricas sin alcance quedan fuera. Los identificadores de VM/LXC se exportan como referencias públicas y detalles es un objeto JSON.
// @Tags         Auditoría (Admin)
// @Produce      text/csv
// @Produce      application/json
// @Security     BearerAuth
// @Param        usuarioId   query string  false "Filtrar por UUID de usuario"
// @Param        accion      query string  false "Filtrar por tipo de acción"
// @Param        instanciaId query string false "Referencia pública inst_... de instancia"
// @Param        resultado   query string  false "PENDIENTE, SUCCESS, FAILED (infraestructura); EXITO o FALLA (otras acciones)"
// @Param        desde       query string  false "Fecha desde (RFC3339 o YYYY-MM-DD)"
// @Param        hasta       query string  false "Fecha hasta (RFC3339 o YYYY-MM-DD)"
// @Param        recursoTipo query string false "Tipo del recurso" Enums(VM,LXC,HOST,USER)
// @Param        recursoId query string false "Referencia pública para VM/LXC; identificador propio para HOST/USER"
// @Param        excluirAccion query []string false "Acciones a excluir" collectionFormat(multi)
// @Param        formato query string false "Formato de descarga; cualquier valor distinto de json produce CSV" default(csv)
// @Failure      400 {object} ErrorResponse "INVALID_QUERY"
// @Success      200 {file} binary "Archivo CSV o JSON descargable"
// @Failure      401 {object} map[string]string
// @Failure      403 {object} map[string]string
// @Router       /admin/audit/export [get]
func (h *AuditHandler) ExportarAuditoria(c *gin.Context) {
	orgID := extraerOrgID(c)
	filtros := parsearFiltrosAuditoria(c)
	opciones := parsearOpcionesAuditoria(c)
	if err := validarConsultaAuditoria(c, &filtros, &opciones); err != nil {
		SendError(c, http.StatusBadRequest, "INVALID_QUERY", err.Error())
		return
	}
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

	// excluirAccion acepta valores repetidos y separados por coma. El feed del
	// Dashboard lo usa para no traer el ruido del canal de eventos, que hoy
	// desplaza a los movimientos reales; la vista completa no lo manda y sigue
	// mostrando todo.
	for _, valor := range c.QueryArray("excluirAccion") {
		for _, parte := range strings.Split(valor, ",") {
			if accion := strings.TrimSpace(parte); accion != "" {
				filtros.ExcluirAcciones = append(filtros.ExcluirAcciones, accion)
			}
		}
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

// validarConsultaAuditoria rechaza filtros malformados en vez de ignorarlos.
// Los nombres anteriores se mantienen como alias para el frontend existente.
func validarConsultaAuditoria(c *gin.Context, f *ports.FiltrosAuditoria, o *ports.OpcionesAuditoria) error {
	if raw := c.Query("usuarioId"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return fmt.Errorf("usuarioId debe ser UUID")
		}
		f.UsuarioID = &id
	}
	for _, campo := range []string{"desde", "hasta"} {
		raw := c.Query(campo)
		if raw == "" {
			continue
		}
		fecha, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			fecha, err = time.Parse(time.DateOnly, raw)
			if err != nil {
				return fmt.Errorf("%s debe ser RFC3339 o YYYY-MM-DD", campo)
			}
			if campo == "hasta" {
				fecha = fecha.AddDate(0, 0, 1).Add(-time.Nanosecond)
			}
		}
		if campo == "desde" {
			f.Desde = &fecha
		} else {
			f.Hasta = &fecha
		}
	}
	if f.Desde != nil && f.Hasta != nil && f.Desde.After(*f.Hasta) {
		return fmt.Errorf("desde debe ser anterior o igual a hasta")
	}
	f.RecursoTipo, f.RecursoID = c.Query("recursoTipo"), c.Query("recursoId")
	switch f.RecursoTipo {
	case "", "VM", "LXC", "HOST", "USER":
	default:
		return fmt.Errorf("recursoTipo inválido")
	}
	switch f.Resultado {
	case "", "SUCCESS", "FAILED", "PENDIENTE", "EXITO", "FALLA":
	default:
		return fmt.Errorf("resultado inválido")
	}
	o.TamanoPag = 25
	for _, campo := range []string{"pagina", "page", "tamano", "limit"} {
		raw := c.Query(campo)
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return fmt.Errorf("%s debe ser un entero positivo", campo)
		}
		if campo == "pagina" || campo == "page" {
			o.Pagina = n
		} else {
			o.TamanoPag = n
		}
	}
	if o.TamanoPag > 100 {
		o.TamanoPag = 100
	}
	if o.Pagina-1 > int(^uint(0)>>1)/o.TamanoPag {
		return fmt.Errorf("page fuera de rango")
	}
	return nil
}
