package http

import (
	"bufio"
	"context"
	"encoding/json"
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

// eventosFalso implementa ports.EventosService: acepta solo el ticket "valido"
// y expone los canales para que el test empuje eventos y cortes.
type eventosFalso struct {
	eventos chan ports.RealtimeEvent
	cierre  chan string
	cerrado chan struct{}
	ticket  struct{ usuario, sesion uuid.UUID }
}

func nuevoEventosFalso() *eventosFalso {
	return &eventosFalso{eventos: make(chan ports.RealtimeEvent, 4), cierre: make(chan string, 1), cerrado: make(chan struct{})}
}

func (f *eventosFalso) EmitirTicket(_ context.Context, usuarioID, sesionID uuid.UUID) (string, error) {
	f.ticket.usuario, f.ticket.sesion = usuarioID, sesionID
	return "ticket-emitido", nil
}
func (f *eventosFalso) Conectar(_ context.Context, ticket string) (*ports.ConexionEventos, error) {
	if ticket != "valido" {
		return nil, ports.ErrTicketInvalido
	}
	return &ports.ConexionEventos{Eventos: f.eventos, Cierre: f.cierre, Cerrar: func() { close(f.cerrado) }}, nil
}
func (f *eventosFalso) Publicar(context.Context, ports.RealtimeEvent) error { return nil }
func (f *eventosFalso) PublicarParaUsuarios(context.Context, ports.RealtimeEvent, []uuid.UUID) error {
	return nil
}

func routerEventos(f *eventosFalso) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewEventsHandler(f, RefsPrueba)
	r := gin.New()
	r.GET("/api/events", h.Stream)
	r.POST("/api/events/ticket", func(c *gin.Context) {
		c.Set(middleware.ContextKeyUserID, uuid.MustParse("11111111-1111-4111-8111-111111111111").String())
		c.Set(middleware.ContextKeySesionID, uuid.MustParse("22222222-2222-4222-8222-222222222222"))
	}, h.EmitirTicket)
	return r
}

func servidorEventos(t *testing.T, f *eventosFalso) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(routerEventos(f))
	t.Cleanup(srv.Close)
	return srv
}

func servidorEventosConTimeout(t *testing.T, f *eventosFalso, timeout time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(routerEventos(f))
	srv.Config.WriteTimeout = timeout
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func TestStream_RechazaSinTicketOConTicketInvalido(t *testing.T) {
	srv := servidorEventos(t, nuevoEventosFalso())
	casos := map[string]string{
		"/api/events":               "EVENTS_TICKET_MISSING",
		"/api/events?ticket=":       "EVENTS_TICKET_MISSING",
		"/api/events?ticket=usado":  "EVENTS_TICKET_INVALID",
		"/api/events?ticket=abc123": "EVENTS_TICKET_INVALID",
	}
	for ruta, codigo := range casos {
		resp, err := http.Get(srv.URL + ruta)
		if err != nil {
			t.Fatal(err)
		}
		var cuerpo map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&cuerpo)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized || cuerpo["errorCode"] != codigo {
			t.Errorf("%s: se esperaba 401 %s, vino %d %v", ruta, codigo, resp.StatusCode, cuerpo)
		}
	}
}

func TestEmitirTicket_UsaUsuarioYSesionDelToken(t *testing.T) {
	f := nuevoEventosFalso()
	srv := servidorEventos(t, f)
	resp, err := http.Post(srv.URL+"/api/events/ticket", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var cuerpo map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&cuerpo)
	if resp.StatusCode != 200 || cuerpo["ticket"] != "ticket-emitido" {
		t.Fatalf("Respuesta: %d %v", resp.StatusCode, cuerpo)
	}
	if f.ticket.sesion.String() != "22222222-2222-4222-8222-222222222222" {
		t.Error("El ticket debe emitirse con la sesión del access token")
	}
}

func TestStream_EnviaEventosLatidosYCorte(t *testing.T) {
	anterior := latidoSSE
	latidoSSE = 50 * time.Millisecond
	defer func() { latidoSSE = anterior }()

	f := nuevoEventosFalso()
	srv := servidorEventos(t, f)
	resp, err := http.Get(srv.URL + "/api/events?ticket=valido")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); resp.StatusCode != 200 || !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Se esperaba 200 text/event-stream, vino %d %q", resp.StatusCode, ct)
	}

	// Un único lector del stream, que pasa las líneas por un canal.
	lineas := make(chan string, 64)
	go func() {
		lector := bufio.NewReader(resp.Body)
		for {
			l, err := lector.ReadString('\n')
			if err != nil {
				close(lineas)
				return
			}
			lineas <- l
		}
	}()
	leerHasta := func(buscado string) string {
		t.Helper()
		var visto strings.Builder
		fin := time.After(2 * time.Second)
		for {
			select {
			case l, ok := <-lineas:
				if !ok {
					t.Fatalf("El stream terminó antes de ver %q. Visto:\n%s", buscado, visto.String())
				}
				visto.WriteString(l)
				if strings.Contains(l, buscado) {
					return visto.String()
				}
			case <-fin:
				t.Fatalf("No llegó %q. Visto:\n%s", buscado, visto.String())
			}
		}
	}

	leerHasta(": conectado")
	leerHasta(": ping") // latido

	ev, _ := ports.NewRealtimeEvent(ports.EventoTareaFinalizada, ports.SeveridadInfo, "La tarea de encendido finalizó correctamente")
	ev = ev.ConRecurso(ports.RecursoVM, "110")
	f.eventos <- ev
	bloque := leerHasta(`"tipo":"TASK_FINISHED"`)
	if !strings.Contains(bloque, "id: "+ev.ID.String()) || !strings.Contains(bloque, `data: {"id":"`+ev.ID.String()) {
		t.Errorf("El evento debe llegar con id y data en JSON:\n%s", bloque)
	}
	if !strings.Contains(bloque, `"recursoId":"`+RefInst(110)+`"`) || strings.Contains(bloque, `"recursoId":"110"`) {
		t.Errorf("recursoId debe llegar como referencia pública, nunca el VMID:\n%s", bloque)
	}

	f.cierre <- "LOGOUT"
	bloque = leerHasta(`{"motivo":"LOGOUT"}`)
	if !strings.Contains(bloque, "event: cierre") {
		t.Errorf("El corte debe llegar como event: cierre:\n%s", bloque)
	}
	select {
	case <-f.cerrado:
	case <-time.After(2 * time.Second):
		t.Fatal("Al cortar, el handler debe liberar la conexión (Cerrar)")
	}
}

func TestStream_SobreviveAlWriteTimeoutDelServidor(t *testing.T) {
	anterior := latidoSSE
	latidoSSE = 40 * time.Millisecond
	defer func() { latidoSSE = anterior }()

	f := nuevoEventosFalso()
	// El deadline global de 15 s cortaría un SSE de larga vida antes del primer
	// latido. El handler debe anularlo solo para esta conexión.
	srv := servidorEventosConTimeout(t, f, 100*time.Millisecond)
	resp, err := http.Get(srv.URL + "/api/events?ticket=valido")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	lineas := make(chan string, 64)
	go func() {
		lector := bufio.NewReader(resp.Body)
		for {
			l, err := lector.ReadString('\n')
			if err != nil {
				close(lineas)
				return
			}
			lineas <- l
		}
	}()

	leerHasta := func(buscado string) {
		t.Helper()
		fin := time.After(2 * time.Second)
		for {
			select {
			case l, ok := <-lineas:
				if !ok {
					t.Fatalf("El stream terminó antes de ver %q", buscado)
				}
				if strings.Contains(l, buscado) {
					return
				}
			case <-fin:
				t.Fatalf("No llegó %q dentro del plazo", buscado)
			}
		}
	}

	leerHasta(": conectado")
	for i := 0; i < 3; i++ {
		leerHasta(": ping")
	}
}

// El evento que viaja por el bus lleva el VMID; el que llega al front, la
// referencia pública, también dentro de los textos de Proxmox. El original no
// se toca: lo comparten todos los streams.
func TestEventoPublico_OcultaElVmid(t *testing.T) {
	ev, _ := ports.NewRealtimeEvent(ports.EventoTareaFinalizada, ports.SeveridadWarning, "La tarea de apagado falló")
	ev = ev.ConRecurso(ports.RecursoLXC, "201").ConDetalles(map[string]any{
		"tareaId": "3f2504e0-4f89-11d3-9a0c-0305e82c3302", "accion": "STOP", "estado": ports.TareaFailed,
		"exitstatus": "CT 201 not running", "motivo": ports.MotivoProxmoxError, "error": "CT 201 not running (lock-201.conf, vm 2010)",
	})

	publico := eventoPublico(RefsPrueba, ev)
	ref := RefInst(201)
	if publico.RecursoID != ref {
		t.Errorf("recursoId: se esperaba %q, vino %q", ref, publico.RecursoID)
	}
	if publico.Detalles["exitstatus"] != "CT "+ref+" not running" {
		t.Errorf("exitstatus debe nombrar la referencia: %v", publico.Detalles["exitstatus"])
	}
	if publico.Detalles["error"] != "CT "+ref+" not running (lock-"+ref+".conf, vm 2010)" {
		t.Errorf("error debe reemplazar solo el VMID suelto (2010 es otro número): %v", publico.Detalles["error"])
	}
	if publico.Detalles["tareaId"] != "3f2504e0-4f89-11d3-9a0c-0305e82c3302" || publico.Detalles["motivo"] != ports.MotivoProxmoxError {
		t.Errorf("El resto de detalles se conserva: %v", publico.Detalles)
	}
	if ev.RecursoID != "201" || ev.Detalles["error"] != "CT 201 not running (lock-201.conf, vm 2010)" {
		t.Errorf("No debe modificarse el evento original: %+v", ev)
	}

	host, _ := ports.NewRealtimeEvent(ports.EventoAlerta, ports.SeveridadCritical, "El uso de CPU del host superó el 90%")
	host = host.ConRecurso(ports.RecursoHost, "")
	if got := eventoPublico(RefsPrueba, host); got.RecursoID != "" || got.Mensaje != host.Mensaje {
		t.Errorf("Un evento del HOST pasa igual: %+v", got)
	}
}
