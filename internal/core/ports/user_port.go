package ports

import (
	"context"
	"time"

	"el-centinela/internal/core/domain"

	"github.com/google/uuid"
)

// ==========================================
// DTOs de filtros
// ==========================================

// FiltrosUsuario define los parámetros opcionales para filtrar la lista de usuarios.
type FiltrosUsuario struct {
	Rol    string // "ADMIN" | "OPERATOR" | "" (todos)
	Activo *bool  // nil = todos, true = solo activos, false = solo inactivos
	Buscar string // búsqueda textual en nombre completo o email
}

// FiltrosActividad define los parámetros para filtrar la actividad de un usuario.
type FiltrosActividad struct {
	Accion string // ej. "START", "STOP", ""
	Desde  *time.Time
	Hasta  *time.Time
}

// ==========================================
// DTOs de salida
// ==========================================

// UsuarioResumenDTO es la proyección pública del usuario para el listado.
// Nunca expone ContrasenaHash ni SecretoTotpCifrado.
type UsuarioResumenDTO struct {
	ID                uuid.UUID  `json:"id"`
	NombreCompleto    string     `json:"nombreCompleto"`
	NombreUsuario     string     `json:"nombreUsuario"`
	EmailUsuario      string     `json:"emailUsuario"`
	Rol               string     `json:"rol"`
	Activo            bool       `json:"activo"`
	TotpVinculado     bool       `json:"totpVinculado"`
	FechaUltimoAcceso *time.Time `json:"fechaUltimoAcceso"`
	FechaCreacion     time.Time  `json:"fechaCreacion"`
	EsUsuarioActual   bool       `json:"esUsuarioActual"` // true si coincide con el usuario autenticado
}

// ResumenUsuariosDTO contiene las estadísticas agregadas del panel de usuarios.
type ResumenUsuariosDTO struct {
	Total     int64 `json:"total"`
	Admins    int64 `json:"admins"`
	Operators int64 `json:"operators"`
}

// ListaUsuariosResult agrupa el resumen y el listado en una sola respuesta.
type ListaUsuariosResult struct {
	Summary ResumenUsuariosDTO  `json:"summary"`
	Users   []UsuarioResumenDTO `json:"users"`
}

// UsuarioDetalleDTO es la proyección completa del usuario incluyendo instancias permitidas.
type UsuarioDetalleDTO struct {
	ID                        uuid.UUID  `json:"id"`
	NombreCompleto            string     `json:"nombreCompleto"`
	NombreUsuario             string     `json:"nombreUsuario"`
	EmailUsuario              string     `json:"emailUsuario"`
	OrganizacionID            uuid.UUID  `json:"organizacionId"`
	Rol                       string     `json:"rol"`
	Activo                    bool       `json:"activo"`
	TotpVinculado             bool       `json:"totpVinculado"`
	CambioContrasenaRequerido bool       `json:"cambioContrasenaRequerido"`
	FechaCreacion             time.Time  `json:"fechaCreacion"`
	FechaUltimoAcceso         *time.Time `json:"fechaUltimoAcceso"`
	InstanciasPermitidas      []int      `json:"instanciasPermitidas"` // VMIDs de Proxmox
}

// CrearUsuarioResult es la respuesta al crear un usuario exitosamente.
type CrearUsuarioResult struct {
	ID             uuid.UUID `json:"id"`
	Rol            string    `json:"rol"`
	Activo         bool      `json:"activo"`
	ContrasenaTemp string    `json:"contrasenaTemp"` // Solo devuelto aquí (Plan A sin SMTP)
}

// ActividadDTO proyecta un registro de auditoría para la vista de actividad de un usuario.
type ActividadDTO struct {
	ID              uuid.UUID `json:"id"`
	FechaHora       time.Time `json:"fechaHora"`
	Accion          string    `json:"accion"`
	InstanciaID     string    `json:"instanciaId"`
	InstanciaNombre string    `json:"instanciaNombre"`
	Resultado       string    `json:"resultado"`
	Detalles        string    `json:"detalles,omitempty"`
}

// RolDTO representa un rol disponible en el sistema.
type RolDTO struct {
	Valor       string `json:"valor"`
	Descripcion string `json:"descripcion"`
}

// ==========================================
// DTOs de entrada
// ==========================================

// CrearUsuarioInput contiene los datos enviados por el admin para crear un usuario.
type CrearUsuarioInput struct {
	NombreCompleto string `json:"nombreCompleto" binding:"required,min=2,max=255"`
	NombreUsuario  string `json:"nombreUsuario"  binding:"required,min=3,max=100"`
	EmailUsuario   string `json:"emailUsuario"   binding:"required,email"`
	Rol            string `json:"rol"            binding:"required,oneof=ADMIN OPERATOR"`
}

// ActualizarUsuarioInput contiene los campos opcionales que el admin puede modificar.
// Solo los campos con valor no-zero serán actualizados (PATCH semántico).
type ActualizarUsuarioInput struct {
	NombreCompleto string `json:"nombreCompleto"`
	EmailUsuario   string `json:"emailUsuario"`
	Rol            string `json:"rol"    binding:"omitempty,oneof=ADMIN OPERATOR"`
	Activo         *bool  `json:"activo"` // puntero para distinguir false de "no enviado"
}

// ActualizarPerfilInput contiene los campos que el propio usuario puede modificar de sí mismo.
type ActualizarPerfilInput struct {
	NombreCompleto string `json:"nombreCompleto" binding:"omitempty,min=2,max=255"`
	EmailUsuario   string `json:"emailUsuario"   binding:"omitempty,email"`
}

// CambiarContrasenaInput es el body para que el usuario cambie su propia contraseña.
type CambiarContrasenaInput struct {
	ContrasenaActual string `json:"contrasenaActual" binding:"required"`
	ContrasenaNueva  string `json:"contrasenaNueva"  binding:"required,min=8"`
}

// ==========================================
// Puerto: Repositorio de Usuarios
// ==========================================

// UserRepository define el contrato de persistencia para el dominio de usuarios.
type UserRepository interface {
	// ListarUsuarios devuelve usuarios de una organización con filtros opcionales.
	ListarUsuarios(ctx context.Context, orgID uuid.UUID, filtros FiltrosUsuario) (*ListaUsuariosResult, error)

	// BuscarUsuarioPorIDEnOrg busca un usuario verificando que pertenezca a la organización.
	BuscarUsuarioPorIDEnOrg(ctx context.Context, id, orgID uuid.UUID) (*domain.Usuario, error)

	// ExisteEmailEnOrg verifica si el email ya está registrado en la organización.
	ExisteEmailEnOrg(ctx context.Context, email string, orgID uuid.UUID, excluirID *uuid.UUID) (bool, error)

	// ExisteUsernameEnOrg verifica si el nombre de usuario ya está registrado en la organización.
	ExisteUsernameEnOrg(ctx context.Context, username string, orgID uuid.UUID, excluirID *uuid.UUID) (bool, error)

	// CrearUsuario persiste un nuevo usuario en la base de datos.
	CrearUsuario(ctx context.Context, u *domain.Usuario) error

	// ActualizarUsuario aplica cambios parciales a un usuario existente.
	ActualizarUsuario(ctx context.Context, id uuid.UUID, cambios map[string]any) error

	// ListarPermisosDeUsuario devuelve los VMIDs a los que tiene acceso un usuario.
	ListarPermisosDeUsuario(ctx context.Context, usuarioID uuid.UUID) ([]int, error)

	// ReemplazarPermisos reemplaza atómicamente todos los permisos de un usuario.
	ReemplazarPermisos(ctx context.Context, usuarioID uuid.UUID, vmids []int) error

	// ListarActividadDeUsuario devuelve registros de auditoría filtrados por usuario.
	ListarActividadDeUsuario(ctx context.Context, usuarioID uuid.UUID, filtros FiltrosActividad) ([]domain.Auditoria, error)
}

// ==========================================
// Puerto: Servicio de Usuarios
// ==========================================

// UserService define la lógica de negocio para la gestión de usuarios.
type UserService interface {
	// --- Gestión de usuarios (solo ADMIN) ---

	// ListarUsuarios devuelve la lista de usuarios de la organización con estadísticas.
	ListarUsuarios(ctx context.Context, orgID uuid.UUID, solicitanteID uuid.UUID, filtros FiltrosUsuario) (*ListaUsuariosResult, error)

	// ObtenerUsuario devuelve el perfil completo de un usuario con sus permisos de instancia.
	ObtenerUsuario(ctx context.Context, id, orgID uuid.UUID) (*UsuarioDetalleDTO, error)

	// CrearUsuario crea un nuevo usuario con contraseña temporal generada automáticamente.
	CrearUsuario(ctx context.Context, orgID uuid.UUID, actorID uuid.UUID, input CrearUsuarioInput) (*CrearUsuarioResult, error)

	// ActualizarUsuario actualiza parcialmente los datos de un usuario.
	ActualizarUsuario(ctx context.Context, id, orgID uuid.UUID, actorID uuid.UUID, input ActualizarUsuarioInput) (*UsuarioResumenDTO, error)

	// EliminarUsuario realiza un soft-delete del usuario y cierra todas sus sesiones.
	EliminarUsuario(ctx context.Context, id, orgID uuid.UUID, actorID uuid.UUID) error

	// AsignarPermisos reemplaza todos los permisos de instancia de un usuario operador.
	AsignarPermisos(ctx context.Context, usuarioID, orgID uuid.UUID, actorID uuid.UUID, vmids []int) error

	// ResetearContrasena genera una nueva contraseña temporal para el usuario.
	ResetearContrasena(ctx context.Context, usuarioID, orgID uuid.UUID, actorID uuid.UUID) (contrasenaTemp string, err error)

	// ResetearTotp invalida el 2FA del usuario, forzando revinculación en el próximo login.
	ResetearTotp(ctx context.Context, usuarioID, orgID uuid.UUID, actorID uuid.UUID) error

	// ListarActividad devuelve el historial de acciones de un usuario.
	ListarActividad(ctx context.Context, usuarioID, orgID uuid.UUID, filtros FiltrosActividad) ([]ActividadDTO, error)

	// --- Perfil propio (cualquier usuario autenticado) ---

	// ObtenerPerfil devuelve el perfil del usuario autenticado.
	ObtenerPerfil(ctx context.Context, usuarioID uuid.UUID) (*UsuarioDetalleDTO, error)

	// ActualizarPerfil permite al usuario modificar su propio nombre y email.
	ActualizarPerfil(ctx context.Context, usuarioID uuid.UUID, input ActualizarPerfilInput) (*UsuarioResumenDTO, error)

	// CambiarContrasena valida la contraseña actual y aplica la nueva.
	CambiarContrasena(ctx context.Context, usuarioID uuid.UUID, input CambiarContrasenaInput) error
}
