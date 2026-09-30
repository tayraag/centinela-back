package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
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
}

// NewEventsHandler crea el handler del canal de eventos.
func NewEventsHandler(service ports.EventosService) *EventsHandler {
	return &EventsHandler{service: service}
}

// ==========================================
// POST /api/events/ticket
// ==========================================

// EmitirTicket genera un ticket de un solo uso para abrir el stream de eventos.
//
// @Summary      Pedir ticket para el stream de eventos
// @Description  Devuelve un ticket de un solo uso, válido por 30 segundos, para abrir GET /api/events?ticket=... El navegador (EventSource) no puede mandar el header Authorization, y así el JWT nunca viaja en la URL.
// @Tags         Eventos en tiempo real
// @Produce      json
// @Security     BearerAuth
// @Success      200 {object} map[string]string "ticket"
// @Failure      401 {object} ErrorResponse
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
// @Summary      Stream de eventos en tiempo real (SSE)
// @Description  Abre un stream text/event-stream. Requiere el ticket de POST /api/events/ticket, que se consume al conectar (un solo uso). Cada evento llega como `data: <RealtimeEvent en JSON>`. Si el backend corta el stream (logout, revocación de sesiones o usuario desactivado) envía antes `event: cierre` con `{"motivo": "..."}`. Cada 25 s llega un comentario `: ping` para mantener la conexión.
// @Tags         Eventos en tiempo real
// @Produce      text/event-stream
// @Param        ticket query string true "Ticket de un solo uso"
// @Success      200 {object} ports.RealtimeEvent "stream de eventos"
// @Failure      401 {object} ErrorResponse "EVENTS_TICKET_MISSING | EVENTS_TICKET_INVALID"
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
