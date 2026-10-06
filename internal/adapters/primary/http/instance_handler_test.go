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

// Metadatos distintivos que informa ObtenerInstancia: si la auditoría graba un
// nombre vacío o un resource_type fijo, las aserciones de los tests fallan.
const (
	nombreInstanciaMock = "vm-auditoria-110"
	tipoInstanciaMock   = "lxc"
)

// mockProxmoxPort implementa ports.ProxmoxPort para pruebas
type mockProxmoxPort struct {
	instancias []ports.InstanciaProxmoxDTO
	errListar  error
	errAccion  error // error de IniciarInstancia / DetenerInstancia
	errDetalle error // error de ObtenerInstancia
	estado     string // estado que informa ObtenerInstancia
	escrituras int    // órdenes de escritura recibidas (start/stop/shutdown/reboot)
}

func (m *mockProxmoxPort) ObtenerInstancia(ctx context.Context, vmid int) (*ports.InstanciaProxmoxDTO, error) {
	if m.errDetalle != nil {
		return nil, m.errDetalle
	}
	return &ports.InstanciaProxmoxDTO{
		Vmid:   vmid,
		Nombre: nombreInstanciaMock,
		Tipo:   tipoInstanciaMock,
		Estado: m.estado,
	}, nil
}

func (m *mockProxmoxPort) ListarInstancias(ctx context.Context) ([]ports.InstanciaProxmoxDTO, error) {
	if m.errListar != nil {
		return nil, m.errListar
	}
	return m.instancias, nil
}

func (m *mockProxmoxPort) IniciarInstancia(ctx context.Context, vmid int) (string, error) {
	m.escrituras++
	if m.errAccion != nil {
		return "", m.errAccion
	}
	return "UPID:test:start", nil
}

func (m *mockProxmoxPort) DetenerInstancia(ctx context.Context, vmid int) (string, error) {
	m.escrituras++
	if m.errAccion != nil {
		return "", m.errAccion
	}
	return "UPID:test:stop", nil
}

func (m *mockProxmoxPort) ObtenerEstadoNodo(context.Context, string) (*ports.NodeStatusDTO, error) {
	return &ports.NodeStatusDTO{}, nil
}

func (m *mockProxmoxPort) ObtenerInterfaces(context.Context, string, string, int) ([]ports.InterfazRed, error) {
	return nil, nil
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

func (m *mockProxmoxPort) Shutdown(ctx context.Context, node string, vmid int, vmType string) (string, error) {
	m.escrituras++
	if m.errAccion != nil {
		return "", m.errAccion
	}
	return "UPID:test:shutdown", nil
}

func (m *mockProxmoxPort) Reboot(ctx context.Context, node string, vmid int, vmType string) (string, error) {
	m.escrituras++
	if m.errAccion != nil {
		return "", m.errAccion
	}
	return "UPID:test:reboot", nil
}

func (m *mockProxmoxPort) EliminarInstancia(ctx context.Context, vmid int) (string, error) {
	m.escrituras++
	if m.errAccion != nil {
		return "", m.errAccion
	}
	return "UPID:test:qmdestroy", nil
}

// inventarioNulo implementa ports.InventarioService sin resolver ninguna IP.
type inventarioNulo struct{}

func (inventarioNulo) Listar(context.Context) ([]ports.InstanciaInventario, error) { return nil, nil }
func (inventarioNulo) ResolverIPs(context.Context, []ports.InstanciaProxmoxDTO) map[int]*string {
	return map[int]*string{}
}

// seguimientoNulo implementa ports.SeguimientoTareas sin hacer nada.
type seguimientoNulo struct{}

func (seguimientoNulo) Seguir(context.Context, uuid.UUID, int, string, string) (uuid.UUID, error) {
	return uuid.New(), nil
}

type mockTareaRepository struct {
	activas map[int]ports.ActiveTaskDTO
}
func (m *mockTareaRepository) Crear(context.Context, *domain.TareaAsincrona) error { return nil }
func (m *mockTareaRepository) ListarEnCurso(context.Context) ([]domain.TareaAsincrona, error) {
	return nil, nil
}
func (m *mockTareaRepository) ActualizarEstado(context.Context, uuid.UUID, string, map[string]any) error {
	return nil
}
func (m *mockTareaRepository) BuscarTareasActivasPorVmids(context.Context, []int) (map[int]ports.ActiveTaskDTO, error) {
	if m.activas == nil {
		return map[int]ports.ActiveTaskDTO{}, nil
	}
	return m.activas, nil
}

type mockAuditService struct {
	registros []ports.RegistrarAuditoriaInput
}

func (m *mockAuditService) Registrar(_ context.Context, input ports.RegistrarAuditoriaInput) {
	m.registros = append(m.registros, input)
}

// ultimo devuelve el último registro de auditoría capturado.
func (m *mockAuditService) ultimo() (ports.RegistrarAuditoriaInput, bool) {
	if len(m.registros) == 0 {
		return ports.RegistrarAuditoriaInput{}, false
	}
	return m.registros[len(m.registros)-1], true
}

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

	handler := adaptersHttp.NewInstanceHandler(mockPx, mockRepo, seguimientoNulo{}, &mockTareaRepository{}, &mockAuditService{}, inventarioNulo{})

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

	var res []ports.InstanciaListadaDTO
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("Error deserializando respuesta: %v", err)
	}

	if len(res) != 3 {
		t.Fatalf("ADMIN debe ver las 3 instancias (100%%), pero recibió %d", len(res))
	}

	// Comprobar normalización: qemu -> vm
	if res[0].Type != "vm" || res[1].Type != "lxc" {
		t.Errorf("Normalización incorrecta: %+v", res)
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

	handler := adaptersHttp.NewInstanceHandler(mockPx, mockRepo, seguimientoNulo{}, &mockTareaRepository{}, &mockAuditService{}, inventarioNulo{})

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

	var res []ports.InstanciaListadaDTO
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("Error deserializando respuesta: %v", err)
	}

	if len(res) != 1 {
		t.Fatalf("OPERATOR debe ver exclusivamente su instancia asignada (1), pero recibió %d", len(res))
	}
	if res[0].ID != 101 {
		t.Errorf("Se esperaba la instancia 101, pero se recibió id=%d", res[0].ID)
	}
}

func TestListarInstancias_ProxmoxInaccesible(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockPx := &mockProxmoxPort{
		errListar: ports.ErrProxmoxNoDisponible,
	}
	mockRepo := &mockUserRepository{}

	handler := adaptersHttp.NewInstanceHandler(mockPx, mockRepo, seguimientoNulo{}, &mockTareaRepository{}, &mockAuditService{}, inventarioNulo{})

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
	handler := adaptersHttp.NewInstanceHandler(mockPx, &mockUserRepository{}, seguimientoNulo{}, &mockTareaRepository{}, &mockAuditService{}, inventarioNulo{})

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
	for _, ruta := range []string{"/api/instances/110/start", "/api/instances/110/stop"} {
		mockPx := &mockProxmoxPort{estado: estadoParaRuta(ruta), errAccion: fmt.Errorf("%w: can't lock file '/var/lock/qemu-server/lock-110.conf' - got timeout", ports.ErrInstanciaOcupada)}
		handler := adaptersHttp.NewInstanceHandler(mockPx, &mockUserRepository{}, seguimientoNulo{}, &mockTareaRepository{}, &mockAuditService{}, inventarioNulo{})
		router := gin.New()
		router.POST("/api/instances/:vmid/start", handler.IniciarInstancia)
		router.POST("/api/instances/:vmid/stop", handler.DetenerInstancia)
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
		handler := adaptersHttp.NewInstanceHandler(mock, &mockUserRepository{}, seguimientoNulo{}, &mockTareaRepository{}, &mockAuditService{}, inventarioNulo{})
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
			mock := &mockProxmoxPort{estado: estadoParaRuta(ep.ruta), errListar: caso.err, errDetalle: caso.err, errAccion: caso.err}
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
		if status, cuerpo := probar(&mockProxmoxPort{estado: estadoParaRuta(ruta), errAccion: ocupada}, http.MethodPost, ruta); status != http.StatusConflict || cuerpo["errorCode"] != "INSTANCE_BUSY" {
			t.Errorf("%s ocupada: se esperaba 409 INSTANCE_BUSY, vino %d %v", ruta, status, cuerpo)
		}
	}
}

// estadoParaRuta devuelve el estado que habilita la acción de la ruta.
func estadoParaRuta(ruta string) string {
	if strings.HasSuffix(ruta, "/start") {
		return "stopped"
	}
	return "running"
}

func ejecutarAccion(mock *mockProxmoxPort, ruta string) (int, map[string]string) {
	gin.SetMode(gin.TestMode)
	handler := adaptersHttp.NewInstanceHandler(mock, &mockUserRepository{}, seguimientoNulo{}, &mockTareaRepository{}, &mockAuditService{}, inventarioNulo{})
	router := gin.New()
	router.POST("/api/instances/:vmid/start", handler.IniciarInstancia)
	router.POST("/api/instances/:vmid/stop", handler.DetenerInstancia)
	router.POST("/api/instances/:vmid/status/:action", handler.CambiarEstado)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, ruta, nil)
	router.ServeHTTP(w, req)
	var cuerpo map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &cuerpo)
	return w.Code, cuerpo
}

// Matriz de estados: acción incompatible => 409 y NINGUNA orden de escritura a Proxmox.
func TestAccionesDeEnergia_EstadoIncompatibleEs409SinEscribir(t *testing.T) {
	casos := []struct{ ruta, estado string }{
		{"/api/instances/110/start", "running"},
		{"/api/instances/110/stop", "stopped"},
		{"/api/instances/110/status/start", "running"},
		{"/api/instances/110/status/stop", "stopped"},
		{"/api/instances/110/status/shutdown", "stopped"},
		{"/api/instances/110/status/reboot", "stopped"},
	}
	for _, caso := range casos {
		mock := &mockProxmoxPort{estado: caso.estado}
		status, cuerpo := ejecutarAccion(mock, caso.ruta)
		if status != http.StatusConflict || cuerpo["errorCode"] != "INSTANCE_INVALID_STATE" ||
			cuerpo["message"] != "La instancia se encuentra en un estado incompatible para la acción solicitada" {
			t.Errorf("%s (%s): se esperaba 409 INSTANCE_INVALID_STATE, vino %d %v", caso.ruta, caso.estado, status, cuerpo)
		}
		if mock.escrituras != 0 {
			t.Errorf("%s: no debía escribir en Proxmox y escribió %d veces", caso.ruta, mock.escrituras)
		}
	}
}

// Acción compatible => 202 con upid y tareaId.
func TestAccionesDeEnergia_EstadoValidEs202(t *testing.T) {
	casos := []struct{ ruta, estado string }{
		{"/api/instances/110/start", "stopped"},
		{"/api/instances/110/stop", "running"},
		{"/api/instances/110/status/start", "stopped"},
		{"/api/instances/110/status/stop", "running"},
		{"/api/instances/110/status/shutdown", "running"},
		{"/api/instances/110/status/reboot", "running"},
	}
	for _, caso := range casos {
		mock := &mockProxmoxPort{estado: caso.estado}
		status, cuerpo := ejecutarAccion(mock, caso.ruta)
		if status != http.StatusAccepted || cuerpo["upid"] == "" || cuerpo["tareaId"] == "" || mock.escrituras != 1 {
			t.Errorf("%s: se esperaba 202 con upid y tareaId, vino %d %v", caso.ruta, status, cuerpo)
		}
	}
}

// Verbo no contemplado en la ruta polimórfica => 400 INVALID_ACTION sin tocar Proxmox.
func TestCambiarEstado_AccionInvalidaEs400(t *testing.T) {
	mock := &mockProxmoxPort{estado: "running"}
	status, cuerpo := ejecutarAccion(mock, "/api/instances/110/status/pause")
	if status != http.StatusBadRequest || cuerpo["errorCode"] != "INVALID_ACTION" || mock.escrituras != 0 {
		t.Errorf("se esperaba 400 INVALID_ACTION, vino %d %v", status, cuerpo)
	}
}
// inventarioFalso resuelve IPs fijas y registra qué instancias le pidieron.
type inventarioFalso struct {
	inventarioNulo
	pedidas []int
}

func (f *inventarioFalso) ResolverIPs(_ context.Context, instancias []ports.InstanciaProxmoxDTO) map[int]*string {
	ips := map[int]*string{}
	for _, inst := range instancias {
		f.pedidas = append(f.pedidas, inst.Vmid)
		if inst.Estado == "running" {
			ip := fmt.Sprintf("192.168.1.%d", inst.Vmid)
			ips[inst.Vmid] = &ip
		}
	}
	return ips
}

// GET /api/instances devuelve la IP resuelta (o null) y solo resuelve las
// instancias que el usuario puede ver.
func TestListarInstancias_IncluyeIPsDeLasVisibles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mockPx := &mockProxmoxPort{instancias: []ports.InstanciaProxmoxDTO{
		{Vmid: 100, Nombre: "vm-1", Tipo: "qemu", Nodo: "pve1", Estado: "running"},
		{Vmid: 101, Nombre: "ct-1", Tipo: "lxc", Nodo: "pve1", Estado: "running"},
		{Vmid: 102, Nombre: "vm-2", Tipo: "qemu", Nodo: "pve1", Estado: "stopped"},
	}}
	inventario := &inventarioFalso{}
	mockRepo := &mockUserRepository{permisosVMIDs: []int{101, 102}}
	handler := adaptersHttp.NewInstanceHandler(mockPx, mockRepo, seguimientoNulo{}, &mockTareaRepository{}, &mockAuditService{}, inventario)

	router := gin.New()
	router.GET("/api/instances", func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyRol), "OPERATOR")
		c.Set(string(middleware.ContextKeyUserID), uuid.New().String())
	}, handler.ListarInstancias)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/instances", nil))

	var res []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != 200 {
		t.Fatalf("%d %v", w.Code, err)
	}
	ips := map[float64]any{}
	for _, inst := range res {
		ips[inst["id"].(float64)] = inst["ip"]
	}
	if ips[101] != "192.168.1.101" {
		t.Errorf("La 101 (encendida) debe traer su IP: %v", ips[101])
	}
	if v, presente := ips[102]; !presente || v != nil {
		t.Errorf("La 102 (apagada) debe traer ip: null: %v", v)
	}
	for _, vmid := range inventario.pedidas {
		if vmid == 100 {
			t.Error("No debe resolverse la IP de una instancia que el OPERATOR no puede ver")
		}
	}
}

// GET /api/instances: activeTask es el objeto { tareaId, action, status } de la
// tarea RUNNING de la instancia, o null si no tiene ninguna.
func TestListarInstancias_ActiveTaskEsObjetoONull(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mockPx := &mockProxmoxPort{instancias: []ports.InstanciaProxmoxDTO{
		{Vmid: 110, Nombre: "vm-1", Tipo: "qemu", Nodo: "pve1", Estado: "stopped"},
		{Vmid: 201, Nombre: "ct-1", Tipo: "lxc", Nodo: "pve1", Estado: "running"},
	}}
	tareas := &mockTareaRepository{activas: map[int]ports.ActiveTaskDTO{
		110: {TareaID: "3f2504e0-4f89-11d3-9a0c-0305e82c3301", Action: "START", Status: "RUNNING"},
	}}
	handler := adaptersHttp.NewInstanceHandler(mockPx, &mockUserRepository{}, seguimientoNulo{}, tareas, &mockAuditService{}, inventarioNulo{})
	router := gin.New()
	router.GET("/api/instances", func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyRol), "ADMIN")
		c.Set(string(middleware.ContextKeyUserID), uuid.New().String())
	}, handler.ListarInstancias)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/instances", nil))

	var res []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != 200 {
		t.Fatalf("%d %v", w.Code, err)
	}
	porID := map[float64]map[string]any{}
	for _, inst := range res {
		porID[inst["id"].(float64)] = inst
	}
	tarea, ok := porID[110]["activeTask"].(map[string]any)
	if !ok || len(tarea) != 3 || tarea["tareaId"] != "3f2504e0-4f89-11d3-9a0c-0305e82c3301" || tarea["action"] != "START" || tarea["status"] != "RUNNING" {
		t.Errorf("activeTask de la 110: %v", porID[110]["activeTask"])
	}
	if v, presente := porID[201]["activeTask"]; !presente || v != nil {
		t.Errorf("La 201 sin tarea debe traer activeTask: null: %v (presente=%v)", v, presente)
	}
}

func ejecutarBorrado(mock *mockProxmoxPort) (int, map[string]string) {
	gin.SetMode(gin.TestMode)
	handler := adaptersHttp.NewInstanceHandler(mock, &mockUserRepository{}, seguimientoNulo{}, &mockTareaRepository{}, &mockAuditService{}, inventarioNulo{})
	router := gin.New()
	router.DELETE("/api/instances/:vmid", func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUserID), uuid.New().String())
	}, handler.EliminarInstancia)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/instances/110", nil))
	var cuerpo map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &cuerpo)
	return w.Code, cuerpo
}

// DELETE /api/instances/:vmid es asíncrono: 202 { upid, tareaId }, como las
// acciones de energía.
func TestEliminarInstancia_Es202ConUpidYTareaId(t *testing.T) {
	mock := &mockProxmoxPort{estado: "stopped"}
	status, cuerpo := ejecutarBorrado(mock)
	if status != http.StatusAccepted || cuerpo["upid"] != "UPID:test:qmdestroy" || cuerpo["tareaId"] == "" || mock.escrituras != 1 {
		t.Errorf("se esperaba 202 con upid y tareaId, vino %d %v", status, cuerpo)
	}
}

func TestEliminarInstancia_EncendidaEs409SinEscribir(t *testing.T) {
	mock := &mockProxmoxPort{estado: "running"}
	status, cuerpo := ejecutarBorrado(mock)
	if status != http.StatusConflict || cuerpo["errorCode"] != "INSTANCE_NOT_STOPPED" || mock.escrituras != 0 {
		t.Errorf("se esperaba 409 INSTANCE_NOT_STOPPED sin escribir, vino %d %v (escrituras %d)", status, cuerpo, mock.escrituras)
	}
}

func TestEliminarInstancia_ErroresDeProxmox(t *testing.T) {
	casos := []struct {
		err    error
		status int
		codigo string
	}{
		{fmt.Errorf("%w: can't lock file", ports.ErrInstanciaOcupada), http.StatusConflict, "INSTANCE_BUSY"},
		{fmt.Errorf("%w: %w: context deadline exceeded", ports.ErrProxmoxNoDisponible, ports.ErrProxmoxTimeout), http.StatusGatewayTimeout, "PROXMOX_TIMEOUT"},
		{fmt.Errorf("%w: connection refused", ports.ErrProxmoxNoDisponible), http.StatusBadGateway, "PROXMOX_UNAVAILABLE"},
	}
	for _, caso := range casos {
		status, cuerpo := ejecutarBorrado(&mockProxmoxPort{estado: "stopped", errAccion: caso.err})
		if status != caso.status || cuerpo["errorCode"] != caso.codigo {
			t.Errorf("%v: se esperaba %d %s, vino %d %v", caso.err, caso.status, caso.codigo, status, cuerpo)
		}
	}
}

// ejecutarCicloDeVidaConAuditoria ejecuta una acción sobre el router de ciclo de
// vida y devuelve el código HTTP junto con el mock que captura la auditoría.
func ejecutarCicloDeVidaConAuditoria(mock *mockProxmoxPort, metodo, ruta string) (int, *mockAuditService) {
	gin.SetMode(gin.TestMode)
	audit := &mockAuditService{}
	handler := adaptersHttp.NewInstanceHandler(mock, &mockUserRepository{}, seguimientoNulo{}, &mockTareaRepository{}, audit, inventarioNulo{})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUserID), uuid.New().String())
	})
	router.POST("/api/instances/:vmid/start", handler.IniciarInstancia)
	router.POST("/api/instances/:vmid/stop", handler.DetenerInstancia)
	router.POST("/api/instances/:vmid/status/:action", handler.CambiarEstado)
	router.DELETE("/api/instances/:vmid", handler.EliminarInstancia)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(metodo, ruta, nil))
	return w.Code, audit
}

// La auditoría de cada endpoint de ciclo de vida debe grabar el vocabulario
// canónico (contrato Etapa 1) y conservar el nombre y el tipo reales de la
// instancia, sin importar si el endpoint es alias o genérico.
func TestAuditoriaDeCicloDeVida_UsaVocabularioCanonicoYMetadatos(t *testing.T) {
	casos := []struct {
		nombre    string
		metodo    string
		ruta      string
		estado    string
		accion    string
		resultado string
	}{
		{"alias start", http.MethodPost, "/api/instances/110/start", "stopped", "START", "PENDING"},
		{"alias stop", http.MethodPost, "/api/instances/110/stop", "running", "STOP", "PENDING"},
		{"genérico start", http.MethodPost, "/api/instances/110/status/start", "stopped", "START", "PENDING"},
		{"genérico stop", http.MethodPost, "/api/instances/110/status/stop", "running", "STOP", "PENDING"},
		{"genérico shutdown", http.MethodPost, "/api/instances/110/status/shutdown", "running", "SHUTDOWN", "PENDING"},
		{"genérico reboot", http.MethodPost, "/api/instances/110/status/reboot", "running", "REBOOT", "PENDING"},
		{"delete", http.MethodDelete, "/api/instances/110", "stopped", "DELETE", "PENDING"},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			status, audit := ejecutarCicloDeVidaConAuditoria(&mockProxmoxPort{estado: caso.estado}, caso.metodo, caso.ruta)
			if status != http.StatusAccepted {
				t.Fatalf("se esperaba 202, vino %d", status)
			}
			reg, ok := audit.ultimo()
			if !ok {
				t.Fatal("el endpoint no registró ninguna auditoría")
			}
			if reg.Accion != caso.accion {
				t.Errorf("Accion: se esperaba %q, vino %q", caso.accion, reg.Accion)
			}
			if reg.Resultado != caso.resultado {
				t.Errorf("Resultado: se esperaba %q, vino %q", caso.resultado, reg.Resultado)
			}
			if reg.InstanciaNombre != nombreInstanciaMock {
				t.Errorf("InstanciaNombre: se esperaba %q, vino %q", nombreInstanciaMock, reg.InstanciaNombre)
			}
			if reg.InstanciaID != "110" {
				t.Errorf("InstanciaID: se esperaba %q, vino %q", "110", reg.InstanciaID)
			}
			if reg.Detalles["resource_type"] != tipoInstanciaMock {
				t.Errorf("resource_type: se esperaba %q, vino %v", tipoInstanciaMock, reg.Detalles["resource_type"])
			}
		})
	}
}

// El fallo de los alias /start y /stop también audita el nombre real de la
// instancia y el código de acción canónico.
func TestAuditoriaDeAlias_FalloUsaAccionCanonicaYNombreReal(t *testing.T) {
	casos := []struct {
		nombre string
		ruta   string
		estado string
		accion string
	}{
		{"alias start", "/api/instances/110/start", "stopped", "START"},
		{"alias stop", "/api/instances/110/stop", "running", "STOP"},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			mock := &mockProxmoxPort{estado: caso.estado, errAccion: fmt.Errorf("%w: fallo simulado", ports.ErrProxmoxNoDisponible)}
			status, audit := ejecutarCicloDeVidaConAuditoria(mock, http.MethodPost, caso.ruta)
			if status != http.StatusBadGateway {
				t.Fatalf("se esperaba 502, vino %d", status)
			}
			reg, ok := audit.ultimo()
			if !ok {
				t.Fatal("el endpoint no registró ninguna auditoría de fallo")
			}
			if reg.Accion != caso.accion {
				t.Errorf("Accion: se esperaba %q, vino %q", caso.accion, reg.Accion)
			}
			if reg.Resultado != ports.ResultadoFalla {
				t.Errorf("Resultado: se esperaba %q, vino %q", ports.ResultadoFalla, reg.Resultado)
			}
			if reg.InstanciaNombre != nombreInstanciaMock {
				t.Errorf("InstanciaNombre: se esperaba %q, vino %q", nombreInstanciaMock, reg.InstanciaNombre)
			}
		})
	}
}
