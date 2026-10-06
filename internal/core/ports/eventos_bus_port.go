package ports

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ==========================================
// Bus de eventos en tiempo real (RF-11)
// ==========================================

// CanalEventos es el canal de Redis Pub/Sub por el que viajan todos los
// mensajes del bus: eventos para los clientes y avisos de control.
const CanalEventos = "centinela:events"

// Tipos de mensaje del bus.
const (
	// MensajeEvento lleva un RealtimeEvent para los clientes conectados.
	MensajeEvento = "EVENT"
	// MensajeLogout avisa que se cerró una sesión: se corta el stream de esa sesión.
	MensajeLogout = "LOGOUT"
	// MensajeSesionesRevocadas avisa que se revocaron todas las sesiones de un
	// usuario (desactivación, eliminación, reset de contraseña o de 2FA): se
	// cortan todos sus streams.
	MensajeSesionesRevocadas = "SESSIONS_REVOKED"
)

// MensajeBus es el sobre que viaja por CanalEventos (en JSON).
type MensajeBus struct {
	Tipo      string         `json:"tipo"`
	Evento    *RealtimeEvent `json:"evento,omitempty"`
	UsuarioID *uuid.UUID     `json:"usuarioId,omitempty"`
	SesionID  *uuid.UUID     `json:"sesionId,omitempty"`
}

// ErrTicketInvalido indica que el ticket no existe, venció o ya se usó.
var ErrTicketInvalido = errors.New("ticket de eventos inválido, vencido o ya utilizado")

// ConexionEventos es un stream abierto de un cliente.
type ConexionEventos struct {
	UsuarioID uuid.UUID
	SesionID  uuid.UUID
	// Eventos entrega los eventos que este usuario puede ver (ya filtrados por rol y permisos).
	Eventos <-chan RealtimeEvent
	// Cierre recibe el motivo cuando el backend corta la conexión (LOGOUT,
	// SESSIONS_REVOKED, USER_INACTIVE). Después de eso Eventos se cierra.
	Cierre <-chan string
	// Cerrar libera la suscripción (llamarlo siempre al terminar).
	Cerrar func()
}

// EventosService es la lógica del canal GET /api/events.
type EventosService interface {
	// EmitirTicket genera un ticket de un solo uso (SET ws_ticket:<uuid> ... EX 30)
	// para abrir el stream sin exponer el JWT en la URL.
	EmitirTicket(ctx context.Context, usuarioID, sesionID uuid.UUID) (string, error)

	// Conectar consume el ticket (GETDEL) y abre la suscripción al bus.
	// Devuelve ErrTicketInvalido si no existe, venció o ya se usó.
	Conectar(ctx context.Context, ticket string) (*ConexionEventos, error)

	// Publicar envía un evento a todos los clientes que puedan verlo.
	Publicar(ctx context.Context, evento RealtimeEvent) error
}

// SeguimientoTareas registra una tarea asíncrona de Proxmox (UPID), la sigue
// hasta que termina y publica TASK_FINISHED en el bus.
type SeguimientoTareas interface {
	// Seguir registra la tarea en tareas_asincronas y empieza a seguirla en
	// segundo plano. Devuelve el ID de la tarea (el "tareaId" del evento).
	Seguir(ctx context.Context, usuarioID uuid.UUID, vmid int, accion, upid string, metadatos ...map[string]any) (uuid.UUID, error)
}
