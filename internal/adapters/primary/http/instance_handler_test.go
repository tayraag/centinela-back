package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
}

func (m *mockProxmoxPort) ObtenerInstancia(ctx context.Context, vmid int) (*ports.InstanciaProxmoxDTO, error) {
	return nil, nil
}

func (m *mockProxmoxPort) ListarInstancias(ctx context.Context) ([]ports.InstanciaProxmoxDTO, error) {
	if m.errListar != nil {
		return nil, m.errListar
	}
	return m.instancias, nil
}

func (m *mockProxmoxPort) IniciarInstancia(ctx context.Context, vmid int) (string, error) {
	return "UPID:test:start", nil
}

func (m *mockProxmoxPort) DetenerInstancia(ctx context.Context, vmid int) (string, error) {
	return "UPID:test:stop", nil
}

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
	return nil, nil
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

	handler := adaptersHttp.NewInstanceHandler(mockPx, mockRepo)

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

	handler := adaptersHttp.NewInstanceHandler(mockPx, mockRepo)

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

	handler := adaptersHttp.NewInstanceHandler(mockPx, mockRepo)

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
