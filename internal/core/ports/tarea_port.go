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
}
