package ports

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// AccionTareaSnapshot es la acción con que se registra la creación de un
// snapshot en tareas_asincronas; en TASK_FINISHED y activeTask llega como SNAPSHOT.
const AccionTareaSnapshot = "snapshot"

// Acciones de tareas_asincronas para revertir y borrar un snapshot; así llegan
// también en TASK_FINISHED y en activeTask; auditoría usa los códigos SNAPSHOT_*.
const (
	AccionTareaRollbackSnapshot = "ROLLBACK_SNAPSHOT"
	AccionTareaDeleteSnapshot   = "DELETE_SNAPSHOT"
)

// EstadoRevirtiendo es el status que muestran el inventario y el detalle
// mientras corre un rollback (estado transitorio; Proxmox no lo informa).
const EstadoRevirtiendo = "rolling_back"

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

	// Revertir y Eliminar validan el nombre (ErrSnapshotNombreInvalido),
	// exigen que el snapshot exista (ErrSnapshotNoEncontrado), despachan la
	// orden en Proxmox y registran la tarea RUNNING (ROLLBACK_SNAPSHOT /
	// DELETE_SNAPSHOT). Devuelven el tareaId.
	Revertir(ctx context.Context, usuarioID uuid.UUID, vmid int, nombre string) (uuid.UUID, error)
	Eliminar(ctx context.Context, usuarioID uuid.UUID, vmid int, nombre string) (uuid.UUID, error)
}
