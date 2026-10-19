package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	adaptersHttp "el-centinela/internal/adapters/primary/http"
	"el-centinela/internal/adapters/secondary/memoria"
	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type mockAuthRepoForWS struct {
	ports.AuthRepository
	usuarios map[uuid.UUID]*domain.Usuario
}

func (m *mockAuthRepoForWS) BuscarUsuarioPorID(_ context.Context, id uuid.UUID) (*domain.Usuario, error) {
	u, ok := m.usuarios[id]
	if !ok {
		return nil, ports.ErrUsuarioNoEncontrado
	}
	return u, nil
}

type mockInstanceRepoForWS struct {
	permisos map[string]bool // "userID:vmid" -> true
}

func (m *mockInstanceRepoForWS) VerificarAcceso(_ context.Context, userID uuid.UUID, vmid int, _ string) (bool, error) {
	key := userID.String() + ":" + string(rune(vmid))
	return m.permisos[key], nil
}

func (m *mockInstanceRepoForWS) PurgarPermisosPorVmid(_ context.Context, _ int, _ string, _ *uuid.UUID) ([]string, error) {
	return []string{}, nil
}

type mockProxmoxForWS struct {
	ports.ProxmoxPort
	instancias    map[int]*ports.InstanciaProxmoxDTO
	metricasEnVivo *domain.MetricasEnVivoDTO
}

func (m *mockProxmoxForWS) ObtenerInstancia(_ context.Context, vmid int) (*ports.InstanciaProxmoxDTO, error) {
	inst, ok := m.instancias[vmid]
	if !ok {
		return nil, ports.ErrInstanciaNoEncontrada
	}
	return inst, nil
}

func (m *mockProxmoxForWS) ObtenerEstadoActual(_ context.Context, _, _ string, vmid int) (*domain.MetricasEnVivoDTO, error) {
	if m.metricasEnVivo != nil {
		m.metricasEnVivo.VMID = vmid
		return m.metricasEnVivo, nil
	}
	cpu, mem := 15.0, int64(1024*1024*1024)
	return &domain.MetricasEnVivoDTO{
		Timestamp: time.Now().Unix(),
		VMID:      vmid,
		Status:    "running",
		CPU:       &cpu,
		CPUs:      2,
		Mem:       &mem,
		MaxMem:    2 * 1024 * 1024 * 1024,
	}, nil
}

func TestAuth_EmitirWsTicket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	kv := memoria.Nuevo()
	authHandler := adaptersHttp.NewAuthHandler(nil, kv)

	r := gin.New()
	adminID := uuid.New()

	r.POST("/api/auth/ws-ticket", func(c *gin.Context) {
		// Simular middleware RequireAuth inyectando userID
		c.Set("userID", adminID.String())
		c.Next()
	}, authHandler.EmitirWsTicket)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/ws-ticket", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Código inesperado: %d, body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Ticket    string `json:"ticket"`
		ExpiresIn int    `json:"expiresIn"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Error deserializando respuesta: %v", err)
	}

	if resp.Ticket == "" || resp.ExpiresIn != 30 {
		t.Errorf("Ticket o expiración inválidos: %+v", resp)
	}

	// Verificar persistencia en KV
	val, err := kv.Get(context.Background(), "ws:ticket:"+resp.Ticket)
	if err != nil || val != adminID.String() {
		t.Errorf("El ticket en KV no coincide con el usuario: val=%s err=%v", val, err)
	}
}

func TestMetricsWS_HandshakeYStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	kv := memoria.Nuevo()

	adminID := uuid.New()
	operatorID := uuid.New()

	authRepo := &mockAuthRepoForWS{
		usuarios: map[uuid.UUID]*domain.Usuario{
			adminID: {
				ID:     adminID,
				Rol:    "ADMIN",
				Activo: true,
			},
			operatorID: {
				ID:     operatorID,
				Rol:    "OPERATOR",
				Activo: true,
			},
		},
	}

	instanceRepo := &mockInstanceRepoForWS{
		permisos: map[string]bool{
			operatorID.String() + ":" + string(rune(100)): true,
		},
	}

	proxmoxMock := &mockProxmoxForWS{
		instancias: map[int]*ports.InstanciaProxmoxDTO{
			100: {Vmid: 100, Tipo: "qemu", Nodo: "proxmox", Estado: "running"},
			200: {Vmid: 200, Tipo: "lxc", Nodo: "proxmox", Estado: "running"},
		},
	}

	wsHandler := adaptersHttp.NewMetricsWSHandler(proxmoxMock, services.NewMetricasVivoService(kv, authRepo, instanceRepo, 0), "proxmox",
		map[string]struct{}{}, adaptersHttp.LimitesWSPorDefecto)

	r := gin.New()
	r.GET("/api/ws/instances/:vmid/metrics", wsHandler.StreamMetrics)

	server := httptest.NewServer(r)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/ws/instances/100/metrics"

	t.Run("falla 401 sin ticket", func(t *testing.T) {
		_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err == nil {
			t.Fatal("Se esperaba error de conexión")
		}
		if resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("Se esperaba 401, vino %v", resp)
		}
	})

	t.Run("falla 401 con ticket inexistente o expirado", func(t *testing.T) {
		_, resp, err := websocket.DefaultDialer.Dial(wsURL+"?ticket=ticket-falso", nil)
		if err == nil {
			t.Fatal("Se esperaba error de conexión")
		}
		if resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("Se esperaba 401, vino %v", resp)
		}
	})

	t.Run("falla 403 para OPERATOR sin permiso sobre la instancia", func(t *testing.T) {
		ticket := uuid.New().String()
		_ = kv.Set(context.Background(), "ws:ticket:"+ticket, operatorID.String(), 30*time.Second)

		// Operador no tiene permiso sobre vmid 200
		urlSinPermiso := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/ws/instances/200/metrics?ticket=" + ticket
		_, resp, err := websocket.DefaultDialer.Dial(urlSinPermiso, nil)
		if err == nil {
			t.Fatal("Se esperaba error de conexión por 403")
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("Se esperaba 403 Forbidden, vino %v", resp)
		}
	})

	t.Run("conexión exitosa con ticket válido y recepción de métricas cada 2s", func(t *testing.T) {
		ticket := uuid.New().String()
		_ = kv.Set(context.Background(), "ws:ticket:"+ticket, adminID.String(), 30*time.Second)

		conn, resp, err := websocket.DefaultDialer.Dial(wsURL+"?ticket="+ticket, nil)
		if err != nil {
			t.Fatalf("Handshake falló: %v, resp: %+v", err, resp)
		}
		defer conn.Close()

		if resp.StatusCode != http.StatusSwitchingProtocols {
			t.Fatalf("Status esperado 101, vino %d", resp.StatusCode)
		}

		// Leer primer paquete inmediato
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, msg1, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("Error leyendo primer mensaje de métricas: %v", err)
		}

		var metrica1 domain.MetricasEnVivoDTO
		if err := json.Unmarshal(msg1, &metrica1); err != nil {
			t.Fatalf("Error parseando métricas JSON: %v", err)
		}
		if metrica1.VMID != 100 || metrica1.Status != "running" {
			t.Errorf("Métrica 1 inesperada: %+v", metrica1)
		}

		// Leer segundo paquete emitido a los 2 segundos
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, msg2, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("Error leyendo segundo intervalo de métricas: %v", err)
		}

		var metrica2 domain.MetricasEnVivoDTO
		if err := json.Unmarshal(msg2, &metrica2); err != nil {
			t.Fatalf("Error parseando métricas 2 JSON: %v", err)
		}
		if metrica2.VMID != 100 {
			t.Errorf("Métrica 2 inesperada: %+v", metrica2)
		}

		// Validar que el ticket fue consumido atómicamente de Redis (un solo uso)
		_, errKV := kv.Get(context.Background(), "ws:ticket:"+ticket)
		if errKV == nil {
			t.Error("El ticket debería haberse eliminado de Redis tras el primer uso")
		}
	})

	t.Run("intento de reutilizar el mismo ticket es rechazado con 401", func(t *testing.T) {
		ticketReutilizable := uuid.New().String()
		_ = kv.Set(context.Background(), "ws:ticket:"+ticketReutilizable, adminID.String(), 30*time.Second)

		// 1er uso: OK
		conn, _, err := websocket.DefaultDialer.Dial(wsURL+"?ticket="+ticketReutilizable, nil)
		if err != nil {
			t.Fatalf("Primer uso falló: %v", err)
		}
		conn.Close()

		// 2do uso con el mismo ticket: debe fallar 401
		_, resp, err := websocket.DefaultDialer.Dial(wsURL+"?ticket="+ticketReutilizable, nil)
		if err == nil {
			t.Fatal("El segundo uso del ticket debía fallar")
		}
		if resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("Se esperaba 401, vino %v", resp)
		}
	})

	t.Run("un logout del usuario cierra el WebSocket con 1008 y motivo LOGOUT", func(t *testing.T) {
		ticket := uuid.New().String()
		_ = kv.Set(context.Background(), "ws:ticket:"+ticket, operatorID.String(), 30*time.Second)
		conn, _, err := websocket.DefaultDialer.Dial(wsURL+"?ticket="+ticket, nil)
		if err != nil {
			t.Fatalf("Handshake falló: %v", err)
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Fatalf("Primer mensaje: %v", err)
		}

		sesion := uuid.New()
		_ = kv.Publish(context.Background(), ports.CanalEventos, ports.MensajeBus{Tipo: ports.MensajeLogout, UsuarioID: &operatorID, SesionID: &sesion})

		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			_, _, err := conn.ReadMessage()
			if err == nil {
				continue // métricas que ya estaban en vuelo
			}
			var cierre *websocket.CloseError
			if !errors.As(err, &cierre) || cierre.Code != websocket.ClosePolicyViolation || cierre.Text != ports.MotivoWSLogout {
				t.Fatalf("Se esperaba cierre 1008 LOGOUT, vino %v", err)
			}
			return
		}
	})
}
