package ports

import (
	"context"
	"errors"
)

// ErrVerificacionPostConfig indica que Proxmox aceptó el PUT /config, pero la
// lectura posterior no pudo determinar si el cambio requiere reinicio.
var ErrVerificacionPostConfig = errors.New("no se pudo verificar la configuración aplicada")

// ResultadoReconfiguracion es el desenlace de cambiar CPU o RAM de una instancia.
type ResultadoReconfiguracion struct {
	// Aplicada distingue un rechazo del PUT de un fallo posterior de lectura.
	Aplicada bool
	// VerificacionPendiente indica que requiresReboot es desconocido porque
	// falló la lectura posterior, aunque Proxmox ya aceptó el cambio.
	VerificacionPendiente bool
	// RequiresReboot: la instancia está encendida y Proxmox dejó el cambio como
	// pendiente (sin hot-plug); se aplica en el próximo reinicio.
	RequiresReboot bool
	RecursoTipo    string             // VM | LXC para la auditoría estructurada
	Anterior       ConfigInstanciaDTO // CPU y RAM en uso antes del cambio
	Solicitada     ConfigInstanciaDTO // estado completo esperado luego del cambio
}

// ConfiguracionInstancias cambia la CPU y la RAM de una instancia existente.
type ConfiguracionInstancias interface {
	// Reconfigurar aplica cores y memoriaMB (0 = no cambia ese recurso). Los
	// incrementos se validan contra el margen del nodo: si no alcanza devuelve
	// ErrRecursosInsuficientes sin escribir en Proxmox.
	Reconfigurar(ctx context.Context, vmid, cores, memoriaMB int) (*ResultadoReconfiguracion, error)
}
