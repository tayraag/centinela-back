package postgres

import (
	"context"
	"fmt"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TareaRepository implementa ports.TareaRepository sobre la tabla tareas_asincronas.
type TareaRepository struct {
	db *gorm.DB
}

var _ ports.TareaRepository = (*TareaRepository)(nil)

// NewTareaRepository crea el repositorio de tareas asíncronas.
func NewTareaRepository(db *gorm.DB) *TareaRepository {
	return &TareaRepository{db: db}
}

// Crear inserta la tarea (se registra en estado RUNNING al disparar la acción).
func (r *TareaRepository) Crear(ctx context.Context, tarea *domain.TareaAsincrona) error {
	if err := r.db.WithContext(ctx).Create(tarea).Error; err != nil {
		return fmt.Errorf("error al registrar tarea: %w", err)
	}
	return nil
}

// ActualizarEstado guarda el estado final de la tarea.
func (r *TareaRepository) ActualizarEstado(ctx context.Context, id uuid.UUID, estado string) error {
	if err := r.db.WithContext(ctx).Model(&domain.TareaAsincrona{}).Where("id = ?", id).
		Update("estado", estado).Error; err != nil {
		return fmt.Errorf("error al actualizar tarea: %w", err)
	}
	return nil
}
