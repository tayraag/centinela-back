package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adaptersHttp "el-centinela/internal/adapters/primary/http"
	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// mockProxmoxPort implementa ports.ProxmoxPort para pruebas
type mockProxmoxPort struct {
	instancias []ports.InstanciaProxmoxDTO
	errListar  error
	errAccion  error // error de IniciarInstancia / DetenerInstancia
	errDetalle error // error de ObtenerInstancia
}

func (m *mockProxmoxPort) ObtenerInstancia(ctx context.Context, vmid int) (*ports.InstanciaProxmoxDTO, error) {
	if m.errDetalle != nil {
		return nil, m.errDetalle
	}
	return &ports.InstanciaProxmoxDTO{Vmid: vmid}, nil
}

func (m *mockProxmoxPort) ListarInstancias(ctx context.Context) ([]ports.InstanciaProxmoxDTO, error) {
	if m.errListar != nil {
		return nil, m.errListar
	}
	return m.instancias, nil
}

func (m *mockProxmoxPort) IniciarInstancia(ctx context.Context, vmid int) (string, error) {
	if m.errAccion != nil {
		return "", m.errAccion
	}
	return "UPID:test:start", nil
}

func (m *mockProxmoxPort) DetenerInstancia(ctx context.Context, vmid int) (string, error) {
	if m.errAccion != nil {
		return "", m.errAccion
	}
	return "UPID:test:stop", nil
}

func (m *mockProxmoxPort) EstadoTarea(ctx context.Context, upid string) (*ports.EstadoTareaDTO, error) {
	return &ports.EstadoTareaDTO{Terminada: true, ExitStatus: "OK"}, nil
}

func (m *mockProxmoxPort) ReiniciarInstancia(ctx context.Context, vmid int) (string, error) {
	if m.errAccion != nil {
		return "", m.errAccion
	}
	return "UPID:test:reboot", nil
}

func (m *mockProxmoxPort) EliminarInstancia(ctx context.Context, vmid int) error {
	return m.errAccion
}

// seguimientoNulo implementa ports.SeguimientoTareas sin hacer nada.
type seguimientoNulo struct{}

func (seguimientoNulo) Seguir(context.Context, uuid.UUID, int, string, string) (uuid.UUID, error) {
	return uuid.New(), nil
}

type mockTareaRepository struct{}
func (m *mockTareaRepository) Crear(context.Context, *domain.TareaAsincrona) error { return nil }
func (m *mockTareaRepository) ActualizarEstado(context.Context, uuid.UUID, string) error { return nil }
func (m *mockTareaRepository) BuscarTareasActivasPorVmids(context.Context, []int) (map[int]string, error) {
	return map[int]string{}, nil
}

type mockAuditService struct{}
func (m *mockAuditService) Registrar(context.Context, ports.RegistrarAuditoriaInput) {}
func (m *mockAuditService) ListarAuditoria(context.Context, uuid.UUID, ports.FiltrosAuditoria, ports.OpcionesAuditoria) (*ports.PaginaAuditoria, error) { return nil, nil }
func (m *mockAuditService) ExportarCSV(context.Context, uuid.UUID, ports.FiltrosAuditoria) ([]byte, error) { return nil, nil }
func (m *mockAuditService) ExportarJSON(context.Context, uuid.UUID, ports.FiltrosAuditoria) ([]byte, error) { return nil, nil }


// mockUserRepository implementa ports.UserRepository para pruebas
type mockUserRepository struct {
	permisosVMIDs []int
}

func (m *mockUserRepository) ListarUsuarios(ctx context.Context, orgID uuid.UUID, filtros ports.FiltrosUsuario) (*ports.ListaUsuariosResult, error) {
	return nil, nil
}
func (m *mockUserRepository) BuscarUsuarioPorIDEnOrg(ctx context.Context, id, orgID uuid.UUID) (*domain.Usuario, error) {
	return nil, nil
}
func (m *mockUserRepository) ExisteEmailEnOrg(ctx context.Context, email string, orgID uuid.UUID, excluirID *uuid.UUID) (bool, error) {
	return false, nil
}
func (m *mockUserRepository) ExisteUsernameEnOrg(ctx context.Context, username string, orgID uuid.UUID, excluirID *uuid.UUID) (bool, error) {
	return false, nil
}
func (m *mockUserRepository) CrearUsuario(ctx context.Context, u *domain.Usuario) error {
	return nil
}
func (m *mockUserRepository) ActualizarUsuario(ctx context.Context, id uuid.UUID, cambios map[string]any) error {
	return nil
}
func (m *mockUserRepository) ListarPermisosDeUsuario(ctx context.Context, usuarioID uuid.UUID) ([]int, error) {
	return m.permisosVMIDs, nil
}
func (m *mockUserRepository) ListarPermisosConNivel(ctx context.Context, usuarioID uuid.UUID) ([]ports.PermisoInstanciaInput, error) {
	var res []ports.PermisoInstanciaInput
	for _, vmid := range m.permisosVMIDs {
		res = append(res, ports.PermisoInstanciaInput{Vmid: vmid, NivelAcceso: ports.NivelAccesoFullAccess})
	}
	return res, nil
}
func (m *mockUserRepository) ReemplazarPermisos(ctx context.Context, usuarioID uuid.UUID, permisos []ports.PermisoInstanciaInput) error {
	return nil
}
func (m *mockUserRepository) ListarActividadDeUsuario(ctx context.Context, usuarioID uuid.UUID, filtros ports.FiltrosActividad) ([]domain.Auditoria, error) {
	return nil, nil
}

func TestListarInstancias_AdminVisualizaTodas(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockPx := &mockProxmoxPort{
		instancias: []ports.InstanciaProxmoxDTO{
			{Vmid: 100, Nombre: "vm-1", Tipo: "qemu", Nodo: "pve1", Estado: "running"},
			{Vmid: 101, Nombre: "ct-1", Tipo: "lxc", Nodo: "pve1", Estado: "stopped"},
			{Vmid: 102, Nombre: "vm-2", Tipo: "qemu", Nodo: "pve1", Estado: "running"},
		},
	}
	mockRepo := &mockUserRepository{
		permisosVMIDs: []int{100}, // Aunque solo tenga 100 en la BD, por ser ADMIN debe ver todas
	}

	handler := adaptersHttp.NewInstanceHandler(mockPx, mockRepo, seguimientoNulo{}, &mockTareaRepository{}, &mockAuditService{})

	router := gin.New()
	router.GET("/api/instances", func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyRol), "ADMIN")
		c.Set(string(middleware.ContextKeyUserID), uuid.New().String())
	}, handler.ListarInstancias)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/instances", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Código de estado esperado 200, obtenido %d", w.Code)
	}

	var res struct {
		Instances []ports.InstanciaListadaDTO `json:"instances"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("Error deserializando respuesta: %v", err)
	}

	if len(res.Instances) != 3 {
		t.Fatalf("ADMIN debe ver las 3 instancias (100%%), pero recibió %d", len(res.Instances))
	}

	// Comprobar normalización: qemu -> vm
	if res.Instances[0].Type != "vm" || res.Instances[1].Type != "lxc" {
		t.Errorf("Normalización incorrecta: %+v", res.Instances)
	}
}

func TestListarInstancias_OperatorVisualizaSoloAsignadas(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockPx := &mockProxmoxPort{
		instancias: []ports.InstanciaProxmoxDTO{
			{Vmid: 100, Nombre: "vm-1", Tipo: "qemu", Nodo: "pve1", Estado: "running"},
			{Vmid: 101, Nombre: "ct-1", Tipo: "lxc", Nodo: "pve1", Estado: "stopped"},
			{Vmid: 102, Nombre: "vm-2", Tipo: "qemu", Nodo: "pve1", Estado: "running"},
		},
	}
	mockRepo := &mockUserRepository{
		permisosVMIDs: []int{101}, // El operador solo tiene asignada la instancia 101
	}

	handler := adaptersHttp.NewInstanceHandler(mockPx, mockRepo, seguimientoNulo{}, &mockTareaRepository{}, &mockAuditService{})

	router := gin.New()
	router.GET("/api/instances", func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyRol), "OPERATOR")
		c.Set(string(middleware.ContextKeyUserID), uuid.New().String())
	}, handler.ListarInstancias)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/instances", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Código de estado esperado 200, obtenido %d", w.Code)
	}

	var res struct {
		Instances []ports.InstanciaListadaDTO `json:"instances"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("Error deserializando respuesta: %v", err)
	}

	if len(res.Instances) != 1 {
		t.Fatalf("OPERATOR debe ver exclusivamente su instancia asignada (1), pero recibió %d", len(res.Instances))
	}
	if res.Instances[0].ID != 101 {
		t.Errorf("Se esperaba la instancia 101, pero se recibió id=%d", res.Instances[0].ID)
	}
}

func TestListarInstancias_ProxmoxInaccesible(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockPx := &mockProxmoxPort{
		errListar: ports.ErrProxmoxNoDisponible,
	}
	mockRepo := &mockUserRepository{}

	handler := adaptersHttp.NewInstanceHandler(mockPx, mockRepo, seguimientoNulo{}, &mockTareaRepository{}, &mockAuditService{})

	router := gin.New()
	router.GET("/api/instances", func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyRol), "ADMIN")
		c.Set(string(middleware.ContextKeyUserID), uuid.New().String())
	}, handler.ListarInstancias)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/instances", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("Código esperado 502 Bad Gateway, obtenido %d", w.Code)
	}

	var errResp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("Error deserializando respuesta de error: %v", err)
	}

	if errResp["errorCode"] != "PROXMOX_UNAVAILABLE" {
		t.Errorf("errorCode esperado PROXMOX_UNAVAILABLE, obtenido: %v", errResp["errorCode"])
	}
}

func TestListarInstancias_ProxmoxTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockPx := &mockProxmoxPort{
		errListar: fmt.Errorf("%w: %w", ports.ErrProxmoxNoDisponible, ports.ErrProxmoxTimeout),
	}
	handler := adaptersHttp.NewInstanceHandler(mockPx, &mockUserRepository{}, seguimientoNulo{}, &mockTareaRepository{}, &mockAuditService{})

	router := gin.New()
	router.GET("/api/instances", func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyRol), "ADMIN")
		c.Set(string(middleware.ContextKeyUserID), uuid.New().String())
	}, handler.ListarInstancias)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/instances", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusGatewayTimeout {
		t.Fatalf("Código esperado 504 Gateway Timeout, obtenido %d", w.Code)
	}
	var errResp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &errResp)
	if errResp["errorCode"] != "PROXMOX_TIMEOUT" {
		t.Errorf("errorCode esperado PROXMOX_TIMEOUT, obtenido: %v", errResp["errorCode"])
	}
}

// DoD: un bloqueo de Proxmox responde 409 INSTANCE_BUSY, no 502 PROXMOX_UNAVAILABLE.
func TestAccionesDeEnergia_InstanciaOcupadaEs409(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mockPx := &mockProxmoxPort{errAccion: fmt.Errorf("%w: can't lock file '/var/lock/qemu-server/lock-110.conf' - got timeout", ports.ErrInstanciaOcupada)}
	handler := adaptersHttp.NewInstanceHandler(mockPx, &mockUserRepository{}, seguimientoNulo{}, &mockTareaRepository{}, &mockAuditService{})

	router := gin.New()
	router.POST("/api/instances/:vmid/start", handler.IniciarInstancia)
	router.POST("/api/instances/:vmid/stop", handler.DetenerInstancia)

	for _, ruta := range []string{"/api/instances/110/start", "/api/instances/110/stop"} {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, ruta, nil)
		router.ServeHTTP(w, req)

		var cuerpo map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &cuerpo)
		if w.Code != http.StatusConflict || cuerpo["errorCode"] != "INSTANCE_BUSY" ||
			cuerpo["message"] != "La instancia se encuentra ejecutando otra tarea. Aguarde a que finalice." {
			t.Errorf("%s: se esperaba 409 INSTANCE_BUSY, vino %d %v", ruta, w.Code, cuerpo)
		}
	}
}

// Cada tipo de error de Proxmox tiene su propio código HTTP y errorCode, igual en
// los cuatro endpoints que usan mapearErrorProxmox.
func TestMapeoDeErroresProxmox_EnTodosLosEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	timeout := fmt.Errorf("%w: %w: context deadline exceeded", ports.ErrProxmoxNoDisponible, ports.ErrProxmoxTimeout)
	caido := fmt.Errorf("%w: dial tcp 10.10.20.1:8006: connect: connection refused", ports.ErrProxmoxNoDisponible)
	token := fmt.Errorf("%w: %w: HTTP 401", ports.ErrProxmoxNoDisponible, ports.ErrProxmoxCredenciales)
	ocupada := fmt.Errorf("%w: can't lock file", ports.ErrInstanciaOcupada)

	casos := []struct {
		nombre string
		err    error
		status int
		codigo string
	}{
		{"timeout", timeout, http.StatusGatewayTimeout, "PROXMOX_TIMEOUT"},
		{"Proxmox caído", caido, http.StatusBadGateway, "PROXMOX_UNAVAILABLE"},
		{"token rechazado", token, http.StatusBadGateway, "PROXMOX_UNAVAILABLE"},
	}
	endpoints := []struct {
		metodo, ruta string
	}{
		{http.MethodGet, "/api/instances"},
		{http.MethodGet, "/api/instances/110"},
		{http.MethodPost, "/api/instances/110/start"},
		{http.MethodPost, "/api/instances/110/stop"},
	}

	probar := func(mock *mockProxmoxPort, metodo, ruta string) (int, map[string]string) {
		handler := adaptersHttp.NewInstanceHandler(mock, &mockUserRepository{}, seguimientoNulo{}, &mockTareaRepository{}, &mockAuditService{})
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyRol), "ADMIN")
			c.Set(string(middleware.ContextKeyUserID), uuid.New().String())
		})
		router.GET("/api/instances", handler.ListarInstancias)
		router.GET("/api/instances/:vmid", handler.ObtenerInstancia)
		router.POST("/api/instances/:vmid/start", handler.IniciarInstancia)
		router.POST("/api/instances/:vmid/stop", handler.DetenerInstancia)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(metodo, ruta, nil)
		router.ServeHTTP(w, req)
		var cuerpo map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &cuerpo)
		return w.Code, cuerpo
	}

	for _, caso := range casos {
		for _, ep := range endpoints {
			mock := &mockProxmoxPort{errListar: caso.err, errDetalle: caso.err, errAccion: caso.err}
			status, cuerpo := probar(mock, ep.metodo, ep.ruta)
			if status != caso.status || cuerpo["errorCode"] != caso.codigo {
				t.Errorf("%s en %s %s: se esperaba %d %s, vino %d %v", caso.nombre, ep.metodo, ep.ruta, caso.status, caso.codigo, status, cuerpo)
			}
			if caso.codigo == "PROXMOX_TIMEOUT" && cuerpo["message"] != "Proxmox no respondió a tiempo; la acción puede haberse aplicado" {
				t.Errorf("Mensaje del timeout: %q", cuerpo["message"])
			}
			for _, interno := range []string{"deadline", "dial tcp", "10.10.20.1", "HTTP 401"} {
				if strings.Contains(cuerpo["message"], interno) {
					t.Errorf("El detalle técnico no debe llegar al cliente: %q", cuerpo["message"])
				}
			}
		}
	}

	// El 409 INSTANCE_BUSY de las acciones de energía no cambia.
	for _, ruta := range []string{"/api/instances/110/start", "/api/instances/110/stop"} {
		if status, cuerpo := probar(&mockProxmoxPort{errAccion: ocupada}, http.MethodPost, ruta); status != http.StatusConflict || cuerpo["errorCode"] != "INSTANCE_BUSY" {
			t.Errorf("%s ocupada: se esperaba 409 INSTANCE_BUSY, vino %d %v", ruta, status, cuerpo)
		}
	}
}
