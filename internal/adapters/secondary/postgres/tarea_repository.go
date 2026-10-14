package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
func (r *TareaRepository) Crear(ctx context.Context, tarea *domain.TareaAsincrona, auditoria ports.RegistrarAuditoriaInput) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(tarea).Error; err != nil {
			return fmt.Errorf("error al registrar tarea: %w", err)
		}
		return NewAuditRepository(tx).Registrar(ctx, auditoria)
	})
}

// ActualizarEstado guarda el estado final de la tarea y sus metadatos (JSONB).
// Para una creación que Proxmox dio por completada (OK o WARNINGS), la misma
// transacción otorga FULL_ACCESS al usuario creador. Así nunca queda una tarea
// COMPLETED sin su permiso, ni se publica TASK_FINISHED antes de que el
// inventario sea visible.
func (r *TareaRepository) ActualizarEstado(ctx context.Context, id uuid.UUID, estado string, metadatos map[string]any, auditoria ports.RegistrarAuditoriaInput) error {
	if estado != ports.TareaCompleted && estado != ports.TareaFailed {
		return fmt.Errorf("estado de cierre inválido: %s", estado)
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var tarea domain.TareaAsincrona
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&tarea, "id = ?", id).Error; err != nil {
			return err
		}
		if tarea.Estado != ports.TareaRunning {
			return nil // otro worker ya confirmó el cierre y su auditoría
		}
		if metadatos == nil {
			metadatos = map[string]any{}
		}
		if debeAsignarPermisoCreacion(tarea, estado, metadatos) {
			vmid, err := strconv.Atoi(tarea.InstanciaID)
			if err != nil {
				return fmt.Errorf("VMID reservado inválido en tarea de creación %s: %w", tarea.ID, err)
			}
			permiso := domain.PermisoInstancia{
				UsuarioID: tarea.UsuarioID, VmidProxmox: vmid, NivelAcceso: ports.NivelAccesoFullAccess,
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&permiso).Error; err != nil {
				return fmt.Errorf("error al autoasignar permiso de la instancia %d: %w", vmid, err)
			}
		}
		if err := NewAuditRepository(tx).Registrar(ctx, auditoria); err != nil {
			return err
		}
		crudo, err := json.Marshal(metadatos)
		if err != nil {
			return fmt.Errorf("error al serializar metadatos de la tarea: %w", err)
		}
		return tx.Model(&tarea).Updates(map[string]any{"estado": estado, "metadatos": string(crudo)}).Error
	})
}

func debeAsignarPermisoCreacion(tarea domain.TareaAsincrona, estado string, metadatos map[string]any) bool {
	_, esCreacion := ports.TipoRecursoCreacion(tarea.Accion)
	exitstatus, _ := metadatos["exitstatus"].(string)
	exitoProxmox := exitstatus == "OK" || strings.HasPrefix(exitstatus, "WARNINGS")
	return esCreacion && estado == ports.TareaCompleted && exitoProxmox
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
		resultado[vmid] = ports.ActiveTaskDTO{TareaID: t.ID.String(), Action: ports.AccionPublicaTarea(t.Accion), Status: t.Estado}
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
