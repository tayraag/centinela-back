package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"

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

// BuscarTareasActivasPorVmids devuelve un mapa vmid → tarea en curso para todas
// las tareas en estado RUNNING que correspondan a alguno de los vmids dados.
// Se hace en un único query para no iterar N veces contra la DB.
func (r *TareaRepository) BuscarTareasActivasPorVmids(ctx context.Context, vmids []int) (map[int]ports.ActiveTaskDTO, error) {
	if len(vmids) == 0 {
		return map[int]ports.ActiveTaskDTO{}, nil
	}

	// instancia_id está guardado como string (strconv.Itoa(vmid)) en seguimiento_tareas.go
	vmidStrs := make([]string, len(vmids))
	for i, v := range vmids {
		vmidStrs[i] = strconv.Itoa(v)
	}

	var tareas []domain.TareaAsincrona
	if err := r.db.WithContext(ctx).
		Where("instancia_id IN ? AND estado = ?", vmidStrs, ports.TareaRunning).
		Order("fecha_creacion ASC").Find(&tareas).Error; err != nil {
		return nil, fmt.Errorf("error al buscar tareas activas: %w", err)
	}

	resultado := make(map[int]ports.ActiveTaskDTO, len(tareas))
	for _, t := range tareas {
		vmid, err := strconv.Atoi(t.InstanciaID)
		if err != nil {
			continue // instancia_id malformado: ignorar silenciosamente
		}
		// Si hay más de una tarea RUNNING para el mismo vmid (no debería pasar),
		// se queda con la más reciente.
		resultado[vmid] = ports.ActiveTaskDTO{TareaID: t.ID.String(), Action: strings.ToUpper(t.Accion), Status: t.Estado}
	}
	return resultado, nil
}

// ListarEnCurso devuelve las tareas RUNNING, de la más vieja a la más nueva.
func (r *TareaRepository) ListarEnCurso(ctx context.Context) ([]domain.TareaAsincrona, error) {
	var tareas []domain.TareaAsincrona
	if err := r.db.WithContext(ctx).Where("estado = ?", ports.TareaRunning).
		Order("fecha_creacion ASC").Find(&tareas).Error; err != nil {
		return nil, fmt.Errorf("error al listar tareas en curso: %w", err)
	}
	return tareas, nil
}
