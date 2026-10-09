package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AuthRepository implementa ports.AuthRepository usando GORM sobre PostgreSQL.
type AuthRepository struct {
	db *gorm.DB
}

// NewAuthRepository crea una nueva instancia del repositorio de autenticación.
func NewAuthRepository(db *gorm.DB) *AuthRepository {
	return &AuthRepository{db: db}
}

// BuscarUsuarioPorEmail busca el usuario no eliminado con ese email (sin
// distinguir mayúsculas ni espacios). Un mismo correo puede estar en varias
// filas eliminadas, pero en una sola no eliminada (uq_usuarios_email_activo_lower).
// Retorna un error descriptivo si no se encuentra o si hay un error de BD.
func (r *AuthRepository) BuscarUsuarioPorEmail(ctx context.Context, email string) (*domain.Usuario, error) {
	var usuario domain.Usuario
	result := r.db.WithContext(ctx).
		Where("lower(btrim(email_usuario)) = lower(btrim(?)) AND eliminado_en IS NULL", email).
		First(&usuario)

	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("usuario no encontrado")
		}
		return nil, fmt.Errorf("error al buscar usuario por email: %w", result.Error)
	}
	return &usuario, nil
}

// BuscarUsuarioPorID busca un usuario por su UUID.
func (r *AuthRepository) BuscarUsuarioPorID(ctx context.Context, id uuid.UUID) (*domain.Usuario, error) {
	var usuario domain.Usuario
	result := r.db.WithContext(ctx).First(&usuario, "id = ?", id)

	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("usuario no encontrado")
		}
		return nil, fmt.Errorf("error al buscar usuario por ID: %w", result.Error)
	}
	return &usuario, nil
}

// CrearSesionYRegistrarAcceso inserta la única fila de la sesión y actualiza
// fecha_ultimo_acceso del usuario en la misma transacción.
func (r *AuthRepository) CrearSesionYRegistrarAcceso(ctx context.Context, sesion *domain.SesionActiva, fechaAcceso time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(sesion).Error; err != nil {
			return fmt.Errorf("error al crear sesión: %w", err)
		}
		if err := tx.Model(&domain.Usuario{}).Where("id = ?", sesion.UsuarioID).
			Update("fecha_ultimo_acceso", fechaAcceso).Error; err != nil {
			return fmt.Errorf("error al registrar último acceso: %w", err)
		}
		return nil
	})
}

// BuscarSesionPorID recupera una sesión por su session_id, esté activa o no.
func (r *AuthRepository) BuscarSesionPorID(ctx context.Context, sesionID uuid.UUID) (*domain.SesionActiva, error) {
	var sesion domain.SesionActiva
	result := r.db.WithContext(ctx).Where("id = ?", sesionID).First(&sesion)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, ports.ErrSesionRevocada
		}
		return nil, fmt.Errorf("error al buscar sesión: %w", result.Error)
	}
	return &sesion, nil
}

// RotarJtiAccess es el UPDATE de la renovación del access token. La condición
// del WHERE hace que una sesión cerrada, revocada o vencida no se pueda renovar.
func (r *AuthRepository) RotarJtiAccess(ctx context.Context, sesionID uuid.UUID, jtiRefresh, nuevoJtiAccess string) error {
	result := r.db.WithContext(ctx).
		Model(&domain.SesionActiva{}).
		Where("id = ? AND jti_refresh = ? AND activa = true AND fecha_expiracion > ?", sesionID, jtiRefresh, time.Now()).
		Updates(map[string]interface{}{"jti_access": nuevoJtiAccess, "fecha_actualizacion": time.Now()})
	if result.Error != nil {
		return fmt.Errorf("error al renovar sesión: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ports.ErrSesionRevocada
	}
	return nil
}

// DesactivarSesion marca la sesión como inactiva (logout). Es idempotente: si
// ya estaba inactiva no falla, pero el refresh tiene que corresponder a la sesión.
func (r *AuthRepository) DesactivarSesion(ctx context.Context, sesionID uuid.UUID, jtiRefresh string) error {
	result := r.db.WithContext(ctx).
		Model(&domain.SesionActiva{}).
		Where("id = ? AND jti_refresh = ?", sesionID, jtiRefresh).
		Updates(map[string]interface{}{"activa": false, "fecha_actualizacion": time.Now()})
	if result.Error != nil {
		return fmt.Errorf("error al cerrar sesión: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ports.ErrSesionRevocada
	}
	return nil
}

// ActualizarTotp guarda el secreto TOTP cifrado y el estado de vinculación del usuario.
func (r *AuthRepository) ActualizarTotp(ctx context.Context, usuarioID uuid.UUID, secretoCifrado string, vinculado bool) error {
	result := r.db.WithContext(ctx).
		Model(&domain.Usuario{}).
		Where("id = ?", usuarioID).
		Updates(map[string]interface{}{
			"secreto_totp_cifrado": secretoCifrado,
			"totp_vinculado":       vinculado,
		})
	if result.Error != nil {
		return fmt.Errorf("error al actualizar TOTP del usuario: %w", result.Error)
	}
	return nil
}

// ResetearTotp borra el secreto TOTP y marca el 2FA como no vinculado.
// Esto fuerza al usuario a volver a escanear el QR en su próximo login.
func (r *AuthRepository) ResetearTotp(ctx context.Context, usuarioID uuid.UUID) error {
	result := r.db.WithContext(ctx).
		Model(&domain.Usuario{}).
		Where("id = ?", usuarioID).
		Updates(map[string]interface{}{
			"secreto_totp_cifrado": "",
			"totp_vinculado":       false,
		})
	if result.Error != nil {
		return fmt.Errorf("error al resetear TOTP del usuario: %w", result.Error)
	}
	return nil
}

// InvalidarSesionesDeUsuario marca todas las sesiones activas de un usuario como
// inactivas y devuelve sus IDs, para que el servicio las borre también de Redis.
func (r *AuthRepository) InvalidarSesionesDeUsuario(ctx context.Context, usuarioID uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&domain.SesionActiva{}).
			Where("usuario_id = ? AND activa = true", usuarioID).
			Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		return tx.Model(&domain.SesionActiva{}).Where("id IN ?", ids).
			Updates(map[string]interface{}{"activa": false, "fecha_actualizacion": time.Now()}).Error
	})
	if err != nil {
		return nil, fmt.Errorf("error al invalidar sesiones del usuario: %w", err)
	}
	return ids, nil
}

// ActualizarUltimoTotpPeriodo guarda el período TOTP del último código validado exitosamente.
// Se usa para protección anti-replay: evita que el mismo código sea aceptado dos veces en la misma ventana de 30s.
func (r *AuthRepository) ActualizarUltimoTotpPeriodo(ctx context.Context, usuarioID uuid.UUID, periodo int64) error {
	result := r.db.WithContext(ctx).
		Model(&domain.Usuario{}).
		Where("id = ?", usuarioID).
		Update("ultimo_totp_periodo", periodo)
	if result.Error != nil {
		return fmt.Errorf("error al actualizar último período TOTP: %w", result.Error)
	}
	return nil
}

// ActualizarCodigoRecuperacion guarda el código de 6 dígitos y su expiración en el usuario.
// Si codigo y expiracion son nil, se limpian los valores (código ya utilizado o invalidado).
func (r *AuthRepository) ActualizarCodigoRecuperacion(ctx context.Context, usuarioID uuid.UUID, codigo *string, expiracion *time.Time) error {
	updates := map[string]interface{}{
		"codigo_recuperacion":   codigo,
		"expiracion_codigo":     expiracion,
		"intentos_recuperacion": 0,
	}
	result := r.db.WithContext(ctx).
		Model(&domain.Usuario{}).
		Where("id = ?", usuarioID).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("error al actualizar código de recuperación: %w", result.Error)
	}
	return nil
}

// ActualizarContrasenaYLimpiarCodigo cambia la contraseña, limpia el código temporal y quita el flag de cambio obligatorio.
func (r *AuthRepository) ActualizarContrasenaYLimpiarCodigo(ctx context.Context, usuarioID uuid.UUID, hash string) error {
	updates := map[string]interface{}{
		"codigo_recuperacion":   nil,
		"expiracion_codigo":     nil,
		"contrasena_hash":       hash,
		"cambio_contrasena":     false,
		"intentos_recuperacion": 0,
	}
	result := r.db.WithContext(ctx).
		Model(&domain.Usuario{}).
		Where("id = ?", usuarioID).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("error al actualizar contraseña: %w", result.Error)
	}
	return nil
}

// ActualizarIntentosRecuperacion actualiza el número de intentos fallidos al recuperar contraseña.
func (r *AuthRepository) ActualizarIntentosRecuperacion(ctx context.Context, usuarioID uuid.UUID, intentos int) error {
	result := r.db.WithContext(ctx).
		Model(&domain.Usuario{}).
		Where("id = ?", usuarioID).
		Update("intentos_recuperacion", intentos)
	if result.Error != nil {
		return fmt.Errorf("error al actualizar intentos de recuperación: %w", result.Error)
	}
	return nil
}
