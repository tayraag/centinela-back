package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// latidoSSE: cada cuánto se manda un comentario vacío para que ningún proxy
// corte el stream por inactividad.
var latidoSSE = 25 * time.Second

// EventsHandler expone el canal de eventos en tiempo real (RF-11) por SSE.
type EventsHandler struct {
	service ports.EventosService

	apagado chan struct{} // se cierra al apagar la API: corta todos los streams
	una     sync.Once
}

// NewEventsHandler crea el handler del canal de eventos.
func NewEventsHandler(service ports.EventosService) *EventsHandler {
	return &EventsHandler{service: service, apagado: make(chan struct{})}
}

// CerrarStreams corta todos los streams abiertos. Se llama al apagar la API: si
// no, el apagado ordenado del servidor HTTP esperaría para siempre a conexiones
// que no terminan. EventSource reconecta y, con el ticket consumido, el front
// pide uno nuevo (ver docs/eventos-tiempo-real.md).
func (h *EventsHandler) CerrarStreams() {
	h.una.Do(func() { close(h.apagado) })
}

// ==========================================
// POST /api/events/ticket
// ==========================================

// EmitirTicket genera un ticket de un solo uso para abrir el stream de eventos.
//
// @Summary      Pedir ticket para el stream de eventos
// @Description  Devuelve un ticket de un solo uso, válido por 30 s, para abrir GET /api/events?ticket=... El navegador (EventSource) no puede mandar el header Authorization, y así el JWT nunca viaja en la URL. Esta operación sí usa `Authorization: Bearer <accessToken>`; el ticket es el que se lo reemplaza en el stream.
// @Tags         Eventos en tiempo real
// @Produce      json
// @Security     BearerAuth
// @Success      200 {object} SSETicketResponse "ticket de un solo uso, válido 30 s"
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      503 {object} ErrorResponse "EVENTS_UNAVAILABLE"
// @Router       /events/ticket [post]
func (h *EventsHandler) EmitirTicket(c *gin.Context) {
	sesionVal, _ := c.Get(middleware.ContextKeySesionID)
	sesionID, ok := sesionVal.(uuid.UUID)
	if !ok {
		SendError(c, http.StatusUnauthorized, "TOKEN_REVOKED", "La sesión no es válida.")
		return
	}
	ticket, err := h.service.EmitirTicket(c.Request.Context(), extraerUserID(c), sesionID)
	if err != nil {
		log.Printf("[EVENTOS] %v", err)
		SendError(c, http.StatusServiceUnavailable, "EVENTS_UNAVAILABLE", "No se pudo emitir el ticket de eventos, intentá nuevamente.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ticket": ticket})
}

// ==========================================
// GET /api/events?ticket=<uuid>
// ==========================================

// Stream abre el canal de eventos (Server-Sent Events).
//
// No declara `@Security`: esta ruta no pasa por el middleware de Bearer. El
// `EventSource` del navegador no puede mandar `Authorization` y poner el JWT en
// la URL lo dejaría en logs, historial y proxies, así que se autentica con el
// ticket de un solo uso de `POST /api/events/ticket`, que se consume al conectar.
//
// @Summary      Stream de eventos en tiempo real (SSE)
// @Description  Abre un stream `text/event-stream` autenticado con el ticket de `POST /api/events/ticket`, que es de un solo uso y vence a los 30 s: si se conecta dos veces o después de vencer, responde 401 y hay que pedir otro. El ticket se consume con `GETDEL` al abrir el stream y el servidor manda `Cache-Control: no-cache`, `Connection: keep-alive` y `X-Accel-Buffering: no` para que un proxy nginx no acumule el stream. El framing tiene cuatro formas: (1) `: conectado` apenas se abre, que `EventSource` ignora; (2) un frame `id: <evento.id>` seguido de `data: <http.SSEEventPayload en JSON>` por cada evento, que `EventSource` entrega en `onmessage`; (3) `: ping` cada 25 s para que ningún proxy corte la conexión por inactividad; (4) `event: cierre` con `{"motivo": ...}` cuando el backend corta el stream, con motivo `LOGOUT`, `SESSIONS_REVOKED` o `USER_INACTIVE`. Importante: `EventSource` reconecta solo con la misma URL, y el ticket ya fue consumido, por lo que hay que cerrar la fuente y abrir un stream nuevo con un ticket nuevo.
// @Tags         Eventos en tiempo real
// @Produce      text/event-stream
// @Param        ticket query string true "Ticket de un solo uso"
// @Success      200 {object} SSEEventPayload "evento del stream, envuelto en el frame data: del protocolo SSE"
// @Failure      401 {object} ErrorResponse "EVENTS_TICKET_MISSING | EVENTS_TICKET_INVALID"
// @Failure      503 {object} ErrorResponse "EVENTS_UNAVAILABLE"
// @Router       /events [get]
func (h *EventsHandler) Stream(c *gin.Context) {
	ticket := c.Query("ticket")
	if ticket == "" {
		SendError(c, http.StatusUnauthorized, "EVENTS_TICKET_MISSING", "Se requiere un ticket para abrir el stream de eventos.")
		return
	}
	conexion, err := h.service.Conectar(c.Request.Context(), ticket)
	if errors.Is(err, ports.ErrTicketInvalido) {
		SendError(c, http.StatusUnauthorized, "EVENTS_TICKET_INVALID", "El ticket es inválido, venció o ya fue utilizado. Pedí uno nuevo.")
		return
	}
	if err != nil {
		log.Printf("[EVENTOS] %v", err)
		SendError(c, http.StatusServiceUnavailable, "EVENTS_UNAVAILABLE", "El canal de eventos no está disponible, intentá nuevamente.")
		return
	}
	defer conexion.Cerrar()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // que un proxy nginx no acumule el stream
	c.Status(http.StatusOK)
	escribir(c, ": conectado\n\n")

	latido := time.NewTicker(latidoSSE)
	defer latido.Stop()
	for {
		select {
		case evento, abierto := <-conexion.Eventos:
			if !abierto {
				// El backend cortó el stream: avisar el motivo si llegó.
				select {
				case motivo := <-conexion.Cierre:
					escribirCierre(c, motivo)
				default:
				}
				return
			}
			payload, _ := json.Marshal(evento)
			escribir(c, fmt.Sprintf("id: %s\ndata: %s\n\n", evento.ID, payload))
		case motivo := <-conexion.Cierre:
			escribirCierre(c, motivo)
			return
		case <-latido.C:
			escribir(c, ": ping\n\n")
		case <-c.Request.Context().Done():
			return // el cliente cerró la conexión
		case <-h.apagado:
			return // la API se está apagando
		}
	}
}

func escribirCierre(c *gin.Context, motivo string) {
	payload, _ := json.Marshal(gin.H{"motivo": motivo})
	escribir(c, fmt.Sprintf("event: cierre\ndata: %s\n\n", payload))
}

func escribir(c *gin.Context, texto string) {
	_, _ = c.Writer.WriteString(texto)
	c.Writer.Flush()
}
