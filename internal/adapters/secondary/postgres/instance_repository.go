package postgres

import (
	"context"
	"fmt"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

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
// para el par (usuario_id, vmid_proxmox) que satisfaga el nivel requerido.
// Usa COUNT para evitar cargar el registro completo.
func (r *InstanceRepository) VerificarAcceso(ctx context.Context, userID uuid.UUID, vmid int, nivelRequerido string) (bool, error) {
	query := r.db.WithContext(ctx).
		Model(&domain.PermisoInstancia{}).
		Where("usuario_id = ? AND vmid_proxmox = ?", userID, vmid)

	if nivelRequerido == ports.NivelAccesoFullAccess {
		query = query.Where("nivel_acceso = ?", ports.NivelAccesoFullAccess)
	}
	// Para READ_ONLY no se filtra por nivel: cualquier fila existente
	// (READ_ONLY o FULL_ACCESS) satisface el requisito.

	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, fmt.Errorf("error al verificar acceso a instancia: %w", err)
	}
	return count > 0, nil
}

// PurgarPermisosPorVmid elimina atómicamente todas las filas vinculadas a ese VMID
// en permisos_instancia y registra la auditoría inmutable de la purga en la misma transacción.
func (r *InstanceRepository) PurgarPermisosPorVmid(ctx context.Context, vmid int, recursoTipo string, actorID *uuid.UUID) ([]string, error) {
	usuariosAfectados := make([]string, 0)

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Obtener lista de usuarios afectados para trazabilidad
		if err := tx.Model(&domain.PermisoInstancia{}).
			Where("vmid_proxmox = ?", vmid).
			Pluck("usuario_id", &usuariosAfectados).Error; err != nil {
			return fmt.Errorf("error al obtener usuarios afectados para purga: %w", err)
		}

		// 2. Eliminar relaciones de acceso
		if err := tx.Where("vmid_proxmox = ?", vmid).
			Delete(&domain.PermisoInstancia{}).Error; err != nil {
			return fmt.Errorf("error al eliminar permisos de instancia para vmid %d: %w", vmid, err)
		}

		// 3. Registrar auditoría inmutable
		if recursoTipo == "" {
			recursoTipo = ports.RecursoVM
		}

		var usuarioIDPtr *uuid.UUID
		if actorID != nil && *actorID != uuid.Nil {
			var existe int64
			if err := tx.Table("usuarios").Where("id = ?", *actorID).Count(&existe).Error; err == nil && existe > 0 {
				usuarioIDPtr = actorID
			}
		}

		detalles := map[string]any{
			"vmid":              vmid,
			"usuariosAfectados": usuariosAfectados,
			"resource_type":     recursoTipo,
		}
		var usuarioID uuid.UUID
		if usuarioIDPtr != nil {
			usuarioID = *usuarioIDPtr
		}
		if err := NewAuditRepository(tx).Registrar(ctx, ports.RegistrarAuditoriaInput{
			UsuarioID:   usuarioID,
			Accion:      ports.AccionInstancePermissionsPurged,
			InstanciaID: fmt.Sprintf("%d", vmid),
			Resultado:   ports.ResultadoSuccess,
			Detalles:    detalles,
		}); err != nil {
			return fmt.Errorf("error al registrar auditoría de purga: %w", err)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}
	return usuariosAfectados, nil
}
