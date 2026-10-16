package ports

import (
	"context"
	"errors"
)

// ErrExclusionNoDisponible: no se pudo comprobar si la instancia está ocupada
// (falló la consulta de tareas en curso). La operación no se despacha.
var ErrExclusionNoDisponible = errors.New("no se pudo verificar si la instancia está ocupada")

// ExclusionInstancias garantiza que sobre una instancia corra una sola
// operación mutante a la vez (energía, borrado, configuración, snapshots).
type ExclusionInstancias interface {
	// Reservar toma la instancia para una operación: el lock Redis
	// lock:instance:{vmid}:operation (atómico, SET NX) y la verificación de que
	// no haya una tarea RUNNING en tareas_asincronas. Devuelve
	// ErrInstanciaOcupada si otra operación la tiene tomada o en curso, o
	// ErrExclusionNoDisponible si no se pudo verificar. liberar suelta el lock y
	// se llama cuando la operación ya quedó registrada (o falló).
	Reservar(ctx context.Context, vmid int) (liberar func(), err error)
}
