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

func servidorEventos(t *testing.T, f *eventosFalso) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewEventsHandler(f)
	r := gin.New()
	r.GET("/api/events", h.Stream)
	r.POST("/api/events/ticket", func(c *gin.Context) {
		c.Set(middleware.ContextKeyUserID, uuid.MustParse("11111111-1111-4111-8111-111111111111").String())
		c.Set(middleware.ContextKeySesionID, uuid.MustParse("22222222-2222-4222-8222-222222222222"))
	}, h.EmitirTicket)
	srv := httptest.NewServer(r)
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
