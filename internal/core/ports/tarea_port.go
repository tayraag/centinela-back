package ports

import (
	"context"

	"el-centinela/internal/core/domain"

	"github.com/google/uuid"
)


// TareaRepository persiste las tareas asíncronas de Proxmox (tabla tareas_asincronas).
type TareaRepository interface {
	// Crear inserta la tarea (estado RUNNING).
	Crear(ctx context.Context, tarea *domain.TareaAsincrona) error

	// ActualizarEstado guarda el estado final (COMPLETED | FAILED).
	ActualizarEstado(ctx context.Context, id uuid.UUID, estado string) error

	// BuscarTareasActivasPorVmids devuelve un mapa vmid → tareaId (string)
	// para las tareas en estado RUNNING de cualquiera de los vmids dados.
	// Si un vmid no tiene tarea activa, no aparece en el mapa.
	BuscarTareasActivasPorVmids(ctx context.Context, vmids []int) (map[int]string, error)
}
