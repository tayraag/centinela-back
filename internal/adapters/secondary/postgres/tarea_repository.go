package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

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
// Para una creación que Proxmox dio por completada con exitstatus OK, la misma
// transacción otorga FULL_ACCESS al usuario creador. Para una destrucción con
// ese mismo resultado, recolecta y elimina todos los permisos del VMID y deja
// su auditoría inmutable. TASK_FINISHED se publica después de este commit.
func (r *TareaRepository) ActualizarEstado(ctx context.Context, id uuid.UUID, estado string, metadatos map[string]any, auditoria ports.RegistrarAuditoriaInput) ([]uuid.UUID, error) {
	if estado != ports.TareaCompleted && estado != ports.TareaFailed {
		return nil, fmt.Errorf("estado de cierre inválido: %s", estado)
	}
	usuariosAfectados := make([]uuid.UUID, 0)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		if debePurgarPermisosDestruccion(tarea, estado, metadatos) {
			vmid, err := strconv.Atoi(tarea.InstanciaID)
			if err != nil {
				return fmt.Errorf("VMID inválido en tarea de destrucción %s: %w", tarea.ID, err)
			}
			// Serializa las escrituras sobre permisos_instancia durante la ventana
			// corta SELECT+DELETE, de modo que la lista auditada coincida con todas
			// las filas eliminadas por este cierre.
			if err := tx.Exec("LOCK TABLE permisos_instancia IN SHARE ROW EXCLUSIVE MODE").Error; err != nil {
				return fmt.Errorf("error al bloquear permisos para purgar el vmid %d: %w", vmid, err)
			}
			if err := tx.Model(&domain.PermisoInstancia{}).
				Where("vmid_proxmox = ?", vmid).
				Pluck("usuario_id", &usuariosAfectados).Error; err != nil {
				return fmt.Errorf("error al obtener usuarios afectados para el vmid %d: %w", vmid, err)
			}
			if err := tx.Where("vmid_proxmox = ?", vmid).
				Delete(&domain.PermisoInstancia{}).Error; err != nil {
				return fmt.Errorf("error al purgar permisos del vmid %d: %w", vmid, err)
			}
			usuariosTexto := make([]string, len(usuariosAfectados))
			for i, usuarioID := range usuariosAfectados {
				usuariosTexto[i] = usuarioID.String()
			}
			if err := NewAuditRepository(tx).Registrar(ctx, ports.RegistrarAuditoriaInput{
				UsuarioID:   tarea.UsuarioID,
				Accion:      ports.AccionInstancePermissionsPurged,
				InstanciaID: tarea.InstanciaID,
				RecursoTipo: recursoTipoTarea(tarea),
				Resultado:   ports.ResultadoSuccess,
				Detalles: map[string]any{
					"vmid": vmid, "usuariosAfectados": usuariosTexto,
				},
			}); err != nil {
				return fmt.Errorf("error al auditar la purga del vmid %d: %w", vmid, err)
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
	if err != nil {
		return nil, err
	}
	return usuariosAfectados, nil
}

func debeAsignarPermisoCreacion(tarea domain.TareaAsincrona, estado string, metadatos map[string]any) bool {
	_, esCreacion := ports.TipoRecursoCreacion(tarea.Accion)
	exitstatus, _ := metadatos["exitstatus"].(string)
	exitoProxmox := exitstatus == "OK"
	return esCreacion && estado == ports.TareaCompleted && exitoProxmox
}

func debePurgarPermisosDestruccion(tarea domain.TareaAsincrona, estado string, metadatos map[string]any) bool {
	accion := strings.ToLower(strings.TrimSpace(tarea.Accion))
	esDestruccion := accion == "delete" || accion == "qmdestroy" || accion == "vzdestroy"
	exitstatus, _ := metadatos["exitstatus"].(string)
	return esDestruccion && estado == ports.TareaCompleted && exitstatus == "OK"
}

func recursoTipoTarea(tarea domain.TareaAsincrona) string {
	tipo := ports.RecursoVM
	if strings.EqualFold(strings.TrimSpace(tarea.Accion), "vzdestroy") {
		return ports.RecursoLXC
	}
	if tarea.Metadatos == nil {
		return tipo
	}
	var metadatos map[string]any
	if json.Unmarshal([]byte(*tarea.Metadatos), &metadatos) != nil {
		return tipo
	}
	recurso, _ := metadatos["resource_type"].(string)
	if strings.EqualFold(recurso, ports.TipoInstanciaLXC) || strings.EqualFold(recurso, ports.RecursoLXC) {
		return ports.RecursoLXC
	}
	return tipo
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

// HuboTareaDesde indica si la instancia tiene una tarea en curso o creada desde
// el instante dado, en cualquier estado.
func (r *TareaRepository) HuboTareaDesde(ctx context.Context, vmid int, desde time.Time) (bool, error) {
	var cantidad int64
	if err := r.db.WithContext(ctx).Model(&domain.TareaAsincrona{}).
		Where("instancia_id = ? AND (estado = ? OR fecha_creacion >= ?)", strconv.Itoa(vmid), ports.TareaRunning, desde).
		Count(&cantidad).Error; err != nil {
		return false, fmt.Errorf("error al buscar tareas recientes: %w", err)
	}
	return cantidad > 0, nil
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
