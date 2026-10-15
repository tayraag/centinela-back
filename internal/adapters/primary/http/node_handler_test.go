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
	r.GET("/api/node/status", NewNodeHandler(svc, nil, "proxmox").ObtenerEstado)
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
		Allocatable:      ports.CuotaAsignable{MaxCPUCores: 6, FreeRamMB: 10717, FreeStorageGB: 59, HeadroomPercent: 15},
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
		esperadas := map[string]bool{"cpu": true, "ram": true, "storage": true, "allocatable": true, "uptimeSeconds": true, "instancesSummary": true, "stale": true, "fetchedAt": true}
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
		allocatable := cuerpo["allocatable"].(map[string]any)
		if allocatable["maxCpuCores"] != float64(6) || allocatable["freeRamMb"] != float64(10717) || allocatable["freeStorageGb"] != float64(59) || allocatable["headroomPercent"] != float64(15) {
			t.Errorf("allocatable: %v", allocatable)
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

// ==========================================
// Mocks para Storages y Templates
// ==========================================

type proxmoxPortMock struct {
	ports.ProxmoxPort
	storages        []ports.StorageDTO
	storagesErr     error
	contenido       map[string][]ports.StorageContentDTO
	contenidoErr    error
	nodoSolicitado  string
	storageConsulto string
}

func (m *proxmoxPortMock) ObtenerStorages(ctx context.Context, node string) ([]ports.StorageDTO, error) {
	m.nodoSolicitado = node
	if m.storagesErr != nil {
		return nil, m.storagesErr
	}
	return m.storages, nil
}

func (m *proxmoxPortMock) ObtenerContenidoStorage(ctx context.Context, node, storage string) ([]ports.StorageContentDTO, error) {
	m.storageConsulto = storage
	if m.contenidoErr != nil {
		return nil, m.contenidoErr
	}
	if m.contenido != nil {
		return m.contenido[storage], nil
	}
	return []ports.StorageContentDTO{}, nil
}

func TestNodeHandler_ObtenerStorages(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockPVE := &proxmoxPortMock{
		storages: []ports.StorageDTO{
			{
				Storage: "local-lvm",
				Type:    "lvmthin",
				Total:   500000000000,
				Avail:   320000000000,
				Active:  1,
				Content: "images, rootdir",
			},
			{
				Storage: "inactivo",
				Type:    "dir",
				Total:   100000,
				Avail:   50000,
				Active:  0, // no debe incluirse
				Content: "backup",
			},
		},
	}

	handler := NewNodeHandler(nil, mockPVE, "proxmox")
	r := gin.New()
	r.GET("/api/node/storages", handler.ObtenerStorages)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/node/storages", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("Se esperaba 200, vino %d", w.Code)
	}

	var res []StorageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("Error deserializando respuesta: %v", err)
	}

	if len(res) != 1 {
		t.Fatalf("Se esperaba 1 storage activo, vinieron %d", len(res))
	}

	st := res[0]
	if st.Name != "local-lvm" || st.Type != "lvmthin" || st.TotalBytes != 500000000000 || st.FreeBytes != 320000000000 {
		t.Errorf("Datos de storage incorrectos: %+v", st)
	}
	if len(st.ContentTypes) != 2 || st.ContentTypes[0] != "images" || st.ContentTypes[1] != "rootdir" {
		t.Errorf("ContentTypes incorrectos: %v", st.ContentTypes)
	}
}

func TestNodeHandler_ObtenerTemplates(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockPVE := &proxmoxPortMock{
		storages: []ports.StorageDTO{
			{
				Storage: "local",
				Active:  1,
				Content: "iso,vztmpl",
			},
			{
				Storage: "solo-backup",
				Active:  1,
				Content: "backup", // no debe consultarse
			},
		},
		contenido: map[string][]ports.StorageContentDTO{
			"local": {
				{
					Volid:   "local:iso/ubuntu-24.04.iso",
					Content: "iso",
					Size:    2100000000,
				},
				{
					Volid:   "local:vztmpl/debian-12-standard.tar.zst",
					Content: "vztmpl",
					Size:    130000000,
				},
				{
					Volid:   "local:backup/vzdump-100.tar.zst",
					Content: "backup", // debe ignorarse
					Size:    500000000,
				},
			},
		},
	}

	handler := NewNodeHandler(nil, mockPVE, "proxmox")
	r := gin.New()
	r.GET("/api/node/templates", handler.ObtenerTemplates)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/node/templates", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("Se esperaba 200, vino %d", w.Code)
	}

	var res []TemplateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("Error deserializando respuesta: %v", err)
	}

	if len(res) != 2 {
		t.Fatalf("Se esperaban 2 plantillas/ISOs, vinieron %d", len(res))
	}

	item0 := res[0]
	if item0.ID != "local:iso/ubuntu-24.04.iso" || item0.Name != "ubuntu-24.04.iso" || item0.Type != "ISO" || item0.SizeBytes != 2100000000 {
		t.Errorf("Template ISO incorrecto: %+v", item0)
	}

	item1 := res[1]
	if item1.ID != "local:vztmpl/debian-12-standard.tar.zst" || item1.Name != "debian-12-standard.tar.zst" || item1.Type != "LXC_TEMPLATE" || item1.SizeBytes != 130000000 {
		t.Errorf("Template LXC incorrecto: %+v", item1)
	}
}

func TestNodeHandler_ProxmoxUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockPVE := &proxmoxPortMock{
		storagesErr: ports.ErrProxmoxNoDisponible,
	}

	handler := NewNodeHandler(nil, mockPVE, "proxmox")
	r := gin.New()
	r.GET("/api/node/storages", handler.ObtenerStorages)
	r.GET("/api/node/templates", handler.ObtenerTemplates)

	for _, path := range []string{"/api/node/storages", "/api/node/templates"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusBadGateway {
			t.Errorf("Se esperaba 502 Bad Gateway para %s, vino %d", path, w.Code)
		}
		var errRes ErrorResponse
		_ = json.Unmarshal(w.Body.Bytes(), &errRes)
		if errRes.ErrorCode != "PROXMOX_UNAVAILABLE" {
			t.Errorf("Se esperaba errorCode PROXMOX_UNAVAILABLE para %s, vino %s", path, errRes.ErrorCode)
		}
	}
}

func TestNodeHandler_RBAC(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockPVE := &proxmoxPortMock{
		storages: []ports.StorageDTO{
			{Storage: "local", Active: 1, Content: "iso"},
		},
	}
	handler := NewNodeHandler(nil, mockPVE, "proxmox")

	setupRouterConRol := func(rol string) *gin.Engine {
		r := gin.New()
		// Middleware simulando RequireAuth + inyección de claims
		r.Use(func(c *gin.Context) {
			c.Set("rol", rol)
			c.Next()
		})
		r.GET("/api/node/storages", func(c *gin.Context) {
			if c.GetString("rol") != "ADMIN" {
				SendError(c, http.StatusForbidden, "INSUFFICIENT_PERMISSIONS", "Se requieren permisos de administrador.")
				return
			}
			handler.ObtenerStorages(c)
		})
		r.GET("/api/node/templates", func(c *gin.Context) {
			if c.GetString("rol") != "ADMIN" {
				SendError(c, http.StatusForbidden, "INSUFFICIENT_PERMISSIONS", "Se requieren permisos de administrador.")
				return
			}
			handler.ObtenerTemplates(c)
		})
		return r
	}

	// 1. Rol OPERATOR debe recibir 403 Forbidden
	routerOperator := setupRouterConRol("OPERATOR")
	for _, ruta := range []string{"/api/node/storages", "/api/node/templates"} {
		w := httptest.NewRecorder()
		routerOperator.ServeHTTP(w, httptest.NewRequest(http.MethodGet, ruta, nil))
		if w.Code != http.StatusForbidden {
			t.Errorf("OPERATOR en %s esperaba 403 Forbidden, vino %d", ruta, w.Code)
		}
	}

	// 2. Rol ADMIN debe recibir 200 OK
	routerAdmin := setupRouterConRol("ADMIN")
	for _, ruta := range []string{"/api/node/storages", "/api/node/templates"} {
		w := httptest.NewRecorder()
		routerAdmin.ServeHTTP(w, httptest.NewRequest(http.MethodGet, ruta, nil))
		if w.Code != http.StatusOK {
			t.Errorf("ADMIN en %s esperaba 200 OK, vino %d", ruta, w.Code)
		}
	}
}
