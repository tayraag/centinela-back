package http_test

import (
	"context"
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
}

func (a *auditFiltros) ListarAuditoria(_ context.Context, _ uuid.UUID, filtros ports.FiltrosAuditoria, _ ports.OpcionesAuditoria) (*ports.PaginaAuditoria, error) {
	a.filtros = filtros
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
	for _, resultado := range []string{"", "PENDIENTE", "SUCCESS", "FAILED"} {
		t.Run(resultado, func(t *testing.T) {
			service := &auditFiltros{}
			router := gin.New()
			router.GET("/api/admin/audit", handlers.NewAuditHandler(service).ListarAuditoria)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/audit?accion=INICIAR_VM&resultado="+resultado, nil))
			if w.Code != http.StatusOK || service.filtros.Accion != "INICIAR_VM" || service.filtros.Resultado != resultado {
				t.Fatalf("filtros perdidos: status=%d filtros=%+v", w.Code, service.filtros)
			}
		})
	}
}
