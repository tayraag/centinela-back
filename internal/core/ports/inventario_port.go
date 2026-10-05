package ports

import "context"

// InstanciaInventario es una VM (qemu) o un contenedor (lxc) del inventario
// consolidado, con métricas crudas de Proxmox y la IP resuelta.
type InstanciaInventario struct {
	Vmid   int
	Nombre string
	Tipo   string // "qemu" | "lxc"
	Nodo   string
	Estado string  // "running" | "stopped" | ...
	CPU    float64 // fracción [0,1]
	Mem    int64   // bytes usados
	MaxMem int64   // bytes asignados
	MaxCPU int
	IP     *string // nil si está apagada, no tiene agente, no tiene red o no respondió a tiempo
}

// InventarioService consolida el inventario de Proxmox y resuelve las IPs.
type InventarioService interface {
	// Listar devuelve todas las VMs y contenedores con sus métricas y su IP.
	Listar(ctx context.Context) ([]InstanciaInventario, error)

	// ResolverIPs resuelve en paralelo (acotado) la IP de las instancias dadas.
	// Nunca devuelve error: la que no se pueda resolver queda en nil.
	ResolverIPs(ctx context.Context, instancias []InstanciaProxmoxDTO) map[int]*string
}
