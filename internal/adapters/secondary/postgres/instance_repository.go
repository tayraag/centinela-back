package postgres

import (
	"context"
	"fmt"

	"el-centinela/internal/core/domain"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// InstanceRepository implementa ports.InstanceRepository usando GORM sobre PostgreSQL.
type InstanceRepository struct {
	db *gorm.DB
}

// NewInstanceRepository crea una nueva instancia del repositorio de permisos de instancia.
func NewInstanceRepository(db *gorm.DB) *InstanceRepository {
	return &InstanceRepository{db: db}
}

// VerificarAcceso devuelve true si existe una fila en permisos_instancia
// para el par (usuario_id, vmid_proxmox). Usa COUNT para evitar cargar el registro completo.
func (r *InstanceRepository) VerificarAcceso(ctx context.Context, userID uuid.UUID, vmid int) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&domain.PermisoInstancia{}).
		Where("usuario_id = ? AND vmid_proxmox = ?", userID, vmid).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("error al verificar acceso a instancia: %w", err)
	}
	return count > 0, nil
}
