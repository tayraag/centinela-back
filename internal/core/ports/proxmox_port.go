package ports

import (
	"context"
	"errors"
)

// ==========================================
// Puerto: Cliente de Proxmox VE
// ==========================================

// Tipos de instancia soportados por Proxmox VE.
const (
	TipoInstanciaQemu = "qemu"
	TipoInstanciaLXC  = "lxc"
)

// ErrInstanciaNoEncontrada indica que el vmid no existe en el cluster de Proxmox.
var ErrInstanciaNoEncontrada = errors.New("instancia no encontrada en Proxmox")

// ErrProxmoxNoDisponible indica que no se pudo completar la operación contra
// la API de Proxmox (red, autenticación o error del lado de Proxmox).
var ErrProxmoxNoDisponible = errors.New("Proxmox VE no disponible")

// InstanciaProxmoxDTO proyecta el estado de una instancia (VM o contenedor)
// leído desde la API de Proxmox.
type InstanciaProxmoxDTO struct {
	Vmid   int    `json:"vmid"`
	Nombre string `json:"nombre"`
	Tipo   string `json:"tipo"` // "qemu" | "lxc"
	Nodo   string `json:"nodo"`
	Estado string `json:"estado"` // "running" | "stopped" | ...
}

// InstanciaListadaDTO es la proyección normalizada que consume el Front en el
// listado del inventario. Nombres de campo y valores fijados por contrato:
// type usa "vm" (no "qemu") para las máquinas virtuales.
type InstanciaListadaDTO struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"` // "vm" | "lxc"
	Node   string `json:"node"`
	Status string `json:"status"`
}

// ProxmoxPort define el contrato hacia la API de Proxmox VE.
// Lo implementa internal/adapters/secondary/proxmox.
type ProxmoxPort interface {
	// ObtenerInstancia devuelve el estado actual de una instancia.
	// Retorna ErrInstanciaNoEncontrada si el vmid no existe en el cluster.
	ObtenerInstancia(ctx context.Context, vmid int) (*InstanciaProxmoxDTO, error)

	// ListarInstancias devuelve el estado actual de todas las VMs y
	// contenedores del cluster, sin ningún filtrado por permisos (eso lo
	// resuelve el handler HTTP según el rol del usuario autenticado).
	ListarInstancias(ctx context.Context) ([]InstanciaProxmoxDTO, error)

	// IniciarInstancia arranca una VM o contenedor. Devuelve el UPID de la
	// tarea asíncrona que crea Proxmox para esta operación.
	IniciarInstancia(ctx context.Context, vmid int) (upid string, err error)

	// DetenerInstancia apaga (forzado) una VM o contenedor. Devuelve el UPID.
	DetenerInstancia(ctx context.Context, vmid int) (upid string, err error)
}
