package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

// proxmoxWSConfigurable: la consulta de métricas puede fallar o demorarse.
// Los campos se cambian desde el test mientras el handler los lee: son atómicos.
type proxmoxWSConfigurable struct {
	mockProxmoxForWS
	falla  atomic.Pointer[error]
	demora atomic.Int64 // nanosegundos
}

func (p *proxmoxWSConfigurable) fallar(err error) { p.falla.Store(&err) }

func (p *proxmoxWSConfigurable) ObtenerEstadoActual(ctx context.Context, nodo, tipo string, vmid int) (*domain.MetricasEnVivoDTO, error) {
	if d := time.Duration(p.demora.Load()); d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := p.falla.Load(); err != nil && *err != nil {
		return nil, *err
	}
	return p.mockProxmoxForWS.ObtenerEstadoActual(ctx, nodo, tipo, vmid)
}

type entornoLimitesWS struct {
	kv      *memoria.KVStore
	admin   uuid.UUID
	url     string
	proxmox *proxmoxWSConfigurable
}

func nuevoEntornoLimitesWS(t *testing.T, limites adaptersHttp.LimitesWS) *entornoLimitesWS {
	t.Helper()
	gin.SetMode(gin.TestMode)
	kv := memoria.Nuevo()
	admin := uuid.New()
	authRepo := &mockAuthRepoForWS{usuarios: map[uuid.UUID]*domain.Usuario{admin: {ID: admin, Rol: "ADMIN", Activo: true}}}
	px := &proxmoxWSConfigurable{mockProxmoxForWS: mockProxmoxForWS{instancias: map[int]*ports.InstanciaProxmoxDTO{
		100: {Vmid: 100, Tipo: "qemu", Nodo: "proxmox", Estado: "running"},
	}}}
	h := adaptersHttp.NewMetricsWSHandler(px, services.NewMetricasVivoService(kv, authRepo, &mockInstanceRepoForWS{}, 0), adaptersHttp.RefsPrueba, "proxmox",
		map[string]struct{}{"https://centinela.example": {}}, limites)
	r := gin.New()
	r.GET("/api/ws/instances/:vmid/metrics", adaptersHttp.ResolverPrueba, h.StreamMetrics)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &entornoLimitesWS{kv: kv, admin: admin, proxmox: px, url: "ws" + strings.TrimPrefix(srv.URL, "http") + adaptersHttp.ConRef("/api/ws/instances/100/metrics")}
}

func (e *entornoLimitesWS) ticket(t *testing.T) string {
	t.Helper()
	ticket := uuid.NewString()
	if err := e.kv.Set(context.Background(), "ws:ticket:"+ticket, ports.SesionWebSocket{UsuarioID: e.admin, SesionID: uuid.New()}, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	return ticket
}

func (e *entornoLimitesWS) conectar(t *testing.T, origen string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	cabeceras := http.Header{}
	if origen != "" {
		cabeceras.Set("Origin", origen)
	}
	return websocket.DefaultDialer.Dial(e.url+"?ticket="+e.ticket(t), cabeceras)
}

func codigoDeError(t *testing.T, resp *http.Response) string {
	t.Helper()
	if resp == nil {
		return "<sin respuesta>"
	}
	var cuerpo map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&cuerpo)
	return fmt.Sprintf("%d %s", resp.StatusCode, cuerpo["errorCode"])
}

var limitesDePrueba = adaptersHttp.LimitesWS{MaxConexionesPorUsuario: 2, Ping: 100 * time.Millisecond, EsperaPong: 300 * time.Millisecond, TimeoutConsulta: 200 * time.Millisecond}

// Punto 5: solo el Origin del front. Uno ajeno se rechaza sin gastar el ticket.
func TestMetricsWS_Origen(t *testing.T) {
	e := nuevoEntornoLimitesWS(t, limitesDePrueba)

	ticket := e.ticket(t)
	cabeceras := http.Header{"Origin": {"https://sitio-malicioso.example"}}
	_, resp, err := websocket.DefaultDialer.Dial(e.url+"?ticket="+ticket, cabeceras)
	if err == nil || codigoDeError(t, resp) != "403 ORIGIN_NOT_ALLOWED" {
		t.Errorf("Origin ajeno: se esperaba 403 ORIGIN_NOT_ALLOWED, vino %v", codigoDeError(t, resp))
	}
	if _, err := e.kv.Get(context.Background(), "ws:ticket:"+ticket); err != nil {
		t.Error("Un intento desde otro origen no debe consumir el ticket del usuario")
	}

	for _, origen := range []string{"https://centinela.example", ""} {
		conn, resp, err := e.conectar(t, origen)
		if err != nil {
			t.Errorf("Origin %q debe aceptarse: %v %s", origen, err, codigoDeError(t, resp))
			continue
		}
		conn.Close()
	}
}

// Punto 4: tope de conexiones por usuario; al cerrar una se libera el lugar.
func TestMetricsWS_TopeDeConexiones(t *testing.T) {
	e := nuevoEntornoLimitesWS(t, limitesDePrueba)
	c1, _, err := e.conectar(t, "")
	if err != nil {
		t.Fatal(err)
	}
	c2, _, err := e.conectar(t, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if _, resp, err := e.conectar(t, ""); err == nil || codigoDeError(t, resp) != "429 TOO_MANY_CONNECTIONS" {
		t.Fatalf("La tercera conexión debe rechazarse con 429 TOO_MANY_CONNECTIONS: %s", codigoDeError(t, resp))
	}
	c1.Close()
	var c3 *websocket.Conn
	for limite := time.Now().Add(2 * time.Second); time.Now().Before(limite); time.Sleep(50 * time.Millisecond) {
		if c3, _, err = e.conectar(t, ""); err == nil {
			break
		}
	}
	if c3 == nil {
		t.Fatal("Al cerrar una conexión debe liberarse su lugar")
	}
	c3.Close()
}

// Punto 4: un mensaje grande del cliente cierra la conexión (1009).
func TestMetricsWS_MensajeDelClienteDemasiadoGrande(t *testing.T) {
	e := nuevoEntornoLimitesWS(t, limitesDePrueba)
	conn, _, err := e.conectar(t, "")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", 4096))); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			if !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
				t.Errorf("Se esperaba cierre 1009 (mensaje demasiado grande), vino %v", err)
			}
			return
		}
	}
}

// Punto 4: un cliente que no responde los pings (red cortada sin cierre) se
// libera a los EsperaPong, y su lugar queda disponible.
func TestMetricsWS_ConexionMuertaSeLibera(t *testing.T) {
	limites := limitesDePrueba
	limites.MaxConexionesPorUsuario = 1
	e := nuevoEntornoLimitesWS(t, limites)
	muerta, _, err := e.conectar(t, "")
	if err != nil {
		t.Fatal(err)
	}
	defer muerta.Close() // nunca lee: no responde los pings
	time.Sleep(600 * time.Millisecond)
	conn, resp, err := e.conectar(t, "")
	if err != nil {
		t.Fatalf("La conexión muerta debía liberarse: %v %s", err, codigoDeError(t, resp))
	}
	conn.Close()
}

// Un cliente que sí responde los pings sigue conectado aunque pase EsperaPong.
func TestMetricsWS_ClienteVivoSigueConectado(t *testing.T) {
	e := nuevoEntornoLimitesWS(t, limitesDePrueba)
	conn, _, err := e.conectar(t, "")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for fin := time.Now().Add(time.Second); time.Now().Before(fin); {
		if _, _, err := conn.ReadMessage(); err != nil { // leer responde los pings
			t.Fatalf("Un cliente vivo no debe ser desconectado: %v", err)
		}
	}
}

func leerJSON(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("leer mensaje: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	return m
}

// Punto 3: si Proxmox falla, el mensaje dice el error y no trae métricas en 0.
func TestMetricsWS_ErroresDeProxmoxExplicitos(t *testing.T) {
	casos := []struct {
		nombre string
		falla  error
		demora time.Duration
		codigo string
	}{
		{"caido", fmt.Errorf("%w: connection refused", ports.ErrProxmoxNoDisponible), 0, "PROXMOX_UNAVAILABLE"},
		{"timeout", nil, time.Second, "PROXMOX_TIMEOUT"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			e := nuevoEntornoLimitesWS(t, limitesDePrueba)
			e.proxmox.demora.Store(int64(c.demora))
			e.proxmox.fallar(c.falla)
			inicio := time.Now()
			conn, _, err := e.conectar(t, "")
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			m := leerJSON(t, conn)
			detalle, _ := m["error"].(map[string]any)
			if m["status"] != "unavailable" || detalle["errorCode"] != c.codigo || m["vmid"] != adaptersHttp.RefInst(100) {
				t.Errorf("Mensaje de error inesperado: %v", m)
			}
			if _, tieneCPU := m["cpu"]; tieneCPU {
				t.Errorf("El mensaje de error no debe traer métricas: %v", m)
			}
			if c.demora > 0 && time.Since(inicio) > 800*time.Millisecond {
				t.Errorf("La consulta colgada debe cortarse a los %s, tardó %s", limitesDePrueba.TimeoutConsulta, time.Since(inicio))
			}

			// Cuando Proxmox vuelve, llegan métricas normales por la misma conexión.
			e.proxmox.fallar(nil)
			e.proxmox.demora.Store(0)
			if m := leerJSON(t, conn); m["error"] != nil || m["cpu"] == nil {
				t.Errorf("Al volver Proxmox deben llegar métricas: %v", m)
			}
		})
	}
}

// Si la instancia deja de existir, se cierra con 1000 INSTANCE_NOT_FOUND.
func TestMetricsWS_InstanciaEliminadaCierra(t *testing.T) {
	e := nuevoEntornoLimitesWS(t, limitesDePrueba)
	conn, _, err := e.conectar(t, "")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	leerJSON(t, conn)
	e.proxmox.fallar(fmt.Errorf("%w: vmid 100", ports.ErrInstanciaNoEncontrada))
	_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))
	for {
		_, _, err := conn.ReadMessage()
		if err == nil {
			continue
		}
		var cierre *websocket.CloseError
		if !errors.As(err, &cierre) || cierre.Code != websocket.CloseNormalClosure || cierre.Text != "INSTANCE_NOT_FOUND" {
			t.Errorf("Se esperaba cierre 1000 INSTANCE_NOT_FOUND, vino %v", err)
		}
		return
	}
}
