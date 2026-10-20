package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	handlers "el-centinela/internal/adapters/primary/http"
	"el-centinela/internal/adapters/primary/http/middleware"
	redisstore "el-centinela/internal/adapters/secondary/redis"
	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type metricsProxmox struct {
	mockProxmoxPort
	calls     int
	timeframe string
	data      []domain.PuntoMetricaDTO
	err       error
}

func (p *metricsProxmox) ObtenerMetricasRRD(_ context.Context, node, kind string, vmid int, timeframe string) ([]domain.PuntoMetricaDTO, error) {
	p.calls++
	p.timeframe = timeframe
	if node != "pve1" || kind != "lxc" || vmid != 110 {
		panic("destino RRD incorrecto")
	}
	return p.data, p.err
}

type metricsAccess struct {
	ports.InstanceRepository
	allowed bool
}

func (a metricsAccess) VerificarAcceso(_ context.Context, _ uuid.UUID, vmid int, level string) (bool, error) {
	if vmid != 110 || level != ports.NivelAccesoReadOnly {
		panic("permiso incorrecto")
	}
	return a.allowed, nil
}

func metricsRouter(p *metricsProxmox, store ports.KeyValueStore, allowed bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.ContextKeyRol, "OPERATOR")
		c.Set(middleware.ContextKeyUserID, uuid.NewString())
	})
	r.GET("/api/instances/:instanceRef/metrics", middleware.ResolveInstanceRef(handlers.RefsPrueba, "instanceRef"), middleware.RequireInstanceAccess(metricsAccess{allowed: allowed}, ports.NivelAccesoReadOnly), handlers.NewInstanceMetricsHandler(p, store).ObtenerMetricas)
	return r
}

func TestInstanceMetricsCache(t *testing.T) {
	for _, tc := range []struct{ query, timeframe, rrd string }{{"", "2h", "hour"}, {"?timeframe=2h", "2h", "hour"}, {"?timeframe=24h", "24h", "day"}} {
		t.Run(tc.query, func(t *testing.T) {
			srv := miniredis.RunT(t)
			store := redisstore.Nuevo(redisstore.Config{Addr: srv.Addr()})
			cpu, ram := 14.5, int64(1073741824)
			point := domain.PuntoMetricaDTO{Timestamp: 1729000000, CPUPercent: &cpu, RAMUsedBytes: &ram, RAMTotalBytes: 2147483648, NetInBps: 1024, NetOutBps: 2048, DiskWriteBps: 4096}
			p := &metricsProxmox{mockProxmoxPort: mockProxmoxPort{nodoDetalle: "pve1"}, data: []domain.PuntoMetricaDTO{point}}
			r := metricsRouter(p, store, true)
			request := func(cache string) {
				t.Helper()
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest("GET", "/api/instances/"+handlers.RefInst(110)+"/metrics"+tc.query, nil))
				if w.Code != 200 || w.Header().Get("X-Cache") != cache {
					t.Fatalf("%d %v %s", w.Code, w.Header(), w.Body)
				}
				var response handlers.InstanceMetricsResponse
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.VMID != handlers.RefInst(110) || response.Timeframe != tc.timeframe || len(response.Data) != 1 {
					t.Fatalf("respuesta incorrecta: %+v", response)
				}
				got, _ := json.Marshal(response.Data[0])
				want, _ := json.Marshal(point)
				if string(got) != string(want) {
					t.Fatalf("métricas: %s != %s", got, want)
				}
			}
			request("MISS")
			if srv.TTL("cache:instance:110:metrics:"+tc.timeframe) != 30*time.Second {
				t.Fatal("TTL incorrecto")
			}
			srv.FastForward(29 * time.Second)
			request("HIT")
			if p.calls != 1 || p.lecturas != 1 || p.timeframe != tc.rrd {
				t.Fatalf("consultas inesperadas: %+v", p)
			}
			srv.FastForward(time.Second)
			request("MISS")
			if p.calls != 2 {
				t.Fatal("no renovó al vencer")
			}
		})
	}
}

func TestInstanceMetricsValidationBeforeDependencies(t *testing.T) {
	for _, tc := range []struct {
		query   string
		allowed bool
		status  int
		code    string
	}{
		{"?timeframe=week", true, 400, "INVALID_TIMEFRAME"},
		{"?timeframe=", true, 400, "INVALID_TIMEFRAME"},
		{"", false, 403, "INSTANCE_ACCESS_DENIED"},
	} {
		p := &metricsProxmox{}
		r := metricsRouter(p, nil, tc.allowed) // Redis nil: cualquier interacción falla la prueba.
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/api/instances/"+handlers.RefInst(110)+"/metrics"+tc.query, nil))
		var body map[string]string
		json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != tc.status || body["errorCode"] != tc.code || p.calls != 0 || p.lecturas != 0 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		if tc.status == 400 && body["message"] != "El timeframe debe ser '2h' o '24h'" {
			t.Fatal(body)
		}
	}
}

func TestInstanceMetricsEmptyAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		srv := miniredis.RunT(t)
		store := redisstore.Nuevo(redisstore.Config{Addr: srv.Addr()})
		p := &metricsProxmox{mockProxmoxPort: mockProxmoxPort{nodoDetalle: "pve1"}}
		if fail {
			p.err = ports.ErrProxmoxNoDisponible
		}
		r := metricsRouter(p, store, true)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/api/instances/"+handlers.RefInst(110)+"/metrics", nil))
		if fail {
			if w.Code != 502 || srv.Exists("cache:instance:110:metrics:2h") {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
		} else {
			var response handlers.InstanceMetricsResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Data == nil || w.Code != 200 {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if _, err := store.Get(context.Background(), "missing"); !errors.Is(err, ports.ErrClaveNoEncontrada) {
				t.Fatal(err)
			}
		}
	}
}
