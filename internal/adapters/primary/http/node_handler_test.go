package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type nodoServiceFalso struct {
	estado *ports.EstadoNodo
	stale  bool
	err    error

	historial    []ports.PuntoHistorialNodo
	errHistorial error
}

func (f nodoServiceFalso) ObtenerEstado(context.Context) (*ports.EstadoNodo, bool, error) {
	return f.estado, f.stale, f.err
}

func (f nodoServiceFalso) ObtenerHistorial(context.Context) ([]ports.PuntoHistorialNodo, error) {
	return f.historial, f.errHistorial
}

// pedirEstadoNodo pide el estado como ADMIN (la vista completa del host).
func pedirEstadoNodo(t *testing.T, svc ports.NodoService) (int, map[string]any) {
	t.Helper()
	status, crudo := pedirNodoComo(t, svc, "ADMIN", nil, "/api/node/status")
	var cuerpo map[string]any
	_ = json.Unmarshal(crudo, &cuerpo)
	return status, cuerpo
}

// permisosFalsos devuelve los VMID asignados al usuario.
type permisosFalsos struct {
	vmids []int
	err   error
}

func (p permisosFalsos) ListarPermisosConNivel(context.Context, uuid.UUID) ([]ports.PermisoInstanciaInput, error) {
	permisos := make([]ports.PermisoInstanciaInput, len(p.vmids))
	for i, v := range p.vmids {
		permisos[i] = ports.PermisoInstanciaInput{Vmid: v, NivelAcceso: ports.NivelAccesoReadOnly}
	}
	return permisos, p.err
}

func pedirNodoComo(t *testing.T, svc ports.NodoService, rol string, permisos permisosDeUsuario, ruta string) (int, []byte) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewNodeHandler(svc, nil, "proxmox", permisos)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.ContextKeyRol, rol)
		c.Set(middleware.ContextKeyUserID, uuid.New().String())
	})
	r.GET("/api/node/status", h.ObtenerEstado)
	r.GET("/api/node/history", h.ObtenerHistorial)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, ruta, nil))
	return w.Code, w.Body.Bytes()
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

	handler := NewNodeHandler(nil, mockPVE, "proxmox", nil)
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

	handler := NewNodeHandler(nil, mockPVE, "proxmox", nil)
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

	handler := NewNodeHandler(nil, mockPVE, "proxmox", nil)
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
	handler := NewNodeHandler(nil, mockPVE, "proxmox", nil)

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

// estadoConInventario: dos VM y dos LXC en el nodo; el OPERATOR de los tests
// tiene asignadas solo la 110 (VM, running) y la 201 (LXC, stopped).
func estadoConInventario() *ports.EstadoNodo {
	instancias := []ports.InstanciaDelNodo{
		{Vmid: 100, Tipo: "qemu", Estado: "running"}, {Vmid: 110, Tipo: "qemu", Estado: "running"},
		{Vmid: 101, Tipo: "lxc", Estado: "running"}, {Vmid: 201, Tipo: "lxc", Estado: "stopped"},
	}
	return &ports.EstadoNodo{
		CPU:              ports.MetricaCPU{UsagePercent: 3.77, Cores: 12},
		RAM:              ports.MetricaCapacidad{UsedGb: 2.53, TotalGb: 15.3, UsagePercent: 16.57},
		Storage:          ports.MetricaCapacidad{UsedGb: 5.1, TotalGb: 67.73, UsagePercent: 7.53},
		Allocatable:      ports.CuotaAsignable{MaxCPUCores: 6, FreeRamMB: 10717, FreeStorageGB: 59, HeadroomPercent: 15},
		InstancesSummary: ports.ResumirInstancias(instancias, nil),
		FetchedAt:        time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
		Instancias:       instancias,
	}
}

// DoD: el OPERATOR no recibe el disco físico ni los contadores de instancias
// ajenas; el ADMIN conserva la vista global.
func TestEstadoNodo_SegregadoPorRol(t *testing.T) {
	svc := nodoServiceFalso{estado: estadoConInventario()}

	status, crudo := pedirNodoComo(t, svc, "OPERATOR", permisosFalsos{vmids: []int{110, 201}}, "/api/node/status")
	var op map[string]any
	_ = json.Unmarshal(crudo, &op)
	if status != 200 {
		t.Fatalf("OPERATOR: %d %s", status, crudo)
	}
	if v, presente := op["storage"]; !presente || v != nil {
		t.Errorf("OPERATOR: storage debe venir en null: %v", op["storage"])
	}
	if v, presente := op["allocatable"].(map[string]any)["freeStorageGb"]; !presente || v != nil {
		t.Errorf("OPERATOR: allocatable.freeStorageGb debe venir en null: %v", op["allocatable"])
	}
	resumen, _ := json.Marshal(op["instancesSummary"])
	if string(resumen) != `{"lxc":{"paused":0,"running":0,"stopped":1,"total":1},"vms":{"paused":0,"running":1,"stopped":0,"total":1}}` {
		t.Errorf("OPERATOR: instancesSummary debe contar solo sus instancias: %s", resumen)
	}
	if op["cpu"] == nil || op["ram"] == nil || op["uptimeSeconds"] == nil {
		t.Errorf("OPERATOR: CPU, RAM y uptime del host se mantienen: %v", op)
	}
	if strings.Contains(string(crudo), "instancias") || strings.Contains(string(crudo), "67.73") {
		t.Errorf("OPERATOR: no debe filtrar el inventario ni el disco: %s", crudo)
	}

	_, admin := pedirEstadoNodo(t, svc)
	if admin["storage"].(map[string]any)["totalGb"] != 67.73 || admin["allocatable"].(map[string]any)["freeStorageGb"] != float64(59) {
		t.Errorf("ADMIN: conserva el disco: %v", admin)
	}
	if admin["instancesSummary"].(map[string]any)["vms"].(map[string]any)["total"] != float64(2) || admin["instancesSummary"].(map[string]any)["lxc"].(map[string]any)["total"] != float64(2) {
		t.Errorf("ADMIN: instancesSummary global: %v", admin["instancesSummary"])
	}
	if _, filtra := admin["instancias"]; filtra {
		t.Error("ADMIN: la foto interna del inventario tampoco se publica")
	}
}

func TestEstadoNodo_OperatorSinAsignacionesCuentaCero(t *testing.T) {
	_, crudo := pedirNodoComo(t, nodoServiceFalso{estado: estadoConInventario()}, "OPERATOR", permisosFalsos{}, "/api/node/status")
	var op map[string]any
	_ = json.Unmarshal(crudo, &op)
	resumen, _ := json.Marshal(op["instancesSummary"])
	if string(resumen) != `{"lxc":{"paused":0,"running":0,"stopped":0,"total":0},"vms":{"paused":0,"running":0,"stopped":0,"total":0}}` {
		t.Errorf("Sin asignaciones no cuenta nada: %s", resumen)
	}
}

// Sin rol en el contexto (no debería pasar detrás de RequireAuth) se trata
// como restringido, y si no se pueden leer los permisos no se responde nada.
func TestEstadoNodo_FailClosed(t *testing.T) {
	svc := nodoServiceFalso{estado: estadoConInventario()}
	_, crudo := pedirNodoComo(t, svc, "", permisosFalsos{}, "/api/node/status")
	if strings.Contains(string(crudo), "67.73") {
		t.Errorf("Sin rol no debe ver el disco: %s", crudo)
	}
	status, crudo := pedirNodoComo(t, svc, "OPERATOR", permisosFalsos{err: errors.New("base caída")}, "/api/node/status")
	if status != http.StatusInternalServerError || strings.Contains(string(crudo), "cpu") {
		t.Errorf("Sin permisos legibles: se esperaba 500 sin datos, vino %d %s", status, crudo)
	}
}

func TestHistorialNodo_SinDiscoParaOperator(t *testing.T) {
	cincuenta := 50.0
	svc := nodoServiceFalso{historial: []ports.PuntoHistorialNodo{{Timestamp: 1, CPUPercent: 10, RAMPercent: &cincuenta, StoragePercent: &cincuenta}}}
	_, op := pedirNodoComo(t, svc, "OPERATOR", permisosFalsos{}, "/api/node/history")
	if strings.Contains(string(op), "storagePercent") || !strings.Contains(string(op), "ramPercent") {
		t.Errorf("OPERATOR: sin storagePercent, con CPU y RAM: %s", op)
	}
	_, admin := pedirNodoComo(t, svc, "ADMIN", nil, "/api/node/history")
	if !strings.Contains(string(admin), `"storagePercent":50`) {
		t.Errorf("ADMIN: conserva storagePercent: %s", admin)
	}
	if svc.historial[0].StoragePercent == nil {
		t.Error("No debe modificarse la serie original (puede venir de la caché)")
	}
}
