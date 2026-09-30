package services

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
)

// limiteSeguimiento: si Proxmox no da por terminada la tarea en este tiempo,
// se marca como fallida (una acción de energía normal tarda segundos).
const limiteSeguimiento = 10 * time.Minute

// nombresAccion traduce la acción al texto del mensaje del evento.
var nombresAccion = map[string]string{"start": "encendido", "stop": "apagado"}

// seguimientoTareas implementa ports.SeguimientoTareas.
type seguimientoTareas struct {
	proxmox   ports.ProxmoxPort
	tareas    ports.TareaRepository
	eventos   ports.EventosService
	intervalo time.Duration // cada cuánto se consulta el estado de la tarea
}

// NewSeguimientoTareas crea el seguidor de tareas. intervalo es cada cuánto se
// le pregunta a Proxmox cómo va la tarea (1 s en la API).
func NewSeguimientoTareas(proxmox ports.ProxmoxPort, tareas ports.TareaRepository, eventos ports.EventosService, intervalo time.Duration) ports.SeguimientoTareas {
	return &seguimientoTareas{proxmox: proxmox, tareas: tareas, eventos: eventos, intervalo: intervalo}
}

// Seguir registra la tarea en tareas_asincronas (RUNNING) y la sigue en segundo plano.
func (s *seguimientoTareas) Seguir(ctx context.Context, usuarioID uuid.UUID, vmid int, accion, upid string) (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, err
	}
	tarea := &domain.TareaAsincrona{
		ID: id, UsuarioID: usuarioID, UpidProxmox: upid,
		InstanciaID: strconv.Itoa(vmid), Accion: accion, Estado: ports.TareaRunning,
	}
	if err := s.tareas.Crear(ctx, tarea); err != nil {
		return uuid.Nil, err
	}
	go s.seguir(tarea, vmid)
	return id, nil
}

// seguir consulta el estado hasta que Proxmox la da por terminada (o se vence
// el límite), guarda el estado final y publica TASK_FINISHED. Corre fuera del
// request, así que usa su propio contexto.
func (s *seguimientoTareas) seguir(tarea *domain.TareaAsincrona, vmid int) {
	ctx, cancel := context.WithTimeout(context.Background(), limiteSeguimiento)
	defer cancel()

	recursoTipo := ports.RecursoVM
	if inst, err := s.proxmox.ObtenerInstancia(ctx, vmid); err == nil && inst.Tipo == ports.TipoInstanciaLXC {
		recursoTipo = ports.RecursoLXC
	}

	estado, detalleError := ports.TareaFailed, "Proxmox no dio por terminada la tarea a tiempo"
	ticker := time.NewTicker(s.intervalo)
	defer ticker.Stop()
esperar:
	for {
		select {
		case <-ctx.Done():
			break esperar
		case <-ticker.C:
			resultado, err := s.proxmox.EstadoTarea(ctx, tarea.UpidProxmox)
			if err != nil {
				log.Printf("[TAREAS] no se pudo consultar la tarea %s (se reintenta): %v", tarea.ID, err)
				continue
			}
			if !resultado.Terminada {
				continue
			}
			if resultado.ExitStatus == "OK" {
				estado, detalleError = ports.TareaCompleted, ""
			} else {
				estado, detalleError = ports.TareaFailed, resultado.ExitStatus
			}
			break esperar
		}
	}

	// El contexto de seguimiento pudo haber vencido: el cierre usa uno nuevo.
	ctxFinal, cancelFinal := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelFinal()
	if err := s.tareas.ActualizarEstado(ctxFinal, tarea.ID, estado); err != nil {
		log.Printf("[TAREAS] %v", err)
	}
	log.Printf("[TAREAS] tarea %s (%s sobre %d) terminó: %s", tarea.ID, tarea.Accion, vmid, estado)

	if err := s.eventos.Publicar(ctxFinal, eventoTareaFinalizada(tarea, recursoTipo, estado, detalleError)); err != nil {
		log.Printf("[TAREAS] no se pudo publicar TASK_FINISHED de la tarea %s: %v", tarea.ID, err)
	}
}

// eventoTareaFinalizada arma el TASK_FINISHED de docs/contrato-eventos.md.
func eventoTareaFinalizada(tarea *domain.TareaAsincrona, recursoTipo, estado, detalleError string) ports.RealtimeEvent {
	nombre := nombresAccion[tarea.Accion]
	if nombre == "" {
		nombre = tarea.Accion
	}
	severidad, mensaje := ports.SeveridadInfo, fmt.Sprintf("La tarea de %s finalizó correctamente", nombre)
	detalles := map[string]any{"tareaId": tarea.ID.String(), "estado": estado, "accion": tarea.Accion}
	if estado != ports.TareaCompleted {
		severidad, mensaje = ports.SeveridadWarning, fmt.Sprintf("La tarea de %s falló", nombre)
		detalles["error"] = detalleError
	}
	// NewRealtimeEvent solo falla con tipo/severidad/mensaje inválidos, y acá son constantes.
	evento, _ := ports.NewRealtimeEvent(ports.EventoTareaFinalizada, severidad, mensaje)
	return evento.ConRecurso(recursoTipo, tarea.InstanciaID).ConDetalles(detalles)
}
