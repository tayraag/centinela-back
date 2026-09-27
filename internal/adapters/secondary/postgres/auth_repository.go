package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"el-centinela/internal/core/domain"

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

// BuscarUsuarioPorEmail busca un usuario por su email.
// Retorna un error descriptivo si no se encuentra o si hay un error de BD.
func (r *AuthRepository) BuscarUsuarioPorEmail(ctx context.Context, email string) (*domain.Usuario, error) {
	var usuario domain.Usuario
	result := r.db.WithContext(ctx).
		Where("email_usuario = ?", email).
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

// GuardarSesion persiste una nueva sesión activa en la base de datos.
func (r *AuthRepository) GuardarSesion(ctx context.Context, sesion *domain.SesionActiva) error {
	result := r.db.WithContext(ctx).Create(sesion)
	if result.Error != nil {
		return fmt.Errorf("error al guardar sesión: %w", result.Error)
	}
	return nil
}

// BuscarSesionPorJTI recupera una sesión activa por su JTI (JWT ID).
func (r *AuthRepository) BuscarSesionPorJTI(ctx context.Context, jti string) (*domain.SesionActiva, error) {
	var sesion domain.SesionActiva
	result := r.db.WithContext(ctx).
		Where("jti_token = ? AND activa = true", jti).
		First(&sesion)

	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("sesión no encontrada o inactiva")
		}
		return nil, fmt.Errorf("error al buscar sesión por JTI: %w", result.Error)
	}
	return &sesion, nil
}

// ActualizarSesion actualiza los campos de una sesión existente.
func (r *AuthRepository) ActualizarSesion(ctx context.Context, sesion *domain.SesionActiva) error {
	result := r.db.WithContext(ctx).Save(sesion)
	if result.Error != nil {
		return fmt.Errorf("error al actualizar sesión: %w", result.Error)
	}
	return nil
}

// RevocarSesiones revoca múltiples sesiones en una sola operación atómica usando sus JTIs.
func (r *AuthRepository) RevocarSesiones(ctx context.Context, jtis []string) error {
	result := r.db.WithContext(ctx).
		Model(&domain.SesionActiva{}).
		Where("jti_token IN ?", jtis).
		Update("activa", false)
	if result.Error != nil {
		return fmt.Errorf("error al revocar sesiones: %w", result.Error)
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

// InvalidarSesionesDeUsuario marca todas las sesiones activas de un usuario como inactivas.
func (r *AuthRepository) InvalidarSesionesDeUsuario(ctx context.Context, usuarioID uuid.UUID) error {
	result := r.db.WithContext(ctx).
		Model(&domain.SesionActiva{}).
		Where("usuario_id = ? AND activa = true", usuarioID).
		Update("activa", false)
	if result.Error != nil {
		return fmt.Errorf("error al invalidar sesiones del usuario: %w", result.Error)
	}
	return nil
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

// ActualizarUltimoAcceso actualiza la fecha de último acceso del usuario.
func (r *AuthRepository) ActualizarUltimoAcceso(ctx context.Context, usuarioID uuid.UUID, fecha time.Time) error {
	result := r.db.WithContext(ctx).
		Model(&domain.Usuario{}).
		Where("id = ?", usuarioID).
		Update("fecha_ultimo_acceso", fecha)
	if result.Error != nil {
		return fmt.Errorf("error al actualizar último acceso: %w", result.Error)
	}
	return nil
}
