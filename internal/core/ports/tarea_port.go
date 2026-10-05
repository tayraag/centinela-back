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

	// ActualizarEstado guarda el estado final (COMPLETED | FAILED) y, si no es
	// nil, los metadatos del cierre en la columna JSONB metadatos.
	ActualizarEstado(ctx context.Context, id uuid.UUID, estado string, metadatos map[string]any) error

	// BuscarTareasActivasPorVmids devuelve un mapa vmid → tarea en curso
	// (tareaId, acción en mayúsculas y estado RUNNING) de cualquiera de los
	// vmids dados. Si un vmid no tiene tarea activa, no aparece en el mapa.
	BuscarTareasActivasPorVmids(ctx context.Context, vmids []int) (map[int]ActiveTaskDTO, error)

	// ListarEnCurso devuelve todas las tareas en estado RUNNING, de la más vieja a
	// la más nueva. Lo usa el reconciliador del seguimiento de tareas.
	ListarEnCurso(ctx context.Context) ([]domain.TareaAsincrona, error)
}
