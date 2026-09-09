package domain

import (
	"time"
	"github.com/google/uuid"
)

// ==========================================
// 1. ORGANIZACIÓN
// ==========================================

type Organizacion struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:uuid_generate_v7()" json:"id"`
	NombreOrg string    `gorm:"type:varchar(255);not null" json:"nombreOrg"`
	CreadoPor uuid.UUID `gorm:"type:uuid;not null" json:"creadoPor"`
	
	Usuarios  []Usuario `gorm:"foreignKey:OrganizacionID" json:"-"`
}

// ==========================================
// 2. USUARIO
// ==========================================

type Usuario struct {
	ID                 uuid.UUID `gorm:"type:uuid;primaryKey;default:uuid_generate_v7()" json:"id"`
	OrganizacionID     uuid.UUID `gorm:"type:uuid;not null" json:"organizacionId"`
	
	NombreCompleto     string    `gorm:"type:varchar(255);not null" json:"nombreCompleto"`
	NombreUsuario      string    `gorm:"type:varchar(100);uniqueIndex;not null" json:"nombreUsuario"`
	EmailUsuario       string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"emailUsuario"`
	ContrasenaHash     string    `gorm:"type:varchar(255);not null" json:"-"` // Oculto en JSON
	
	Rol                string    `gorm:"type:varchar(50);not null" json:"rol"` // ADMIN u OPERATOR[cite: 11, 13]
	Activo             bool      `gorm:"default:true" json:"activo"`
	DebeCambiarContrasena   bool      `gorm:"default:true" json:"cambioContrasena"`
	
	// Campos 2FA (RF-01)
	TotpVinculado      bool      `gorm:"default:false" json:"totpVinculado"`
	SecretoTotpCifrado string    `gorm:"type:varchar(255)" json:"-"` // Oculto en JSON[cite: 11]
	
	FechaUltimoAcceso  *time.Time `json:"fechaUltimoAcceso"` // Puntero porque puede ser null inicialmente
	FechaCreacion      time.Time  `gorm:"default:now()" json:"fechaCreacion"`
}

// ==========================================
// 3. SESIÓN ACTIVA
// ==========================================

type SesionActiva struct {
	ID              uuid.UUID `gorm:"type:uuid;primaryKey;default:uuid_generate_v7()" json:"id"`
	UsuarioID       uuid.UUID `gorm:"type:uuid;not null;index" json:"usuarioId"`
	
	JtiToken        string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"-"`
	TokenHash       string    `gorm:"type:varchar(64);uniqueIndex;not null" json:"-"`
	Proposito       string    `gorm:"type:varchar(30);not null;index" json:"-"`
	Activa          bool      `gorm:"default:true" json:"activa"`
	Consumida       bool      `gorm:"default:false" json:"-"`
	RecordarSesion  bool      `gorm:"default:false" json:"-"`
	Estado2fa       bool      `gorm:"default:false" json:"estado2fa"` // Control intermedio del login[cite: 11]
	
	FechaExpiracion time.Time `gorm:"not null" json:"fechaExpiracion"`
	FechaCreacion   time.Time `gorm:"default:now()" json:"fechaCreacion"`
}

// ==========================================
// 4. PERMISO INSTANCIA (Matriz de Acceso)
// ==========================================

type PermisoInstancia struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey;default:uuid_generate_v7()" json:"id"`
	UsuarioID   uuid.UUID `gorm:"type:uuid;not null;index" json:"usuarioId"`
	VmidProxmox int       `gorm:"not null;index" json:"vmidProxmox"`
}

// ==========================================
// 5. AUDITORÍA (Append-Only Log)
// ==========================================

type Auditoria struct {
	ID              uuid.UUID `gorm:"type:uuid;primaryKey;default:uuid_generate_v7()" json:"id"`
	UsuarioID       uuid.UUID `gorm:"type:uuid;not null;index" json:"usuarioId"`
	
	Accion          string    `gorm:"type:varchar(100);not null" json:"accion"`
	InstanciaID     string    `gorm:"type:varchar(100)" json:"instanciaId"`
	InstanciaNombre string    `gorm:"type:varchar(255)" json:"instanciaNombre"`
	Resultado       string    `gorm:"type:varchar(50);not null" json:"resultado"` // EXITO o FALLA[cite: 18, 19]
	
	Detalles        string    `gorm:"type:jsonb" json:"detalles"` // JSON estructurado con metadata extra
	FechaHora       time.Time `gorm:"default:now();index" json:"fechaHora"`
}

// ==========================================
// 6. TAREA ASÍNCRONA (Gestión de UPIDs)
// ==========================================

type TareaAsincrona struct {
	ID            uuid.UUID `gorm:"type:uuid;primaryKey;default:uuid_generate_v7()" json:"id"` // ID limpio para el front
	UsuarioID     uuid.UUID `gorm:"type:uuid;not null" json:"usuarioId"`
	
	UpidProxmox   string    `gorm:"type:varchar(255);not null;uniqueIndex" json:"-"` // Crudo oculto al front
	InstanciaID   string    `gorm:"type:varchar(100)" json:"instanciaId"`
	Accion        string    `gorm:"type:varchar(100)" json:"accion"`
	Estado        string    `gorm:"type:varchar(50);not null" json:"estado"` // RUNNING, COMPLETED, FAILED[cite: 14]
	
	FechaCreacion time.Time `gorm:"default:now()" json:"fechaCreacion"`
}

// ==========================================
// 7. NOTIFICACIÓN
// ==========================================

type Notificacion struct {
	ID            uuid.UUID `gorm:"type:uuid;primaryKey;default:uuid_generate_v7()" json:"id"`
	UsuarioID     uuid.UUID `gorm:"type:uuid;not null;index" json:"usuarioId"`
	
	InstanciaID   string    `gorm:"type:varchar(100)" json:"instanciaId"`
	TipoEvento    string    `gorm:"type:varchar(100)" json:"tipoEvento"`
	Mensaje       string    `gorm:"type:text;not null" json:"mensaje"`
	Leida         bool      `gorm:"default:false" json:"leida"`
	
	FechaCreacion time.Time `gorm:"default:now()" json:"fechaCreacion"`
}