package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ==========================================
// DTOs de Auditoría
// ==========================================

// FiltrosAuditoria define los parámetros opcionales para filtrar el log de auditoría.
type FiltrosAuditoria struct {
	UsuarioID   *uuid.UUID // Filtrar por usuario específico
	Accion      string     // Ej: "LOGIN", "CREATE_USER", "START_VM"
	InstanciaID string     // ID de la instancia Proxmox afectada
	Resultado   string     // "EXITO" | "FALLA" | "" (todos)
	Desde       *time.Time // Rango temporal: desde (inclusive)
	Hasta       *time.Time // Rango temporal: hasta (inclusive)
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
	ID              uuid.UUID  `json:"id"`
	UsuarioID       *uuid.UUID `json:"usuarioId"`             // Puntero: puede ser null si el usuario fue eliminado
	NombreUsuario   string     `json:"nombreUsuario"`         // Nombre completo (JOIN con usuarios, o "[usuario eliminado]")
	FechaHora       time.Time  `json:"fechaHora"`
	Accion          string     `json:"accion"`
	InstanciaID     string     `json:"instanciaId,omitempty"`
	InstanciaNombre string     `json:"instanciaNombre,omitempty"`
	Resultado       string     `json:"resultado"`
	Detalles        string     `json:"detalles,omitempty"`
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
	Accion          string         // Código de la acción: "LOGIN", "CREATE_USER", etc.
	InstanciaID     string         // ID de la instancia afectada (vacío si no aplica)
	InstanciaNombre string         // Nombre de la instancia afectada (vacío si no aplica)
	Resultado       string         // "EXITO" | "FALLA"
	Detalles        map[string]any // Metadata adicional (se serializa a JSON)
}

// ==========================================
// Constantes de acciones de auditoría
// ==========================================

// Acciones de autenticación
const (
	AccionLogin          = "LOGIN"
	AccionLoginFalla     = "LOGIN_FALLA"
	AccionVerificar2FA   = "VERIFICAR_2FA"
	Accion2FAFalla       = "2FA_FALLA"
	AccionLogout         = "LOGOUT"
)

// Acciones de gestión de usuarios
const (
	AccionCrearUsuario       = "CREAR_USUARIO"
	AccionActualizarUsuario  = "ACTUALIZAR_USUARIO"
	AccionEliminarUsuario    = "ELIMINAR_USUARIO"
	AccionAsignarPermisos    = "ASIGNAR_PERMISOS"
	AccionResetearContrasena = "RESETEAR_CONTRASENA"
	AccionResetearTotp       = "RESETEAR_TOTP"
	AccionCambiarContrasena  = "CAMBIAR_CONTRASENA"
	AccionActualizarPerfil   = "ACTUALIZAR_PERFIL"
)

// Acciones sobre instancias (preparado para RF futuro de VMs)
const (
	AccionIniciarVM   = "INICIAR_VM"
	AccionDetenerVM   = "DETENER_VM"
	AccionReiniciarVM = "REINICIAR_VM"
	AccionSuspenderVM = "SUSPENDER_VM"
	AccionResumeVM    = "REANUDAR_VM"
)

// Resultados de auditoría
const (
	ResultadoExito = "EXITO"
	ResultadoFalla = "FALLA"
)

// ==========================================
// Puerto: Repositorio de Auditoría
// ==========================================

// AuditRepository define el contrato de persistencia para auditoría (append-only).
// No expone métodos de actualización ni borrado.
type AuditRepository interface {
	// Registrar inserta un nuevo registro de auditoría de forma inmutable.
	Registrar(ctx context.Context, input RegistrarAuditoriaInput) error

	// Listar devuelve registros paginados con filtros y ordenamiento aplicados.
	// Filtra por orgID para aislar los datos entre organizaciones.
	Listar(ctx context.Context, orgID uuid.UUID, filtros FiltrosAuditoria, opciones OpcionesAuditoria) (*PaginaAuditoria, error)

	// ExportarRegistros devuelve todos los registros que cumplen los filtros (sin paginación).
	// Tiene un límite interno de 100.000 filas para proteger la memoria.
	ExportarRegistros(ctx context.Context, orgID uuid.UUID, filtros FiltrosAuditoria) ([]AuditoriaDTO, error)
}

// ==========================================
// Puerto: Servicio de Auditoría
// ==========================================

// AuditService define la lógica de negocio para el registro y consulta de auditoría.
type AuditService interface {
	// Registrar inserta un registro de auditoría. Los errores se registran en log
	// pero no se propagan (fallo silencioso) para no interrumpir la operación principal.
	Registrar(ctx context.Context, input RegistrarAuditoriaInput)

	// ListarAuditoria devuelve el log de auditoría paginado con filtros.
	ListarAuditoria(ctx context.Context, orgID uuid.UUID, filtros FiltrosAuditoria, opciones OpcionesAuditoria) (*PaginaAuditoria, error)

	// ExportarCSV construye y devuelve el archivo CSV como bytes.
	ExportarCSV(ctx context.Context, orgID uuid.UUID, filtros FiltrosAuditoria) ([]byte, error)

	// ExportarJSON construye y devuelve el archivo JSON como bytes.
	ExportarJSON(ctx context.Context, orgID uuid.UUID, filtros FiltrosAuditoria) ([]byte, error)
}
