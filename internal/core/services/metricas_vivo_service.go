package services

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
)

// prefijoTicketWS es la clave del ticket del WebSocket de métricas (RF-05):
// SET ws:ticket:<uuid> <usuario_id> EX 30.
const prefijoTicketWS = "ws:ticket:"

// metricasVivoService implementa ports.MetricasVivoService.
type metricasVivoService struct {
	kv        ports.KeyValueStore
	authRepo  ports.AuthRepository
	instancia ports.InstanceRepository
	intervalo time.Duration // cada cuánto se revisan el usuario y su permiso
}

// NewMetricasVivoService crea el servicio. intervalo <= 0 usa 2 s, el mismo
// ritmo con el que el WebSocket envía métricas.
func NewMetricasVivoService(kv ports.KeyValueStore, authRepo ports.AuthRepository, instancia ports.InstanceRepository, intervalo time.Duration) ports.MetricasVivoService {
	if intervalo <= 0 {
		intervalo = 2 * time.Second
	}
	return &metricasVivoService{kv: kv, authRepo: authRepo, instancia: instancia, intervalo: intervalo}
}

func (s *metricasVivoService) ConsumirTicket(ctx context.Context, ticket string, vmid int) (uuid.UUID, error) {
	ticket = strings.TrimSpace(ticket)
	if ticket == "" {
		return uuid.Nil, ports.ErrTicketInvalido
	}
	valor, err := s.kv.GetDel(ctx, prefijoTicketWS+ticket)
	if err != nil {
		if !errors.Is(err, ports.ErrClaveNoEncontrada) {
			log.Printf("[WS] no se pudo validar el ticket: %v", err)
		}
		return uuid.Nil, ports.ErrTicketInvalido
	}
	usuarioID, err := uuid.Parse(valor)
	if err != nil {
		return uuid.Nil, ports.ErrTicketInvalido
	}
	motivo, err := s.verificar(ctx, usuarioID, vmid)
	if err != nil {
		// Fail-closed: si no se puede comprobar el acceso, no se abre el canal.
		return uuid.Nil, err
	}
	if motivo != "" {
		switch motivo {
		case ports.MotivoWSAccesoDenegado:
			return uuid.Nil, ports.ErrAccesoInstanciaDenegado
		case ports.MotivoWSUsuarioInactivo:
			return uuid.Nil, s.errorDeCuenta(ctx, usuarioID)
		}
		return uuid.Nil, ports.ErrTicketInvalido
	}
	return usuarioID, nil
}

// errorDeCuenta: una cuenta eliminada no existe para la autenticación (ticket
// inválido); una suspendida responde ErrCuentaSuspendida.
func (s *metricasVivoService) errorDeCuenta(ctx context.Context, usuarioID uuid.UUID) error {
	if u, err := s.authRepo.BuscarUsuarioPorID(ctx, usuarioID); err == nil && u.EliminadoEn == nil {
		return ports.ErrCuentaSuspendida
	}
	return ports.ErrTicketInvalido
}

func (s *metricasVivoService) Vigilar(ctx context.Context, usuarioID uuid.UUID, vmid int) (<-chan string, error) {
	mensajes, cancelar, err := s.kv.Subscribe(ctx, ports.CanalEventos)
	if err != nil {
		return nil, err
	}
	motivos := make(chan string, 1)
	go func() {
		defer func() { _ = cancelar() }()
		ticker := time.NewTicker(s.intervalo)
		defer ticker.Stop()
		cortar := func(motivo string) {
			log.Printf("[WS] se corta el WebSocket de métricas | user=%s | vmid=%d | motivo=%s", usuarioID, vmid, motivo)
			motivos <- motivo
		}
		for {
			select {
			case <-ctx.Done():
				return
			case m, ok := <-mensajes:
				if !ok {
					// Sin el bus no llegarían los avisos de logout: fail-closed.
					if ctx.Err() == nil {
						cortar(ports.MotivoWSCanalNoDisponible)
					}
					return
				}
				if motivo := motivoDeCorteWS(m.Contenido, usuarioID); motivo != "" {
					cortar(motivo)
					return
				}
			case <-ticker.C:
				motivo, err := s.verificar(ctx, usuarioID, vmid)
				if err != nil {
					// Un error de la base no es evidencia de que perdió el acceso: se
					// reintenta en el próximo intervalo y los avisos del bus siguen llegando.
					log.Printf("[WS] no se pudo revisar el acceso de %s sobre %d: %v", usuarioID, vmid, err)
					continue
				}
				if motivo != "" {
					cortar(motivo)
					return
				}
			}
		}
	}()
	return motivos, nil
}

// motivoDeCorteWS: un LOGOUT o SESSIONS_REVOKED del usuario corta todas sus
// conexiones (el ticket no guarda la sesión).
func motivoDeCorteWS(contenido string, usuarioID uuid.UUID) string {
	var msg ports.MensajeBus
	if err := json.Unmarshal([]byte(contenido), &msg); err != nil || msg.UsuarioID == nil || *msg.UsuarioID != usuarioID {
		return ""
	}
	switch msg.Tipo {
	case ports.MensajeLogout:
		return ports.MotivoWSLogout
	case ports.MensajeSesionesRevocadas:
		return ports.MotivoWSSesionesRevocadas
	}
	return ""
}

// verificar lee el estado actual del usuario y su permiso sobre la instancia.
// Devuelve el motivo de corte ("" si puede seguir) o un error si no se pudo
// consultar la base: cada llamador decide qué hacer con ese error.
func (s *metricasVivoService) verificar(ctx context.Context, usuarioID uuid.UUID, vmid int) (string, error) {
	usuario, err := s.authRepo.BuscarUsuarioPorID(ctx, usuarioID)
	if errors.Is(err, ports.ErrUsuarioNoEncontrado) {
		return ports.MotivoWSUsuarioInactivo, nil
	}
	if err != nil {
		return "", err
	}
	if usuario.EliminadoEn != nil || !usuario.Activo {
		return ports.MotivoWSUsuarioInactivo, nil
	}
	if usuario.Rol == "ADMIN" {
		return "", nil
	}
	tiene, err := s.instancia.VerificarAcceso(ctx, usuarioID, vmid, ports.NivelAccesoReadOnly)
	if err != nil {
		return "", err
	}
	if !tiene {
		return ports.MotivoWSAccesoDenegado, nil
	}
	return "", nil
}
