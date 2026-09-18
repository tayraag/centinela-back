package postgres

import (
	"context"
	"errors"
	"fmt"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// UserRepository implementa ports.UserRepository usando GORM sobre PostgreSQL.
type UserRepository struct {
	db *gorm.DB
}

// NewUserRepository crea una nueva instancia del repositorio de usuarios.
func NewUserRepository(db *gorm.DB) *UserRepository {
	return &UserRepository{db: db}
}

// ListarUsuarios devuelve usuarios de una organización con filtros y estadísticas.
func (r *UserRepository) ListarUsuarios(ctx context.Context, orgID uuid.UUID, filtros ports.FiltrosUsuario) (*ports.ListaUsuariosResult, error) {
	// --- Calcular estadísticas con una sola query de agregación ---
	type conteo struct {
		Rol   string
		Total int64
	}
	var conteos []conteo
	if err := r.db.WithContext(ctx).
		Model(&domain.Usuario{}).
		Select("rol, COUNT(*) as total").
		Where("organizacion_id = ?", orgID).
		Group("rol").
		Scan(&conteos).Error; err != nil {
		return nil, fmt.Errorf("error al calcular estadísticas de usuarios: %w", err)
	}

	summary := ports.ResumenUsuariosDTO{}
	for _, c := range conteos {
		summary.Total += c.Total
		switch c.Rol {
		case "ADMIN":
			summary.Admins = c.Total
		case "OPERATOR":
			summary.Operators = c.Total
		}
	}

	// --- Construir query de listado con filtros ---
	query := r.db.WithContext(ctx).
		Where("organizacion_id = ?", orgID).
		Order("fecha_creacion DESC")

	if filtros.Rol != "" {
		query = query.Where("rol = ?", filtros.Rol)
	}
	if filtros.Activo != nil {
		query = query.Where("activo = ?", *filtros.Activo)
	}
	if filtros.Buscar != "" {
		like := "%" + filtros.Buscar + "%"
		query = query.Where("nombre_completo ILIKE ? OR email_usuario ILIKE ?", like, like)
	}

	var usuarios []domain.Usuario
	if err := query.Find(&usuarios).Error; err != nil {
		return nil, fmt.Errorf("error al listar usuarios: %w", err)
	}

	// Proyectar a DTOs (sin exponer campos sensibles)
	dtos := make([]ports.UsuarioResumenDTO, len(usuarios))
	for i, u := range usuarios {
		dtos[i] = usuarioAResumenDTO(u)
	}

	return &ports.ListaUsuariosResult{
		Summary: summary,
		Users:   dtos,
	}, nil
}

// BuscarUsuarioPorIDEnOrg busca un usuario verificando que pertenezca a la organización.
func (r *UserRepository) BuscarUsuarioPorIDEnOrg(ctx context.Context, id, orgID uuid.UUID) (*domain.Usuario, error) {
	var usuario domain.Usuario
	result := r.db.WithContext(ctx).
		Where("id = ? AND organizacion_id = ?", id, orgID).
		First(&usuario)

	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("usuario no encontrado")
		}
		return nil, fmt.Errorf("error al buscar usuario: %w", result.Error)
	}
	return &usuario, nil
}

// ExisteEmailEnOrg verifica si el email ya está registrado en la organización.
// excluirID permite excluir al propio usuario en operaciones de actualización.
func (r *UserRepository) ExisteEmailEnOrg(ctx context.Context, email string, orgID uuid.UUID, excluirID *uuid.UUID) (bool, error) {
	query := r.db.WithContext(ctx).
		Model(&domain.Usuario{}).
		Where("email_usuario = ? AND organizacion_id = ?", email, orgID)
	if excluirID != nil {
		query = query.Where("id != ?", *excluirID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, fmt.Errorf("error al verificar email: %w", err)
	}
	return count > 0, nil
}

// ExisteUsernameEnOrg verifica si el nombre de usuario ya está registrado en la organización.
func (r *UserRepository) ExisteUsernameEnOrg(ctx context.Context, username string, orgID uuid.UUID, excluirID *uuid.UUID) (bool, error) {
	query := r.db.WithContext(ctx).
		Model(&domain.Usuario{}).
		Where("nombre_usuario = ? AND organizacion_id = ?", username, orgID)
	if excluirID != nil {
		query = query.Where("id != ?", *excluirID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, fmt.Errorf("error al verificar username: %w", err)
	}
	return count > 0, nil
}

// CrearUsuario persiste un nuevo usuario en la base de datos.
func (r *UserRepository) CrearUsuario(ctx context.Context, u *domain.Usuario) error {
	if err := r.db.WithContext(ctx).Create(u).Error; err != nil {
		return fmt.Errorf("error al crear usuario: %w", err)
	}
	return nil
}

// ActualizarUsuario aplica cambios parciales a un usuario (solo los campos del mapa).
func (r *UserRepository) ActualizarUsuario(ctx context.Context, id uuid.UUID, cambios map[string]any) error {
	result := r.db.WithContext(ctx).
		Model(&domain.Usuario{}).
		Where("id = ?", id).
		Updates(cambios)
	if result.Error != nil {
		return fmt.Errorf("error al actualizar usuario: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("usuario no encontrado")
	}
	return nil
}

// ListarPermisosDeUsuario devuelve los VMIDs a los que tiene acceso un usuario.
func (r *UserRepository) ListarPermisosDeUsuario(ctx context.Context, usuarioID uuid.UUID) ([]int, error) {
	var permisos []domain.PermisoInstancia
	if err := r.db.WithContext(ctx).
		Where("usuario_id = ?", usuarioID).
		Find(&permisos).Error; err != nil {
		return nil, fmt.Errorf("error al listar permisos de usuario: %w", err)
	}

	vmids := make([]int, len(permisos))
	for i, p := range permisos {
		vmids[i] = p.VmidProxmox
	}
	return vmids, nil
}

// ReemplazarPermisos reemplaza atómicamente todos los permisos de instancia de un usuario.
// Usa una transacción: primero borra todos, luego inserta los nuevos.
func (r *UserRepository) ReemplazarPermisos(ctx context.Context, usuarioID uuid.UUID, vmids []int) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Borrar todos los permisos actuales del usuario
		if err := tx.Where("usuario_id = ?", usuarioID).
			Delete(&domain.PermisoInstancia{}).Error; err != nil {
			return fmt.Errorf("error al limpiar permisos existentes: %w", err)
		}

		// 2. Insertar los nuevos (si hay alguno)
		if len(vmids) == 0 {
			return nil
		}
		
		// 2.1 Deduplicar vmids en memoria
		vmidsUnicos := make(map[int]bool)
		var vmidsFiltrados []int
		for _, v := range vmids {
			if !vmidsUnicos[v] {
				vmidsUnicos[v] = true
				vmidsFiltrados = append(vmidsFiltrados, v)
			}
		}

		// 2.2 Crear los structs a insertar
		nuevos := make([]domain.PermisoInstancia, len(vmidsFiltrados))
		for i, vmid := range vmidsFiltrados {
			nuevos[i] = domain.PermisoInstancia{
				ID:          uuid.New(),
				UsuarioID:   usuarioID,
				VmidProxmox: vmid,
			}
		}
		if err := tx.Create(&nuevos).Error; err != nil {
			return fmt.Errorf("error al insertar nuevos permisos: %w", err)
		}
		return nil
	})
}

// ListarActividadDeUsuario devuelve registros de auditoría filtrados por usuario y criterios opcionales.
func (r *UserRepository) ListarActividadDeUsuario(ctx context.Context, usuarioID uuid.UUID, filtros ports.FiltrosActividad) ([]domain.Auditoria, error) {
	query := r.db.WithContext(ctx).
		Where("usuario_id = ?", usuarioID).
		Order("fecha_hora DESC").
		Limit(100)

	if filtros.Accion != "" {
		query = query.Where("accion = ?", filtros.Accion)
	}
	if filtros.Desde != nil {
		query = query.Where("fecha_hora >= ?", *filtros.Desde)
	}
	if filtros.Hasta != nil {
		query = query.Where("fecha_hora <= ?", *filtros.Hasta)
	}

	var registros []domain.Auditoria
	if err := query.Find(&registros).Error; err != nil {
		return nil, fmt.Errorf("error al listar actividad del usuario: %w", err)
	}
	return registros, nil
}

// ==========================================
// Helpers de proyección
// ==========================================

// usuarioAResumenDTO convierte un domain.Usuario en un UsuarioResumenDTO.
// No se expone ContrasenaHash ni SecretoTotpCifrado.
func usuarioAResumenDTO(u domain.Usuario) ports.UsuarioResumenDTO {
	return ports.UsuarioResumenDTO{
		ID:                u.ID,
		NombreCompleto:    u.NombreCompleto,
		NombreUsuario:     u.NombreUsuario,
		EmailUsuario:      u.EmailUsuario,
		Rol:               u.Rol,
		Activo:            u.Activo,
		TotpVinculado:     u.TotpVinculado,
		FechaUltimoAcceso: u.FechaUltimoAcceso,
		FechaCreacion:     u.FechaCreacion,
	}
}
