package postgres

import (
	"context"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type UserRepository struct {
	db *gorm.DB
}

func NewUserRepository(db *gorm.DB) *UserRepository {
	return &UserRepository{db: db}
}

func (repository *UserRepository) FindByEmail(ctx context.Context, email string) (*domain.Usuario, error) {
	var user domain.Usuario
	err := repository.db.WithContext(ctx).Where("LOWER(email_usuario) = LOWER(?)", email).First(&user).Error
	return &user, err
}

func (repository *UserRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Usuario, error) {
	var user domain.Usuario
	err := repository.db.WithContext(ctx).First(&user, "id = ?", id).Error
	return &user, err
}

func (repository *UserRepository) UpdateLastAccess(ctx context.Context, id uuid.UUID, accessedAt time.Time) error {
	return repository.db.WithContext(ctx).Model(&domain.Usuario{}).Where("id = ?", id).Update("fecha_ultimo_acceso", accessedAt).Error
}

func (repository *UserRepository) SetTwoFactorSecret(ctx context.Context, id uuid.UUID, secret string) error {
	return repository.db.WithContext(ctx).Model(&domain.Usuario{}).Where("id = ?", id).Update("secreto_totp_cifrado", secret).Error
}

func (repository *UserRepository) EnableTwoFactor(ctx context.Context, id uuid.UUID) error {
	return repository.db.WithContext(ctx).Model(&domain.Usuario{}).Where("id = ?", id).Update("totp_vinculado", true).Error
}

type PermissionRepository struct {
	db *gorm.DB
}

func NewPermissionRepository(db *gorm.DB) *PermissionRepository {
	return &PermissionRepository{db: db}
}

func (repository *PermissionRepository) ListInstanceIDs(ctx context.Context, userID uuid.UUID) ([]int, error) {
	var permissions []domain.PermisoInstancia
	if err := repository.db.WithContext(ctx).Where("usuario_id = ?", userID).Find(&permissions).Error; err != nil {
		return nil, err
	}
	instanceIDs := make([]int, 0, len(permissions))
	for _, permission := range permissions {
		instanceIDs = append(instanceIDs, permission.VmidProxmox)
	}
	return instanceIDs, nil
}

type AuditRepository struct {
	db *gorm.DB
}

func NewAuditRepository(db *gorm.DB) *AuditRepository {
	return &AuditRepository{db: db}
}

func (repository *AuditRepository) Record(ctx context.Context, audit *domain.Auditoria) error {
	return repository.db.WithContext(ctx).Create(audit).Error
}

type SessionRepository struct {
	db *gorm.DB
}

func NewSessionRepository(db *gorm.DB) *SessionRepository {
	return &SessionRepository{db: db}
}

func (repository *SessionRepository) CreateChallenge(ctx context.Context, session ports.Session) error {
	return repository.db.WithContext(ctx).Create(toDomainSession(session)).Error
}

func (repository *SessionRepository) ConsumeChallenge(ctx context.Context, tokenHash string, now time.Time) (*ports.Session, error) {
	var record domain.SesionActiva
	err := repository.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("token_hash = ? AND proposito IN ? AND activa = ? AND consumida = ? AND fecha_expiracion > ?", tokenHash, []string{"2FA_SETUP", "2FA_VERIFY"}, true, false, now).First(&record).Error; err != nil {
			return err
		}
		return tx.Model(&record).Updates(map[string]interface{}{"consumida": true, "activa": false}).Error
	})
	if err != nil {
		return nil, err
	}
	result := fromDomainSession(record)
	return &result, nil
}

func (repository *SessionRepository) FindActiveRefresh(ctx context.Context, tokenHash string, now time.Time) (*ports.Session, error) {
	var record domain.SesionActiva
	err := repository.db.WithContext(ctx).Where("token_hash = ? AND proposito = ? AND activa = ? AND consumida = ? AND fecha_expiracion > ?", tokenHash, "REFRESH", true, false, now).First(&record).Error
	if err != nil {
		return nil, err
	}
	result := fromDomainSession(record)
	return &result, nil
}

func (repository *SessionRepository) CreateRefreshSession(ctx context.Context, session ports.Session) error {
	return repository.db.WithContext(ctx).Create(toDomainSession(session)).Error
}

func (repository *SessionRepository) RotateRefreshSession(ctx context.Context, oldHash string, now time.Time, next ports.Session) error {
	return repository.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current domain.SesionActiva
		if err := tx.Where("token_hash = ? AND proposito = ? AND activa = ? AND fecha_expiracion > ?", oldHash, "REFRESH", true, now).First(&current).Error; err != nil {
			return err
		}
		if err := tx.Model(&current).Updates(map[string]interface{}{"activa": false, "consumida": true}).Error; err != nil {
			return err
		}
		return tx.Create(toDomainSession(next)).Error
	})
}

func (repository *SessionRepository) RevokeRefreshSession(ctx context.Context, tokenHash string, now time.Time) error {
	return repository.db.WithContext(ctx).Model(&domain.SesionActiva{}).Where("token_hash = ? AND proposito = ? AND activa = ? AND fecha_expiracion > ?", tokenHash, "REFRESH", true, now).Updates(map[string]interface{}{"activa": false, "consumida": true}).Error
}

func toDomainSession(session ports.Session) *domain.SesionActiva {
	return &domain.SesionActiva{ID: session.ID, UsuarioID: session.UserID, JtiToken: uuid.NewString(), TokenHash: session.ChallengeHash + session.RefreshHash, Proposito: session.Purpose, Activa: true, Consumida: false, RecordarSesion: session.Remember, FechaExpiracion: session.ExpiresAt}
}

func fromDomainSession(record domain.SesionActiva) ports.Session {
	session := ports.Session{ID: record.ID, UserID: record.UsuarioID, ExpiresAt: record.FechaExpiracion, Remember: record.RecordarSesion, Purpose: record.Proposito}
	if record.Proposito == "REFRESH" {
		session.RefreshHash = record.TokenHash
	} else {
		session.ChallengeHash = record.TokenHash
	}
	return session
}