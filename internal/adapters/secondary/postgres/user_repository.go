package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
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
		Where("organizacion_id = ? AND eliminado_en IS NULL", orgID).
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
	// Los usuarios eliminados (eliminado_en) no se listan: sus filas se conservan
	// solo por la auditoría. Los suspendidos (activo = false) sí.
	query := r.db.WithContext(ctx).
		Where("organizacion_id = ? AND eliminado_en IS NULL", orgID).
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
// Encuentra también a los eliminados (para su detalle y su actividad histórica):
// el servicio impide modificarlos.
func (r *UserRepository) BuscarUsuarioPorIDEnOrg(ctx context.Context, id, orgID uuid.UUID) (*domain.Usuario, error) {
	var usuario domain.Usuario
	result := r.db.WithContext(ctx).
		Where("id = ? AND organizacion_id = ?", id, orgID).
		First(&usuario)

	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, ports.ErrUsuarioNoEncontrado
		}
		return nil, fmt.Errorf("error al buscar usuario: %w", result.Error)
	}
	return &usuario, nil
}

// ExisteEmailEnOrg verifica si el email ya está registrado en la organización
// por un usuario no eliminado (activo o suspendido), sin distinguir mayúsculas
// ni espacios: el mismo criterio que el índice uq_usuarios_email_activo_lower.
// excluirID permite excluir al propio usuario en operaciones de actualización.
func (r *UserRepository) ExisteEmailEnOrg(ctx context.Context, email string, orgID uuid.UUID, excluirID *uuid.UUID) (bool, error) {
	query := r.db.WithContext(ctx).
		Model(&domain.Usuario{}).
		Where("lower(btrim(email_usuario)) = lower(btrim(?)) AND eliminado_en IS NULL AND organizacion_id = ?", email, orgID)
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

// CrearUsuario persiste un nuevo usuario en la base de datos. Si el correo o el
// nombre de usuario chocan con un índice único (por ejemplo, dos altas
// simultáneas con el mismo correo) devuelve el mismo error que la verificación previa.
func (r *UserRepository) CrearUsuario(ctx context.Context, u *domain.Usuario) error {
	if err := r.db.WithContext(ctx).Create(u).Error; err != nil {
		if violacion := violacionUnicidad(err); violacion != nil {
			return violacion
		}
		return fmt.Errorf("error al crear usuario: %w", err)
	}
	return nil
}

// ActualizarUsuario aplica cambios parciales a un usuario (solo los campos del mapa).
// Nunca toca a un usuario eliminado: una cuenta eliminada es inmutable (si un
// DELETE se adelanta a esta escritura, devuelve ports.ErrUsuarioEliminado).
func (r *UserRepository) ActualizarUsuario(ctx context.Context, id uuid.UUID, cambios map[string]any) error {
	result := r.db.WithContext(ctx).
		Model(&domain.Usuario{}).
		Where("id = ? AND eliminado_en IS NULL", id).
		Updates(cambios)
	if result.Error != nil {
		if violacion := violacionUnicidad(result.Error); violacion != nil {
			return violacion
		}
		return fmt.Errorf("error al actualizar usuario: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ports.ErrUsuarioEliminado
	}
	return nil
}

// EliminarLogicamente implementa la baja de ports.UserRepository en una sola
// transacción: marca eliminado_en (UTC) y activo = false, borra el código de
// recuperación vigente e invalida las sesiones activas. revocar se llama antes
// del COMMIT con los IDs de esas sesiones (para borrarlas de Redis): si falla,
// se hace ROLLBACK y el usuario queda como estaba (fail-closed). No toca otras
// tablas: permisos, tareas y auditoría quedan como historial.
func (r *UserRepository) EliminarLogicamente(ctx context.Context, id uuid.UUID, revocar func(sesionIDs []uuid.UUID) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		baja := tx.Model(&domain.Usuario{}).
			Where("id = ? AND eliminado_en IS NULL", id).
			Updates(map[string]any{
				"activo":                false,
				"eliminado_en":          time.Now().UTC(),
				"codigo_recuperacion":   nil,
				"expiracion_codigo":     nil,
				"intentos_recuperacion": 0,
			})
		if baja.Error != nil {
			return fmt.Errorf("error al eliminar usuario: %w", baja.Error)
		}
		if baja.RowsAffected == 0 {
			return ports.ErrUsuarioEliminado
		}

		var sesiones []uuid.UUID
		if err := tx.Model(&domain.SesionActiva{}).
			Where("usuario_id = ? AND activa = true", id).
			Pluck("id", &sesiones).Error; err != nil {
			return fmt.Errorf("%w: %v", ports.ErrRevocacionFallida, err)
		}
		if len(sesiones) > 0 {
			if err := tx.Model(&domain.SesionActiva{}).Where("id IN ?", sesiones).
				Updates(map[string]any{"activa": false, "fecha_actualizacion": time.Now()}).Error; err != nil {
				return fmt.Errorf("%w: %v", ports.ErrRevocacionFallida, err)
			}
		}
		if err := revocar(sesiones); err != nil {
			return fmt.Errorf("%w: %v", ports.ErrRevocacionFallida, err)
		}
		return nil
	})
}

// ListarPermisosDeUsuario devuelve los VMIDs a los que tiene acceso un usuario,
// sin su nivel (lo usa el filtrado RBAC del listado de instancias).
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

// ListarPermisosConNivel devuelve los permisos de un usuario junto con su nivel de acceso.
func (r *UserRepository) ListarPermisosConNivel(ctx context.Context, usuarioID uuid.UUID) ([]ports.PermisoInstanciaInput, error) {
	var permisos []domain.PermisoInstancia
	if err := r.db.WithContext(ctx).
		Where("usuario_id = ?", usuarioID).
		Find(&permisos).Error; err != nil {
		return nil, fmt.Errorf("error al listar permisos de usuario: %w", err)
	}

	resultado := make([]ports.PermisoInstanciaInput, len(permisos))
	for i, p := range permisos {
		resultado[i] = ports.PermisoInstanciaInput{Vmid: p.VmidProxmox, NivelAcceso: p.NivelAcceso}
	}
	return resultado, nil
}

// ReemplazarPermisos reemplaza atómicamente todos los permisos de instancia de un usuario.
// Usa una transacción: primero borra todos, luego inserta los nuevos.
func (r *UserRepository) ReemplazarPermisos(ctx context.Context, usuarioID uuid.UUID, permisos []ports.PermisoInstanciaInput) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Borrar todos los permisos actuales del usuario
		if err := tx.Where("usuario_id = ?", usuarioID).
			Delete(&domain.PermisoInstancia{}).Error; err != nil {
			return fmt.Errorf("error al limpiar permisos existentes: %w", err)
		}

		// 2. Insertar los nuevos (si hay alguno)
		if len(permisos) == 0 {
			return nil
		}

		// 2.1 Deduplicar por vmid en memoria (se queda con la primera aparición)
		vmidsVistos := make(map[int]bool)
		var permisosFiltrados []ports.PermisoInstanciaInput
		for _, p := range permisos {
			if !vmidsVistos[p.Vmid] {
				vmidsVistos[p.Vmid] = true
				permisosFiltrados = append(permisosFiltrados, p)
			}
		}

		// 2.2 Crear los structs a insertar
		nuevos := make([]domain.PermisoInstancia, len(permisosFiltrados))
		for i, p := range permisosFiltrados {
			nuevos[i] = domain.PermisoInstancia{
				ID:          uuid.New(),
				UsuarioID:   usuarioID,
				VmidProxmox: p.Vmid,
				NivelAcceso: p.NivelAcceso,
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

// violacionUnicidad traduce una violación de índice único (SQLSTATE 23505) de
// usuarios al error de negocio; nil si el error es otro (que NO se presenta como
// conflicto: es un error de la base).
func violacionUnicidad(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return nil
	}
	if pgErr.ConstraintName == indiceEmailParcial || strings.Contains(pgErr.ConstraintName, "email") {
		return ports.ErrEmailYaRegistrado
	}
	if strings.Contains(pgErr.ConstraintName, "nombre_usuario") {
		return ports.ErrUsernameYaRegistrado
	}
	return nil
}
