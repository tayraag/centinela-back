package ports

import (
	"context"

	"github.com/google/uuid"
)

// ==========================================
// Niveles de acceso a una instancia
// ==========================================

const (
	NivelAccesoFullAccess = "FULL_ACCESS"
	NivelAccesoReadOnly   = "READ_ONLY"
)

// PermisoInstanciaInput es un vmid junto con el nivel de acceso otorgado.
// Lo usan tanto la asignación de permisos (UserService.AsignarPermisos) como
// su consulta (UserService.ObtenerPermisos).
type PermisoInstanciaInput struct {
	Vmid        int    `json:"vmid"`
	NivelAcceso string `json:"nivelAcceso"`
}

// ==========================================
// Puerto: Repositorio de Permisos de Instancia
// ==========================================

// InstanceRepository define el contrato de consulta sobre la tabla
// permisos_instancia, usado por el guard de autorización por recurso.
type InstanceRepository interface {
	// VerificarAcceso devuelve true si el usuario tiene, como mínimo, el nivel
	// de acceso requerido sobre el vmid dado. Si nivelRequerido es
	// NivelAccesoFullAccess, solo cuenta una fila con exactamente ese nivel;
	// si es NivelAccesoReadOnly, cuenta cualquier fila existente (READ_ONLY o
	// FULL_ACCESS). La lógica de rol (ADMIN pasa siempre) se resuelve en el
	// middleware, no aquí: este método consulta únicamente la tabla de permisos.
	VerificarAcceso(ctx context.Context, userID uuid.UUID, vmid int, nivelRequerido string) (bool, error)
}
