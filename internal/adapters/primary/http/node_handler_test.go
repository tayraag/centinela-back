package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
)

type nodoServiceFalso struct {
	estado *ports.EstadoNodo
	stale  bool
	err    error
}

func (f nodoServiceFalso) ObtenerEstado(context.Context) (*ports.EstadoNodo, bool, error) {
	return f.estado, f.stale, f.err
}

func pedirEstadoNodo(t *testing.T, svc ports.NodoService) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/node/status", NewNodeHandler(svc).ObtenerEstado)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/node/status", nil))
	var cuerpo map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &cuerpo)
	return w.Code, cuerpo
}

func TestEstadoNodo_RespetaElContrato(t *testing.T) {
	estado := &ports.EstadoNodo{
		CPU:              ports.MetricaCPU{UsagePercent: 3.77, Cores: 12},
		RAM:              ports.MetricaCapacidad{UsedGb: 2.53, TotalGb: 15.3, UsagePercent: 16.57},
		Storage:          ports.MetricaCapacidad{UsedGb: 5.1, TotalGb: 67.73, UsagePercent: 7.53},
		UptimeSeconds:    199252,
		InstancesSummary: ports.ResumenInstancias{VMs: ports.ResumenEstado{Running: 1, Total: 1}},
		FetchedAt:        time.Date(2026, 10, 5, 12, 0, 0, 0, time.FixedZone("ART", -3*3600)),
	}
	for _, stale := range []bool{false, true} {
		status, cuerpo := pedirEstadoNodo(t, nodoServiceFalso{estado: estado, stale: stale})
		if status != 200 {
			t.Fatalf("status %d", status)
		}
		// Exactamente las claves del contrato (docs/contrato-etapa1.md), ni más ni menos.
		esperadas := map[string]bool{"cpu": true, "ram": true, "storage": true, "uptimeSeconds": true, "instancesSummary": true, "stale": true, "fetchedAt": true}
		for k := range cuerpo {
			if !esperadas[k] {
				t.Errorf("Clave fuera del contrato: %q", k)
			}
			delete(esperadas, k)
		}
		if len(esperadas) > 0 {
			t.Errorf("Faltan claves del contrato: %v", esperadas)
		}
		if cuerpo["stale"] != stale {
			t.Errorf("stale = %v, se esperaba %v", cuerpo["stale"], stale)
		}
		if cuerpo["fetchedAt"] != "2026-10-05T15:00:00Z" {
			t.Errorf("fetchedAt debe ser RFC3339 en UTC: %v", cuerpo["fetchedAt"])
		}
		ram := cuerpo["ram"].(map[string]any)
		if ram["usedGb"] != 2.53 || ram["totalGb"] != 15.3 || ram["usagePercent"] != 16.57 {
			t.Errorf("ram: %v", ram)
		}
		vms := cuerpo["instancesSummary"].(map[string]any)["vms"].(map[string]any)
		for _, k := range []string{"running", "stopped", "paused", "total"} {
			if _, ok := vms[k]; !ok {
				t.Errorf("instancesSummary.vms debe traer %q aunque sea 0: %v", k, vms)
			}
		}
	}
}

func TestEstadoNodo_ErroresSinLecturaPrevia(t *testing.T) {
	casos := map[int]error{
		http.StatusBadGateway:     ports.ErrProxmoxNoDisponible,
		http.StatusGatewayTimeout: errors.Join(ports.ErrProxmoxNoDisponible, ports.ErrProxmoxTimeout),
	}
	codigos := map[int]string{http.StatusBadGateway: "PROXMOX_UNAVAILABLE", http.StatusGatewayTimeout: "PROXMOX_TIMEOUT"}
	for status, err := range casos {
		got, cuerpo := pedirEstadoNodo(t, nodoServiceFalso{err: err})
		if got != status || cuerpo["errorCode"] != codigos[status] {
			t.Errorf("Se esperaba %d %s, vino %d %v", status, codigos[status], got, cuerpo)
		}
	}
}
