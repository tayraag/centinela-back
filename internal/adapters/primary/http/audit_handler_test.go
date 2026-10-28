package http_test

import (
	"el-centinela/internal/adapters/primary/http/middleware"
	"time"

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

func TestAuditHandlerExcluirAccion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	casos := []struct {
		nombre   string
		query    string
		esperado []string
	}{
		{"un solo valor", "excluirAccion=EVENTOS_CONEXION", []string{"EVENTOS_CONEXION"}},
		{"separado por comas", "excluirAccion=EVENTOS_CONEXION,EVENTOS_CIERRE", []string{"EVENTOS_CONEXION", "EVENTOS_CIERRE"}},
		{"repetido en la query", "excluirAccion=EVENTOS_CONEXION&excluirAccion=EVENTOS_CIERRE", []string{"EVENTOS_CONEXION", "EVENTOS_CIERRE"}},
		{"con espacios y vacíos", "excluirAccion=%20EVENTOS_CONEXION%20,,EVENTOS_CIERRE", []string{"EVENTOS_CONEXION", "EVENTOS_CIERRE"}},
		{"sin exclusión no filtra nada", "accion=LOGIN", nil},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			service := &auditFiltros{}
			router := gin.New()
			router.GET("/api/admin/audit", handlers.NewAuditHandler(service).ListarAuditoria)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/audit?"+caso.query, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("status inesperado: %d", w.Code)
			}
			obtenido := service.filtros.ExcluirAcciones
			if len(obtenido) != len(caso.esperado) {
				t.Fatalf("ExcluirAcciones = %v, se esperaba %v", obtenido, caso.esperado)
			}
			for i := range caso.esperado {
				if obtenido[i] != caso.esperado[i] {
					t.Fatalf("ExcluirAcciones = %v, se esperaba %v", obtenido, caso.esperado)
				}
			}
		})
	}
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

func TestRF08ConsultaValidada(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, query := range []string{"usuarioId=incorrecto", "desde=ayer", "desde=2026-10-21&hasta=2026-10-20", "recursoTipo=SQL", "resultado=OK", "page=0", "limit=-1"} {
		t.Run(query, func(t *testing.T) {
			r := gin.New()
			r.GET("/audit", handlers.NewAuditHandler(&auditFiltros{}).ListarAuditoria)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/audit?"+query, nil))
			if w.Code != 400 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
	svc := &auditFiltros{}
	r := gin.New()
	r.GET("/audit", handlers.NewAuditHandler(svc).ListarAuditoria)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/audit?desde=2026-10-01T10:30:00Z&hasta=2026-10-20&recursoTipo=VM&recursoId=105&limit=200&page=2", nil))
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	if svc.filtros.RecursoTipo != "VM" || svc.filtros.RecursoID != "105" || svc.filtros.Desde.Hour() != 10 || svc.filtros.Hasta.Nanosecond() != 999999999 {
		t.Fatalf("filtros=%+v", svc.filtros)
	}
	if !svc.filtros.Hasta.Before(time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("hasta fuera de rango")
	}
	var body map[string]any
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["page"] != float64(2) || body["limit"] != float64(100) {
		t.Fatalf("respuesta=%v", body)
	}
}
func TestRF08OperatorForbidden(t *testing.T) {
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.ContextKeyRol, "OPERATOR") })
	r.GET("/api/admin/audit", middleware.RequireRole("ADMIN"), handlers.NewAuditHandler(&auditFiltros{}).ListarAuditoria)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/audit", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestRF08ContratoPaginado(t *testing.T) {
	svc := &auditFiltros{pagina: &ports.PaginaAuditoria{Total: 1420, Items: []ports.AuditoriaDTO{{NombreUsuario: "admin", RecursoTipo: "VM", RecursoID: "105", Detalles: json.RawMessage(`{"snapname":"pre-deploy"}`)}}}}
	r := gin.New()
	r.GET("/audit", handlers.NewAuditHandler(svc).ListarAuditoria)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/audit", nil))
	var body struct {
		Page, Limit, TotalPages int
		Items                   []struct {
			UsuarioNombre, RecursoTipo, RecursoId string
			Detalle                               map[string]any
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Page != 1 || body.Limit != 25 || body.TotalPages != 57 || len(body.Items) != 1 {
		t.Fatalf("body=%s", w.Body)
	}
	item := body.Items[0]
	if item.UsuarioNombre != "admin" || item.RecursoTipo != "VM" || item.RecursoId != "105" || item.Detalle["snapname"] != "pre-deploy" {
		t.Fatalf("item=%+v", item)
	}
}
