// Contrato base del canal de eventos en tiempo real (RF-11).
// Define el esquema genérico de evento que viajará por el canal.
// El servidor WebSocket, las colas y los workers están fuera de alcance:
// este archivo es solo el contrato de datos compartido por todo el back.
package ports

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ==========================================
// Constantes del contrato de eventos
// ==========================================

// Tipos de evento permitidos. Los nombres en Go van en español y los valores
// en inglés MAYÚSCULA, respetando la convención de enums del proyecto.
const (
	EventoInstanciaEstado = "INSTANCE_STATE_CHANGED" // cambió el estado de una instancia
	EventoInstanciaCreada = "INSTANCE_CREATED"       // se creó una instancia
	EventoSaturacion      = "RESOURCE_SATURATION"    // un recurso superó su umbral de uso
	EventoTareaFinalizada = "TASK_FINISHED"          // terminó una tarea asincrónica
)

// Niveles de severidad permitidos para un evento.
const (
	SeveridadInfo     = "INFO"
	SeveridadWarning  = "WARNING"
	SeveridadCritical = "CRITICAL"
)

// Tipos de recurso que puede referenciar un evento.
const (
	RecursoVM   = "VM"
	RecursoLXC  = "LXC"
	RecursoNodo = "NODE"
)

// Estados posibles de una tarea asincrónica reportados en el evento.
const (
	TareaRunning   = "RUNNING"
	TareaCompleted = "COMPLETED"
	TareaFailed    = "FAILED"
)

// tiposEventoPermitidos reúne los valores válidos de EventoTipo para validar entradas.
var tiposEventoPermitidos = []string{
	EventoInstanciaEstado,
	EventoInstanciaCreada,
	EventoSaturacion,
	EventoTareaFinalizada,
}

// severidadesPermitidas reúne los valores válidos de severidad para validar entradas.
var severidadesPermitidas = []string{
	SeveridadInfo,
	SeveridadWarning,
	SeveridadCritical,
}

// ==========================================
// DTO: Evento en tiempo real
// ==========================================

// RealtimeEvent es el esquema genérico de evento que viaja por el canal en tiempo real.
// Es un contrato de datos puro: no acopla al emisor ni al transporte.
// Los campos obligatorios se validan en NewRealtimeEvent y los opcionales se
// completan con ConRecurso y ConDetalles.
//
// @name RealtimeEvent
// @Description Esquema genérico de evento del canal en tiempo real (RF-11). Los enums usan
// @Description valores en inglés MAYÚSCULA y los campos nombres en español camelCase.
type RealtimeEvent struct {
	ID          uuid.UUID      `json:"id"`
	Tipo        string         `json:"tipo"`
	Severidad   string         `json:"severidad"`
	RecursoTipo string         `json:"recursoTipo"`
	RecursoID   string         `json:"recursoId"`
	Mensaje     string         `json:"mensaje"`
	FechaHora   time.Time      `json:"fechaHora"`
	Detalles    map[string]any `json:"detalles,omitempty"`
}

// NewRealtimeEvent construye un evento validando tipo, severidad y mensaje.
// Solo inicializa los campos automáticos ID y FechaHora; el resto de los campos
// (recurso y detalles) los completa quien emite el evento con ConRecurso y ConDetalles.
func NewRealtimeEvent(tipo, severidad, mensaje string) (RealtimeEvent, error) {
	if tipo == "" {
		return RealtimeEvent{}, fmt.Errorf("el tipo de evento es obligatorio")
	}
	if !slices.Contains(tiposEventoPermitidos, tipo) {
		return RealtimeEvent{}, fmt.Errorf("tipo de evento no válido: %q", tipo)
	}

	if severidad == "" {
		return RealtimeEvent{}, fmt.Errorf("la severidad es obligatoria")
	}
	if !slices.Contains(severidadesPermitidas, severidad) {
		return RealtimeEvent{}, fmt.Errorf("severidad no válida: %q", severidad)
	}

	if strings.TrimSpace(mensaje) == "" {
		return RealtimeEvent{}, fmt.Errorf("el mensaje es obligatorio")
	}

	return RealtimeEvent{
		ID:        uuid.New(),
		Tipo:      tipo,
		Severidad: severidad,
		Mensaje:   mensaje,
		FechaHora: time.Now(),
	}, nil
}

// ConRecurso devuelve una copia del evento con el recurso afectado completado.
func (e RealtimeEvent) ConRecurso(tipoRecurso, recursoID string) RealtimeEvent {
	e.RecursoTipo = tipoRecurso
	e.RecursoID = recursoID
	return e
}

// ConDetalles devuelve una copia del evento con el mapa de detalles completado.
func (e RealtimeEvent) ConDetalles(detalles map[string]any) RealtimeEvent {
	e.Detalles = detalles
	return e
}
