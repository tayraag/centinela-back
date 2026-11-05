package ports

import (
	"context"
	"strings"
	"time"

	"el-centinela/internal/core/domain"

	"github.com/google/uuid"
)

// Operaciones de aprovisionamiento que Proxmox incluye en el UPID, y los
// códigos de dominio con que versiones anteriores guardaban la tarea
// (CREAR_VM/CREAR_LXC). TipoRecursoCreacion acepta ambas formas para que las
// tareas sobrevivan a reinicios aunque hayan sido registradas por versiones
// distintas. No son acciones de auditoría: la creación se audita como
// CREATE_INSTANCE (AccionCrearInstancia).
const (
	AccionProxmoxCrearVM  = "qmcreate"
	AccionProxmoxCrearLXC = "vzcreate"
	AccionTareaCrearVM    = "CREAR_VM"
	AccionTareaCrearLXC   = "CREAR_LXC"
)

// TipoRecursoCreacion reconoce una acción de aprovisionamiento y devuelve el
// tipo de recurso del contrato de eventos. Acepta tanto el código de dominio
// como el nombre de la operación que viene dentro del UPID de Proxmox.
func TipoRecursoCreacion(accion string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(accion)) {
	case strings.ToLower(AccionTareaCrearVM), AccionProxmoxCrearVM:
		return RecursoVM, true
	case strings.ToLower(AccionTareaCrearLXC), AccionProxmoxCrearLXC:
		return RecursoLXC, true
	default:
		return "", false
	}
}

// AccionPublicaTarea normaliza los nombres internos de Proxmox al vocabulario
// del contrato HTTP/SSE. qmcreate y vzcreate son una sola acción CREATE.
func AccionPublicaTarea(accion string) string {
	if _, creacion := TipoRecursoCreacion(accion); creacion {
		return "CREATE"
	}
	return strings.ToUpper(strings.TrimSpace(accion))
}

// TareaRepository es el puerto de persistencia de tareas y su auditoría asociada.
// El servicio construye el registro de auditoría; el adaptador garantiza que
// ambas escrituras sean atómicas sin decidir sus códigos ni resultados.
type TareaRepository interface {
	// Crear inserta atómicamente la tarea RUNNING y su auditoría PENDIENTE.
	Crear(ctx context.Context, tarea *domain.TareaAsincrona, auditoria RegistrarAuditoriaInput) error

	// ActualizarEstado confirma atómicamente el estado final (COMPLETED | FAILED),
	// los metadatos técnicos y la auditoría definitiva. En una creación que
	// Proxmox completó con exitstatus OK también asigna FULL_ACCESS al usuario
	// creador sobre el VMID reservado.
	// Repetir el cierre de una tarea terminada no genera nuevos registros. Un
	// error deja RUNNING para reintento.
	// Si confirma una destrucción con exitstatus OK, la misma transacción
	// recolecta y elimina los permisos del VMID y registra
	// INSTANCE_PERMISSIONS_PURGED. Devuelve los usuarios afectados para que el
	// worker pueda dirigirles el último TASK_FINISHED después del commit.
	ActualizarEstado(ctx context.Context, id uuid.UUID, estado string, metadatos map[string]any, auditoria RegistrarAuditoriaInput) ([]uuid.UUID, error)

	// BuscarTareasActivasPorVmids devuelve un mapa vmid → tarea en curso
	// (tareaId, acción en mayúsculas y estado RUNNING) de cualquiera de los
	// vmids dados. Si un vmid no tiene tarea activa, no aparece en el mapa.
	BuscarTareasActivasPorVmids(ctx context.Context, vmids []int) (map[int]ActiveTaskDTO, error)

	// HuboTareaDesde indica si la instancia tiene una tarea de Centinela en
	// curso o creada desde el instante dado (cualquier acción y estado). Explica
	// un cambio de estado que Centinela ordenó (start, stop, rollback...).
	HuboTareaDesde(ctx context.Context, vmid int, desde time.Time) (bool, error)

	// FueCreadaPorCentinela indica si el VMID tiene una creación propia
	// confirmada por Proxmox. Es la guarda de pertenencia del borrado: una
	// instancia que la API no creó no se destruye desde la API.
	FueCreadaPorCentinela(ctx context.Context, vmid int) (bool, error)

	// ListarEnCurso devuelve todas las tareas en estado RUNNING, de la más vieja a
	// la más nueva. Lo usa el reconciliador del seguimiento de tareas.
	ListarEnCurso(ctx context.Context) ([]domain.TareaAsincrona, error)
}
