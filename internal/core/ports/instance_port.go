package ports

import (
	"context"

	"github.com/google/uuid"
)

// ==========================================
// Puerto: Repositorio de Permisos de Instancia
// ==========================================

// InstanceRepository define el contrato de consulta sobre la tabla
// permisos_instancia, usado por el guard de autorización por recurso.
type InstanceRepository interface {
	// VerificarAcceso devuelve true si el usuario tiene permiso sobre el vmid dado.
	// La lógica de rol (ADMIN pasa siempre) se resuelve en el middleware,
	// no aquí: este método consulta únicamente la tabla de permisos.
	VerificarAcceso(ctx context.Context, userID uuid.UUID, vmid int) (bool, error)
}
