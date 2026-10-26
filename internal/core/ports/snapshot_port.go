package ports

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// AccionTareaSnapshot es la acción con que se registra la creación de un
// snapshot en tareas_asincronas; en TASK_FINISHED y activeTask llega como SNAPSHOT.
const AccionTareaSnapshot = "snapshot"

// LargoMaximoDescripcionSnapshot acota la descripción de un snapshot.
const LargoMaximoDescripcionSnapshot = 255

// ErrSnapshotDescripcionInvalida: la descripción supera el largo máximo.
var ErrSnapshotDescripcionInvalida = errors.New("descripción de snapshot demasiado larga")

// SnapshotCreado es lo que devuelve la creación aceptada por Proxmox.
type SnapshotCreado struct {
	TareaID uuid.UUID
	Nombre  string
}

// SnapshotService gestiona los puntos de restauración de una instancia (RF-06).
type SnapshotService interface {
	// Listar devuelve los snapshots de la instancia, del más nuevo al más viejo.
	Listar(ctx context.Context, vmid int) ([]SnapshotDTO, error)

	// Crear valida el nombre (ErrSnapshotNombreInvalido) y la descripción,
	// rechaza un nombre repetido (ErrSnapshotExistente), despacha la creación
	// en Proxmox y registra la tarea RUNNING con su auditoría PENDIENTE. Si
	// Proxmox aceptó pero no se pudo registrar el seguimiento devuelve
	// ErrSeguimientoNoRegistrado.
	Crear(ctx context.Context, usuarioID uuid.UUID, vmid int, nombre, descripcion string) (*SnapshotCreado, error)
}
