package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
)

const (
	// ttlTicket: el ticket se usa enseguida para abrir el stream; si no, vence.
	ttlTicket     = 30 * time.Second
	prefijoTicket = "ws_ticket:"

	// Motivos por los que el backend corta un stream.
	MotivoLogout            = ports.MensajeLogout            // se cerró esa sesión
	MotivoSesionesRevocadas = ports.MensajeSesionesRevocadas // se revocaron todas las sesiones del usuario
	MotivoUsuarioInactivo   = "USER_INACTIVE"                // el usuario fue desactivado o eliminado
)

// ticketEventos es el valor de ws_ticket:<uuid>: el usuario y también la sesión,
// para cortar solo el stream de esa sesión en un logout y para la auditoría.
type ticketEventos struct {
	UsuarioID uuid.UUID `json:"usuario_id"`
	SesionID  uuid.UUID `json:"sesion_id"`
}

// eventosService implementa ports.EventosService.
type eventosService struct {
	kv           ports.KeyValueStore
	bus          busEventos
	authRepo     ports.AuthRepository     // usuario (activo, rol) y sesión
	instanceRepo ports.InstanceRepository // permisos de un OPERATOR sobre cada instancia
	auditSvc     ports.AuditService
}

// NewEventosService crea el servicio del canal de eventos en tiempo real.
func NewEventosService(kv ports.KeyValueStore, authRepo ports.AuthRepository, instanceRepo ports.InstanceRepository, auditSvc ports.AuditService) ports.EventosService {
	return &eventosService{kv: kv, bus: busEventos{kv: kv}, authRepo: authRepo, instanceRepo: instanceRepo, auditSvc: auditSvc}
}

// EmitirTicket: SET ws_ticket:<uuid v4> {usuario_id, sesion_id} EX 30.
// uuid.New() genera un UUID v4 con crypto/rand.
func (s *eventosService) EmitirTicket(ctx context.Context, usuarioID, sesionID uuid.UUID) (string, error) {
	ticket := uuid.New().String()
	if err := s.kv.Set(ctx, prefijoTicket+ticket, ticketEventos{UsuarioID: usuarioID, SesionID: sesionID}, ttlTicket); err != nil {
		return "", fmt.Errorf("no se pudo emitir el ticket de eventos: %w", err)
	}
	return ticket, nil
}

// Conectar consume el ticket con GETDEL (un solo uso), verifica que la sesión
// y el usuario sigan activos y se suscribe al bus.
func (s *eventosService) Conectar(ctx context.Context, ticket string) (*ports.ConexionEventos, error) {
	if ticket == "" {
		return nil, ports.ErrTicketInvalido
	}
	valor, err := s.kv.GetDel(ctx, prefijoTicket+ticket)
	if errors.Is(err, ports.ErrClaveNoEncontrada) {
		return nil, ports.ErrTicketInvalido
	}
	if err != nil {
		return nil, fmt.Errorf("no se pudo validar el ticket de eventos: %w", err)
	}
	var t ticketEventos
	if err := json.Unmarshal([]byte(valor), &t); err != nil {
		return nil, ports.ErrTicketInvalido
	}

	// El ticket vive 30 s, pero en ese lapso la sesión pudo cerrarse.
	sesion, err := s.authRepo.BuscarSesionPorID(ctx, t.SesionID)
	if err != nil || !sesion.Activa || !time.Now().Before(sesion.FechaExpiracion) || sesion.UsuarioID != t.UsuarioID {
		return nil, ports.ErrTicketInvalido
	}
	if usuario, err := s.authRepo.BuscarUsuarioPorID(ctx, t.UsuarioID); err != nil || !usuario.Activo {
		return nil, ports.ErrTicketInvalido
	}

	mensajes, cancelarSuscripcion, err := s.kv.Subscribe(ctx, ports.CanalEventos)
	if err != nil {
		return nil, fmt.Errorf("no se pudo abrir la suscripción al bus de eventos: %w", err)
	}

	eventos := make(chan ports.RealtimeEvent, 16)
	cierre := make(chan string, 1)
	fin := make(chan struct{})
	var unaVez sync.Once
	cerrar := func() {
		unaVez.Do(func() {
			close(fin)
			_ = cancelarSuscripcion()
		})
	}

	go func() {
		defer close(eventos)
		for m := range mensajes {
			var msg ports.MensajeBus
			if err := json.Unmarshal([]byte(m.Contenido), &msg); err != nil {
				log.Printf("[EVENTOS] mensaje del bus con formato inválido: %v", err)
				continue
			}
			motivo := s.motivoDeCierre(msg, t)
			if motivo == "" && msg.Tipo == ports.MensajeEvento && msg.Evento != nil {
				visible, inactivo := s.puedeVer(context.Background(), t.UsuarioID, *msg.Evento)
				if inactivo {
					motivo = MotivoUsuarioInactivo
				} else if visible {
					select {
					case eventos <- *msg.Evento:
					case <-fin:
						return
					}
				}
			}
			if motivo != "" {
				s.registrarCierre(t, motivo)
				cierre <- motivo
				cerrar()
				return
			}
		}
	}()

	s.auditSvc.Registrar(ctx, ports.RegistrarAuditoriaInput{
		UsuarioID: t.UsuarioID,
		Accion:    ports.AccionEventosConexion,
		Resultado: ports.ResultadoExito,
		Detalles:  map[string]any{"sesion_id": t.SesionID.String()},
	})
	log.Printf("[EVENTOS] stream abierto | user=%s | session=%s", t.UsuarioID, t.SesionID)

	return &ports.ConexionEventos{UsuarioID: t.UsuarioID, SesionID: t.SesionID, Eventos: eventos, Cierre: cierre, Cerrar: cerrar}, nil
}

// motivoDeCierre dice si un mensaje de control corta el stream de este ticket.
func (s *eventosService) motivoDeCierre(msg ports.MensajeBus, t ticketEventos) string {
	switch msg.Tipo {
	case ports.MensajeLogout:
		if msg.SesionID != nil && *msg.SesionID == t.SesionID {
			return MotivoLogout
		}
	case ports.MensajeSesionesRevocadas:
		if msg.UsuarioID != nil && *msg.UsuarioID == t.UsuarioID {
			return MotivoSesionesRevocadas
		}
	}
	return ""
}

// puedeVer decide, en el momento de cada evento, si el usuario lo puede recibir:
// ADMIN ve todo; OPERATOR solo los eventos de instancias sobre las que tiene
// permiso (cualquier nivel). Los eventos que no son de una VM o LXC (por
// ejemplo, saturación del nodo) son solo para ADMIN. Como se consulta en vivo,
// si un admin le quita un permiso deja de recibir esos eventos al instante.
// Si el usuario ya no está activo devuelve inactivo = true.
func (s *eventosService) puedeVer(ctx context.Context, usuarioID uuid.UUID, ev ports.RealtimeEvent) (visible, inactivo bool) {
	usuario, err := s.authRepo.BuscarUsuarioPorID(ctx, usuarioID)
	if err != nil || !usuario.Activo {
		return false, true
	}
	if usuario.Rol == "ADMIN" {
		return true, false
	}
	if ev.RecursoTipo != ports.RecursoVM && ev.RecursoTipo != ports.RecursoLXC {
		return false, false
	}
	vmid, err := strconv.Atoi(ev.RecursoID)
	if err != nil {
		return false, false
	}
	permitido, err := s.instanceRepo.VerificarAcceso(ctx, usuarioID, vmid, ports.NivelAccesoReadOnly)
	if err != nil {
		log.Printf("[EVENTOS] no se pudo verificar el permiso de %s sobre %d: %v", usuarioID, vmid, err)
		return false, false
	}
	return permitido, false
}

func (s *eventosService) registrarCierre(t ticketEventos, motivo string) {
	log.Printf("[EVENTOS] stream cortado por el backend | user=%s | session=%s | motivo=%s", t.UsuarioID, t.SesionID, motivo)
	s.auditSvc.Registrar(context.Background(), ports.RegistrarAuditoriaInput{
		UsuarioID: t.UsuarioID,
		Accion:    ports.AccionEventosCierre,
		Resultado: ports.ResultadoExito,
		Detalles:  map[string]any{"sesion_id": t.SesionID.String(), "motivo": motivo},
	})
}

// Publicar envía un evento a todos los clientes que puedan verlo.
func (s *eventosService) Publicar(ctx context.Context, evento ports.RealtimeEvent) error {
	return s.bus.publicar(ctx, ports.MensajeBus{Tipo: ports.MensajeEvento, Evento: &evento})
}
