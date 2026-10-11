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
