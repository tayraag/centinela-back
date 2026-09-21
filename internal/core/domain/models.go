package domain

import (
	"github.com/google/uuid"
	"time"
)

// ==========================================
// 1. ORGANIZACIÓN
// ==========================================

type Organizacion struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:uuid_generate_v7()" json:"id"`
	NombreOrg string    `gorm:"type:varchar(255);not null" json:"nombreOrg"`
	CreadoPor uuid.UUID `gorm:"type:uuid;not null" json:"creadoPor"`

	Usuarios []Usuario `gorm:"foreignKey:OrganizacionID" json:"-"`
}

// ==========================================
// 2. USUARIO
// ==========================================

type Usuario struct {
	ID             uuid.UUID `gorm:"type:uuid;primaryKey;default:uuid_generate_v7()" json:"id"`
	OrganizacionID uuid.UUID `gorm:"type:uuid;not null" json:"organizacionId"`

	NombreCompleto string `gorm:"type:varchar(255);not null" json:"nombreCompleto"`
	NombreUsuario  string `gorm:"type:varchar(100);uniqueIndex;not null" json:"nombreUsuario"`
	EmailUsuario   string `gorm:"type:varchar(255);uniqueIndex;not null" json:"emailUsuario"`
	ContrasenaHash string `gorm:"type:varchar(255);not null" json:"-"` // Oculto en JSON

	Rol              string `gorm:"type:varchar(50);not null" json:"rol"` // ADMIN u OPERATOR
	Activo           bool   `gorm:"default:true" json:"activo"`
	CambioContrasena bool   `gorm:"default:true" json:"cambioContrasena"`

	// Campos 2FA (RF-01)
	TotpVinculado      bool   `gorm:"default:false" json:"totpVinculado"`
	SecretoTotpCifrado string `gorm:"type:varchar(255)" json:"-"`          // Oculto en JSON
	UltimoTotpPeriodo  *int64 `gorm:"type:bigint" json:"-"`                // Anti-replay: período TOTP (unix/30) del último código usado

	FechaUltimoAcceso *time.Time `json:"fechaUltimoAcceso"` // Puntero porque puede ser null inicialmente
	FechaCreacion     time.Time  `gorm:"default:now()" json:"fechaCreacion"`

	// Relaciones Has-Many (Para Foreign Keys)
	SesionesActivas   []SesionActiva     `gorm:"foreignKey:UsuarioID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	PermisosInstancia []PermisoInstancia `gorm:"foreignKey:UsuarioID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	Auditorias        []Auditoria        `gorm:"foreignKey:UsuarioID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL;" json:"-"`
	TareasAsincronas  []TareaAsincrona   `gorm:"foreignKey:UsuarioID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	Notificaciones    []Notificacion     `gorm:"foreignKey:UsuarioID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
}

// ==========================================
// 3. SESIÓN ACTIVA
// ==========================================

type SesionActiva struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:uuid_generate_v7()" json:"id"`
	UsuarioID uuid.UUID `gorm:"type:uuid;not null;index" json:"usuarioId"`

	JtiToken       string `gorm:"type:varchar(255);uniqueIndex;not null" json:"jtiToken"`
	Activa         bool   `gorm:"default:true" json:"activa"`
	CodigoTemporal string `gorm:"type:varchar(10)" json:"codigoTemporal"`
	Estado2fa      bool   `gorm:"default:false" json:"estado2fa"` // Control intermedio del login

	FechaExpiracion time.Time `gorm:"not null" json:"fechaExpiracion"`
	FechaCreacion   time.Time `gorm:"default:now()" json:"fechaCreacion"`
}

// ==========================================
// 4. PERMISO INSTANCIA (Matriz de Acceso)
// ==========================================

type PermisoInstancia struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey;default:uuid_generate_v7()" json:"id"`
	UsuarioID   uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_permisos_usuario_vmid" json:"usuarioId"`
	VmidProxmox int       `gorm:"not null;uniqueIndex:idx_permisos_usuario_vmid" json:"vmidProxmox"`
}

// ==========================================
// 5. AUDITORÍA (Append-Only Log)
// ==========================================

type Auditoria struct {
	ID        uuid.UUID  `gorm:"type:uuid;primaryKey;default:uuid_generate_v7()" json:"id"`
	UsuarioID *uuid.UUID `gorm:"type:uuid;index:idx_auditoria_usuario_fecha" json:"usuarioId"` // Puntero: SET NULL si se elimina el usuario

	Accion          string `gorm:"type:varchar(100);not null;index:idx_auditoria_accion" json:"accion"`
	InstanciaID     string `gorm:"type:varchar(100);index:idx_auditoria_instancia" json:"instanciaId"`
	InstanciaNombre string `gorm:"type:varchar(255)" json:"instanciaNombre"`
	Resultado       string `gorm:"type:varchar(50);not null;index:idx_auditoria_resultado" json:"resultado"` // EXITO o FALLA

	Detalles  *string   `gorm:"type:jsonb" json:"detalles"`                                     // JSON estructurado con metadata extra (puntero para permitir NULL en PostgreSQL)
	FechaHora time.Time `gorm:"default:now();index:idx_auditoria_usuario_fecha" json:"fechaHora"` // Índice compuesto con usuario_id
}

// ==========================================
// 6. TAREA ASÍNCRONA (Gestión de UPIDs)
// ==========================================

type TareaAsincrona struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:uuid_generate_v7()" json:"id"` // ID limpio para el front
	UsuarioID uuid.UUID `gorm:"type:uuid;not null" json:"usuarioId"`

	UpidProxmox string `gorm:"type:varchar(255);not null;uniqueIndex" json:"-"` // Crudo oculto al front
	InstanciaID string `gorm:"type:varchar(100)" json:"instanciaId"`
	Accion      string `gorm:"type:varchar(100)" json:"accion"`
	Estado      string `gorm:"type:varchar(50);not null" json:"estado"` // RUNNING, COMPLETED, FAILED

	FechaCreacion time.Time `gorm:"default:now()" json:"fechaCreacion"`
}

// ==========================================
// 7. NOTIFICACIÓN
// ==========================================

type Notificacion struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:uuid_generate_v7()" json:"id"`
	UsuarioID uuid.UUID `gorm:"type:uuid;not null;index" json:"usuarioId"`

	InstanciaID string `gorm:"type:varchar(100)" json:"instanciaId"`
	TipoEvento  string `gorm:"type:varchar(100)" json:"tipoEvento"`
	Mensaje     string `gorm:"type:text;not null" json:"mensaje"`
	Leida       bool   `gorm:"default:false" json:"leida"`

	FechaCreacion time.Time `gorm:"default:now()" json:"fechaCreacion"`
}

// Configuraciones explícitas de nombres de tablas para GORM
func (Organizacion) TableName() string     { return "organizaciones" }
func (Usuario) TableName() string          { return "usuarios" }
func (SesionActiva) TableName() string     { return "sesiones_activas" }
func (PermisoInstancia) TableName() string { return "permisos_instancia" }
func (Auditoria) TableName() string        { return "auditoria" }
func (TareaAsincrona) TableName() string   { return "tareas_asincronas" }
func (Notificacion) TableName() string     { return "notificaciones" }
