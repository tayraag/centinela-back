package ports

import (
	"context"
	"strings"

	"el-centinela/internal/core/domain"

	"github.com/google/uuid"
)

// Operaciones de aprovisionamiento que Proxmox incluye en el UPID. Se
// mantienen separadas de los códigos de auditoría (CREAR_VM/CREAR_LXC), pero
// ambas formas son aceptadas por TipoRecursoCreacion para que las tareas
// sobrevivan a reinicios aunque hayan sido registradas por versiones distintas.
const (
	AccionProxmoxCrearVM  = "qmcreate"
	AccionProxmoxCrearLXC = "vzcreate"
)

// TipoRecursoCreacion reconoce una acción de aprovisionamiento y devuelve el
// tipo de recurso del contrato de eventos. Acepta tanto el código de dominio
// como el nombre de la operación que viene dentro del UPID de Proxmox.
func TipoRecursoCreacion(accion string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(accion)) {
	case strings.ToLower(AccionCrearVM), AccionProxmoxCrearVM:
		return RecursoVM, true
	case strings.ToLower(AccionCrearLXC), AccionProxmoxCrearLXC:
		return RecursoLXC, true
	default:
		return "", false
	}
}

// TareaRepository es el puerto de persistencia de tareas y su auditoría asociada.
// El servicio construye el registro de auditoría; el adaptador garantiza que
// ambas escrituras sean atómicas sin decidir sus códigos ni resultados.
type TareaRepository interface {
	// Crear inserta atómicamente la tarea RUNNING y su auditoría PENDIENTE.
	Crear(ctx context.Context, tarea *domain.TareaAsincrona, auditoria RegistrarAuditoriaInput) error

	// ActualizarEstado confirma atómicamente el estado final (COMPLETED | FAILED),
	// los metadatos técnicos y la auditoría definitiva. Repetir el cierre de una
	// tarea terminada no genera nuevos registros. Un error deja RUNNING para reintento.
	ActualizarEstado(ctx context.Context, id uuid.UUID, estado string, metadatos map[string]any, auditoria RegistrarAuditoriaInput) error

	// BuscarTareasActivasPorVmids devuelve un mapa vmid → tarea en curso
	// (tareaId, acción en mayúsculas y estado RUNNING) de cualquiera de los
	// vmids dados. Si un vmid no tiene tarea activa, no aparece en el mapa.
	BuscarTareasActivasPorVmids(ctx context.Context, vmids []int) (map[int]ActiveTaskDTO, error)

	// ListarEnCurso devuelve todas las tareas en estado RUNNING, de la más vieja a
	// la más nueva. Lo usa el reconciliador del seguimiento de tareas.
	ListarEnCurso(ctx context.Context) ([]domain.TareaAsincrona, error)
}
