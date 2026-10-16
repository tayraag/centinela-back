package ports

import "context"

// ResultadoReconfiguracion es el desenlace de cambiar CPU o RAM de una instancia.
type ResultadoReconfiguracion struct {
	// RequiresReboot: la instancia está encendida y Proxmox dejó el cambio como
	// pendiente (sin hot-plug); se aplica en el próximo reinicio.
	RequiresReboot bool
	Anterior       ConfigInstanciaDTO // CPU y RAM en uso antes del cambio
	Solicitada     ConfigInstanciaDTO // lo pedido (0 = ese recurso no se cambió)
}

// ConfiguracionInstancias cambia la CPU y la RAM de una instancia existente.
type ConfiguracionInstancias interface {
	// Reconfigurar aplica cores y memoriaMB (0 = no cambia ese recurso). Los
	// incrementos se validan contra el margen del nodo: si no alcanza devuelve
	// ErrRecursosInsuficientes sin escribir en Proxmox.
	Reconfigurar(ctx context.Context, vmid, cores, memoriaMB int) (*ResultadoReconfiguracion, error)
}
