package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	handlers "el-centinela/internal/adapters/primary/http"
	"el-centinela/internal/core/ports"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type auditFiltros struct {
	ports.AuditService
	filtros ports.FiltrosAuditoria
	pagina  *ports.PaginaAuditoria
}

func (a *auditFiltros) ListarAuditoria(_ context.Context, _ uuid.UUID, filtros ports.FiltrosAuditoria, _ ports.OpcionesAuditoria) (*ports.PaginaAuditoria, error) {
	a.filtros = filtros
	if a.pagina != nil {
		return a.pagina, nil
	}
	return &ports.PaginaAuditoria{Items: []ports.AuditoriaDTO{}}, nil
}

func TestAuditHandlerFiltrosRF08(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, caso := range []struct{ accion, resultado string }{
		{ports.AccionCrearInstancia, "PENDIENTE"},
		{ports.AccionCrearInstancia, "SUCCESS"},
		{ports.AccionCrearInstancia, "FAILED"},
		{ports.AccionConfigurarInstancia, "SUCCESS"},
		{ports.AccionConfigurarInstancia, "FAILED"},
	} {
		t.Run(caso.accion+"/"+caso.resultado, func(t *testing.T) {
			service := &auditFiltros{}
			router := gin.New()
			router.GET("/api/admin/audit", handlers.NewAuditHandler(service).ListarAuditoria)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/audit?accion="+caso.accion+"&resultado="+caso.resultado, nil))
			if w.Code != http.StatusOK || service.filtros.Accion != caso.accion || service.filtros.Resultado != caso.resultado {
				t.Fatalf("filtros perdidos: status=%d filtros=%+v", w.Code, service.filtros)
			}
		})
	}
}

func TestAuditHandlerDevuelveDetallesComoObjetoJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &auditFiltros{pagina: &ports.PaginaAuditoria{
		Total: 1, Pagina: 1,
		Items: []ports.AuditoriaDTO{{
			ID: uuid.New(), Accion: ports.AccionCrearInstancia, Resultado: ports.ResultadoPendiente,
			Detalles: json.RawMessage(`{"tareaId":"tarea-123","cores":4}`),
		}},
	}}
	router := gin.New()
	router.GET("/api/admin/audit", handlers.NewAuditHandler(service).ListarAuditoria)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/audit", nil))

	var respuesta struct {
		Items []struct {
			Detalles map[string]any `json:"detalles"`
		} `json:"items"`
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status inesperado: %d", w.Code)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &respuesta); err != nil {
		t.Fatalf("la respuesta no contiene detalles como objeto JSON: %v; body=%s", err, w.Body.String())
	}
	if len(respuesta.Items) != 1 || respuesta.Items[0].Detalles["tareaId"] != "tarea-123" || respuesta.Items[0].Detalles["cores"] != float64(4) {
		t.Fatalf("detalles incompletos: %+v", respuesta.Items)
	}
}
