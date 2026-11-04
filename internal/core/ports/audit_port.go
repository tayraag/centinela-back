package ports

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"el-centinela/internal/core/domain"

	"github.com/google/uuid"
)

// ==========================================
// DTOs de Auditoría
// ==========================================

// FiltrosAuditoria define los parámetros opcionales para filtrar el log de auditoría.
type FiltrosAuditoria struct {
	RecursoTipo string
	RecursoID   string
	UsuarioID   *uuid.UUID // Filtrar por usuario específico
	Accion      string     // Ej: "LOGIN", "CREAR_USUARIO", "INICIAR_VM"
	// ExcluirAcciones deja afuera del listado las acciones indicadas. Se usa para
	// que el feed del Dashboard no traiga el ruido del canal de eventos
	// (EVENTOS_CONEXION / EVENTOS_CIERRE) sin quitarlo de la vista completa.
	ExcluirAcciones []string
	InstanciaID     string     // ID de la instancia Proxmox afectada
	Resultado       string     // "EXITO" | "FALLA" | "PENDIENTE" | "" (todos)
	Desde           *time.Time // Rango temporal: desde (inclusive)
	Hasta           *time.Time // Rango temporal: hasta (inclusive)
}

// OpcionesAuditoria define paginación y ordenamiento para el listado.
type OpcionesAuditoria struct {
	Pagina     int    // 1-based (default 1)
	TamanoPag  int    // Registros por página (default 50, máx 200)
	OrdenarPor string // "fechaHora" (default) | "accion" | "resultado"
	Direccion  string // "desc" (default) | "asc"
}

// AuditoriaDTO es la proyección pública de un registro de auditoría.
type AuditoriaDTO struct {
	ID              uuid.UUID       `json:"id"`
	UsuarioID       *uuid.UUID      `json:"usuarioId"`     // Puntero: puede ser null si el usuario fue eliminado
	NombreUsuario   string          `json:"nombreUsuario"` // Nombre completo (JOIN con usuarios, o "[usuario eliminado]")
	FechaHora       time.Time       `json:"fechaHora"`
	Accion          string          `json:"accion" enums:"LOGIN,LOGIN_FALLA,VERIFICAR_2FA,2FA_FALLA,LOGOUT,CREAR_USUARIO,ACTUALIZAR_USUARIO,ELIMINAR_USUARIO,ASIGNAR_PERMISOS,RESETEAR_CONTRASENA,RESETEAR_TOTP,PASSWORD_CHANGED,UPDATE_PROFILE_EMAIL_REQUESTED,UPDATE_PROFILE_EMAIL_CONFIRMED,CREATE_INSTANCE,INICIAR_VM,DETENER_VM,APAGAR_VM,REINICIAR_VM,REANUDAR_VM,ELIMINAR_VM,CONFIG_UPDATED,SNAPSHOT_CREATE,SNAPSHOT_ROLLBACK,SNAPSHOT_DELETE,INSTANCE_PERMISSIONS_PURGED,SYSTEM_INSTANCE_STOPPED_EXTERNALLY,ALERT_TRIGGERED,ALERT_RESOLVED,EVENTOS_CONEXION,EVENTOS_CIERRE"`
	InstanciaID     string          `json:"instanciaId,omitempty"`
	InstanciaNombre string          `json:"instanciaNombre,omitempty"`
	RecursoTipo     string          `json:"resourceType,omitempty"`
	RecursoID       string          `json:"resourceId,omitempty"`
	Resultado       string          `json:"resultado" enums:"EXITO,FALLA,PENDIENTE"`
	Detalles        json.RawMessage `json:"detalles,omitempty" swaggertype:"object"`
}

// PaginaAuditoria encapsula el resultado paginado del listado de auditoría.
type PaginaAuditoria struct {
	Total  int64          `json:"total"`  // Total de registros que coinciden con los filtros
	Pagina int            `json:"pagina"` // Página actual
	Items  []AuditoriaDTO `json:"items"`  // Registros de esta página
}

// RegistrarAuditoriaInput es lo que los servicios pasan para insertar un registro.
type RegistrarAuditoriaInput struct {
	UsuarioID       uuid.UUID      // ID del usuario que ejecutó la acción
	Accion          string         // Código de la acción: AccionesAuditoriaValidas()
	InstanciaID     string         // ID de la instancia afectada (vacío si no aplica)
	InstanciaNombre string         // Nombre de la instancia afectada (vacío si no aplica)
	RecursoTipo     string         // Tipo estable del recurso auditado: VM o LXC
	RecursoID       string         // Identificador estable del recurso auditado
	Upid            string         // UPID de Proxmox para correlacionar despacho y desenlace
	Resultado       string         // ResultadosAuditoriaValidos(): EXITO, FALLA o PENDIENTE
	Detalles        map[string]any // Metadata adicional (se serializa a JSON)
}

// ==========================================
// Vocabulario cerrado de auditoría (RF-08)
// ==========================================
//
// auditoria.accion y auditoria.resultado solo admiten los valores de
// AccionesAuditoriaValidas() y ResultadosAuditoriaValidos(). El repositorio
// rechaza cualquier otro (ErrAuditoriaFueraDeContrato), el filtro HTTP
// responde 400 y el Swagger publica las dos listas como enum. Para agregar una
// acción: declarar la constante acá, sumarla a accionesAuditoria y regenerar
// el Swagger (los enum de AuditoriaDTO y de audit_handler.go deben coincidir).

// Acciones de autenticación
const (
	AccionLogin        = "LOGIN"
	AccionLoginFalla   = "LOGIN_FALLA"
	AccionVerificar2FA = "VERIFICAR_2FA"
	Accion2FAFalla     = "2FA_FALLA"
	AccionLogout       = "LOGOUT"
)

// Acciones del canal de eventos en tiempo real (GET /api/events) y del sistema
const (
	AccionEventosConexion               = "EVENTOS_CONEXION" // se abrió un stream (detalles: sesion_id)
	AccionEventosCierre                 = "EVENTOS_CIERRE"   // el backend cortó un stream (detalles: sesion_id, motivo)
	AccionInstanciaDetenidaExternamente = "SYSTEM_INSTANCE_STOPPED_EXTERNALLY"
	AccionAlertaDisparada               = "ALERT_TRIGGERED" // el evaluador de umbrales emitió una alerta (sin usuario)
	AccionAlertaResuelta                = "ALERT_RESOLVED"  // una alerta volvió a la normalidad (sin usuario)
)

// Acciones de gestión de usuarios y del perfil propio
const (
	AccionCrearUsuario         = "CREAR_USUARIO"
	AccionActualizarUsuario    = "ACTUALIZAR_USUARIO"
	AccionEliminarUsuario      = "ELIMINAR_USUARIO"
	AccionAsignarPermisos      = "ASIGNAR_PERMISOS"
	AccionResetearContrasena   = "RESETEAR_CONTRASENA"
	AccionResetearTotp         = "RESETEAR_TOTP"
	AccionCambiarContrasena    = "PASSWORD_CHANGED"
	AccionSolicitarCambioEmail = "UPDATE_PROFILE_EMAIL_REQUESTED"
	AccionConfirmarCambioEmail = "UPDATE_PROFILE_EMAIL_CONFIRMED"
)

// Acciones de auditoría sobre instancias (códigos solicitados por RF-08).
const (
	AccionCrearInstancia = "CREATE_INSTANCE"
	AccionIniciarVM      = "INICIAR_VM"
	AccionDetenerVM      = "DETENER_VM"
	AccionReiniciarVM    = "REINICIAR_VM"
	AccionReanudarVM     = "REANUDAR_VM"
	AccionEliminarVM     = "ELIMINAR_VM"
	AccionApagarVM       = "APAGAR_VM"
	// AccionConfigurarInstancia: cambio de CPU o RAM (PUT /api/instances/:instanceRef/config).
	AccionConfigurarInstancia = "CONFIG_UPDATED"
	// AccionCrearSnapshot: creación de un snapshot (POST /api/instances/:instanceRef/snapshots).
	AccionCrearSnapshot    = "SNAPSHOT_CREATE"
	AccionRevertirSnapshot = "SNAPSHOT_ROLLBACK"
	AccionEliminarSnapshot = "SNAPSHOT_DELETE"
	// AccionInstancePermissionsPurged: depuración de permisos por reciclaje de VMID.
	AccionInstancePermissionsPurged = "INSTANCE_PERMISSIONS_PURGED"
)

// Resultados de auditoría. Las operaciones sincrónicas terminan en EXITO o
// FALLA; las tareas asíncronas de Proxmox registran PENDIENTE al despacharse y
// EXITO o FALLA en su desenlace (exitstatus OK, o error/timeout).
const (
	ResultadoExito     = "EXITO"
	ResultadoFalla     = "FALLA"
	ResultadoPendiente = "PENDIENTE"
)

// accionesAuditoria es el orden publicado en el Swagger y en
// docs/contrato-etapa1.md.
var accionesAuditoria = []string{
	AccionLogin, AccionLoginFalla, AccionVerificar2FA, Accion2FAFalla, AccionLogout,
	AccionCrearUsuario, AccionActualizarUsuario, AccionEliminarUsuario, AccionAsignarPermisos,
	AccionResetearContrasena, AccionResetearTotp, AccionCambiarContrasena,
	AccionSolicitarCambioEmail, AccionConfirmarCambioEmail,
	AccionCrearInstancia, AccionIniciarVM, AccionDetenerVM, AccionApagarVM, AccionReiniciarVM,
	AccionReanudarVM, AccionEliminarVM, AccionConfigurarInstancia,
	AccionCrearSnapshot, AccionRevertirSnapshot, AccionEliminarSnapshot,
	AccionInstancePermissionsPurged, AccionInstanciaDetenidaExternamente,
	AccionAlertaDisparada, AccionAlertaResuelta,
	AccionEventosConexion, AccionEventosCierre,
}

var resultadosAuditoria = []string{ResultadoExito, ResultadoFalla, ResultadoPendiente}

// AccionesAuditoriaValidas devuelve una copia del conjunto cerrado de acciones.
func AccionesAuditoriaValidas() []string { return append([]string(nil), accionesAuditoria...) }

// ResultadosAuditoriaValidos devuelve una copia del conjunto cerrado de resultados.
func ResultadosAuditoriaValidos() []string { return append([]string(nil), resultadosAuditoria...) }

// EsAccionAuditoriaValida indica si accion pertenece al vocabulario cerrado.
func EsAccionAuditoriaValida(accion string) bool { return contiene(accionesAuditoria, accion) }

// EsResultadoAuditoriaValido indica si resultado pertenece al vocabulario cerrado.
func EsResultadoAuditoriaValido(resultado string) bool {
	return contiene(resultadosAuditoria, resultado)
}

func contiene(lista []string, valor string) bool {
	for _, v := range lista {
		if v == valor {
			return true
		}
	}
	return false
}

// ErrAuditoriaFueraDeContrato: se intentó persistir una acción o un resultado
// que no pertenece al vocabulario cerrado. No se inserta la fila.
var ErrAuditoriaFueraDeContrato = errors.New("acción o resultado de auditoría fuera del contrato")

// ==========================================
// Puerto: Repositorio de Auditoría
// ==========================================

// CursorAuditoria mantiene una lectura incremental. Siempre debe cerrarse,
// incluso si el consumidor cancela o falla al escribir la descarga.
type CursorAuditoria interface {
	Next() bool
	Actual() (AuditoriaDTO, error)
	Err() error
	Close() error
}

// AuditStreaming separa la capacidad de streaming del contrato histórico que
// devuelve slices/bytes. Servicio y repositorio implementan esta capacidad.
type AuditStreaming interface {
	AbrirExportacion(context.Context, uuid.UUID, FiltrosAuditoria) (CursorAuditoria, error)
}

// AuditRepository define el contrato de persistencia para auditoría (append-only).
// No expone métodos de actualización ni borrado.
type AuditRepository interface {
	// Registrar inserta un nuevo registro de auditoría de forma inmutable.
	Registrar(ctx context.Context, input RegistrarAuditoriaInput) error

	// Listar devuelve registros paginados con filtros y ordenamiento aplicados.
	// Filtra por orgID para aislar los datos entre organizaciones.
	Listar(ctx context.Context, orgID uuid.UUID, filtros FiltrosAuditoria, opciones OpcionesAuditoria) (*PaginaAuditoria, error)

	// ExportarRegistros es el contrato histórico en memoria; el endpoint HTTP usa AuditStreaming.
	// Tiene un límite interno de 100.000 filas para proteger la memoria.
	ExportarRegistros(ctx context.Context, orgID uuid.UUID, filtros FiltrosAuditoria) ([]AuditoriaDTO, error)
}

// ==========================================
// Puerto: Servicio de Auditoría
// ==========================================

// AuditService define la lógica de negocio para el registro y consulta de auditoría.
type AuditService interface {
	// PrepararAuditoriaTarea aplica RF-08 sin persistir. El seguimiento entrega
	// el registro al puerto de tareas para guardarlo en la misma transacción.
	PrepararAuditoriaTarea(tarea *domain.TareaAsincrona, cierre map[string]any, ahora time.Time) RegistrarAuditoriaInput

	// Registrar inserta un registro de auditoría. Los errores se registran en log
	// pero no se propagan (fallo silencioso) para no interrumpir la operación principal.
	Registrar(ctx context.Context, input RegistrarAuditoriaInput)

	// RegistrarEstricto inserta un registro de auditoría y propaga cualquier error.
	// Se usa cuando confirmar la auditoría es parte del resultado de la operación.
	RegistrarEstricto(ctx context.Context, input RegistrarAuditoriaInput) error

	// ListarAuditoria devuelve el log de auditoría paginado con filtros.
	ListarAuditoria(ctx context.Context, orgID uuid.UUID, filtros FiltrosAuditoria, opciones OpcionesAuditoria) (*PaginaAuditoria, error)

	// ExportarCSV conserva el contrato histórico en memoria. HTTP usa AuditStreaming.
	ExportarCSV(ctx context.Context, orgID uuid.UUID, filtros FiltrosAuditoria) ([]byte, error)

	// ExportarJSON conserva el contrato histórico en memoria. HTTP usa AuditStreaming.
	ExportarJSON(ctx context.Context, orgID uuid.UUID, filtros FiltrosAuditoria) ([]byte, error)
}

// DetallesAuditoriaSeguros normaliza cualquier valor serializable a JSON antes
// de recorrerlo. Incluye structs, RawMessage, mapas tipados y arrays sin mutar
// los metadatos del llamador. Un valor no serializable impide la escritura.
func DetallesAuditoriaSeguros(detalles map[string]any) ([]byte, error) {
	raw, err := json.Marshal(detalles)
	if err != nil {
		return nil, err
	}
	var valor any
	if err := json.Unmarshal(raw, &valor); err != nil {
		return nil, err
	}
	var redactar func(any)
	redactar = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, sub := range x {
				clave := strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(k))
				sensible := false
				for _, patron := range []string{"password", "contras", "secret", "token", "authorization", "cookie", "apikey", "privatekey", "credential", "totp", "recoverycode", "codigorecuperacion"} {
					sensible = sensible || strings.Contains(clave, patron)
				}
				if sensible {
					x[k] = "[REDACTED]"
				} else {
					redactar(sub)
				}
			}
		case []any:
			for _, sub := range x {
				redactar(sub)
			}
		}
	}
	redactar(valor)
	return json.Marshal(valor)
}
