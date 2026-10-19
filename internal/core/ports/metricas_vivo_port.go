package ports

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrAccesoInstanciaDenegado: el usuario no tiene permiso (ni READ_ONLY) sobre la instancia.
var ErrAccesoInstanciaDenegado = errors.New("sin permisos sobre la instancia")

// Motivos con los que el backend cierra un WebSocket de métricas. Viajan como
// "reason" del frame de cierre (código 1008, policy violation) y son los mismos
// nombres que usa el SSE en su event: cierre.
const (
	MotivoWSLogout            = MensajeLogout            // el usuario cerró sesión
	MotivoWSSesionesRevocadas = MensajeSesionesRevocadas // se revocaron sus sesiones (baja, suspensión, resets)
	MotivoWSUsuarioInactivo   = "USER_INACTIVE"          // el usuario fue suspendido o eliminado
	MotivoWSAccesoDenegado    = "INSTANCE_ACCESS_DENIED" // perdió el permiso sobre la instancia
	MotivoWSCanalNoDisponible = "EVENTS_UNAVAILABLE"     // se perdió la suscripción al bus: sin ella no se pueden recibir revocaciones
)

// MetricasVivoService autentica y vigila las conexiones WebSocket de métricas
// en vivo (GET /api/ws/instances/:vmid/metrics).
type MetricasVivoService interface {
	// ConsumirTicket consume el ticket ws:ticket:<uuid> (GETDEL, un solo uso) y
	// valida que el usuario siga activo y pueda ver la instancia. Devuelve
	// ErrTicketInvalido (no existe, venció, ya se usó o la cuenta fue
	// eliminada), ErrCuentaSuspendida o ErrAccesoInstanciaDenegado.
	ConsumirTicket(ctx context.Context, ticket string, vmid int) (uuid.UUID, error)

	// Vigilar se suscribe a los avisos de corte del bus y revisa periódicamente
	// el estado del usuario y su permiso. Por el canal devuelto llega UN motivo
	// (MotivoWS*) cuando la conexión debe cerrarse; se libera al cancelar ctx.
	// Como el ticket solo identifica al usuario, un logout corta todas sus
	// conexiones WebSocket, no solo las de esa sesión.
	Vigilar(ctx context.Context, usuarioID uuid.UUID, vmid int) (<-chan string, error)
}
