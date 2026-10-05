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

// ErrInstanciaOcupada indica que Proxmox rechazó la operación porque la
// instancia está bloqueada por otra tarea en curso ("can't lock file ..." o
// "VM 110 is locked (...)"). Proxmox está en línea: no es un error de
// infraestructura, así que NO envuelve ErrProxmoxNoDisponible.
var ErrInstanciaOcupada = errors.New("la instancia está ocupada con otra tarea")

// ErrProxmoxNoDisponible indica que no se pudo completar la operación contra
// la API de Proxmox (red, autenticación o error del lado de Proxmox).
var ErrProxmoxNoDisponible = errors.New("Proxmox VE no disponible")

// ErrProxmoxCredenciales indica que Proxmox rechazó el API Token (401/403) o
// que el token no está configurado. Siempre viaja envuelto junto con
// ErrProxmoxNoDisponible: hacia el cliente HTTP se responde el mismo 502, pero
// permite distinguir en los logs un token mal configurado de un Proxmox caído.
var ErrProxmoxCredenciales = errors.New("credenciales de Proxmox rechazadas o no configuradas")

// ErrProxmoxTimeout indica que Proxmox no respondió a tiempo. Viaja envuelto
// junto con ErrProxmoxNoDisponible; hacia el cliente HTTP se responde 504.
var ErrProxmoxTimeout = errors.New("Proxmox VE no respondió a tiempo")

// InstanciaProxmoxDTO proyecta el estado de una instancia (VM o contenedor)
// leído desde la API de Proxmox. Los campos de telemetría (Cpu, Mem, MaxMem)
// provienen directamente de cluster/resources y solo tienen valor cuando la
// instancia está en running; en stopped Proxmox devuelve 0.
type InstanciaProxmoxDTO struct {
	Vmid   int    `json:"vmid"`
	Nombre string `json:"nombre"`
	Tipo   string `json:"tipo"` // "qemu" | "lxc"
	Nodo   string `json:"nodo"`
	Estado string `json:"estado"` // "running" | "stopped" | ...

	// Telemetría en tiempo real (0 cuando la instancia está detenida)
	Cpu    float64 `json:"cpu"`    // uso de CPU: fracción [0,1]
	Mem    int64   `json:"mem"`    // RAM usada (bytes)
	MaxMem int64   `json:"maxMem"` // RAM máxima asignada (bytes)
	MaxCpu int     `json:"maxCpu"` // cantidad de vCPUs asignadas
}

// InstanciaListadaDTO es la proyección normalizada que consume el Front en el
// listado del inventario. Nombres de campo y valores fijados por contrato:
// type usa "vm" (no "qemu") para las máquinas virtuales.
type InstanciaListadaDTO struct {
	// Campos base (contrato original — no romper)
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"` // "vm" | "lxc"
	Node   string `json:"node"`
	Status string `json:"status"`

	// Campos extendidos de telemetría (retrocompatibles: omitempty)
	Ip          *string     `json:"ip"`          // dirección IP de la instancia (si está disponible)
	CpuUsage    *float64    `json:"cpuUsage"`    // fracción [0,1]
	RamUsage    *int64      `json:"ramUsage"`    // bytes usados
	MaxRam      *int64      `json:"maxRam"`      // bytes máximos
	NivelAcceso string      `json:"nivelAcceso"` // "FULL_ACCESS" | "READ_ONLY" (o "FULL_ACCESS" para ADMIN)
	ActiveTask  interface{} `json:"activeTask"`  // tareaId en curso o null
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

	// ReiniciarInstancia reinicia (reboot) una VM o contenedor. Devuelve el UPID.
	ReiniciarInstancia(ctx context.Context, vmid int) (upid string, err error)

	// Shutdown realiza un apagado ordenado de la VM o contenedor. Devuelve el UPID.
	Shutdown(ctx context.Context, node string, vmid int, vmType string) (string, error)

	// Reboot reinicia la VM o contenedor de forma genérica con los parámetros exigidos.
	Reboot(ctx context.Context, node string, vmid int, vmType string) (string, error)

	// EliminarInstancia elimina permanentemente una VM o contenedor del cluster.
	// La instancia DEBE estar detenida antes de llamar a este método; si está
	// encendida Proxmox responde con error y se retorna ErrInstanciaOcupada.
	EliminarInstancia(ctx context.Context, vmid int) error

	// EstadoTarea consulta GET /nodes/{node}/tasks/{upid}/status.
	EstadoTarea(ctx context.Context, upid string) (*EstadoTareaDTO, error)

	// ObtenerEstadoNodo consulta GET /nodes/{node}/status (salud física del hipervisor).
	ObtenerEstadoNodo(ctx context.Context, node string) (*NodeStatusDTO, error)
}

// NodeStatusDTO es el estado físico del nodo tal como lo informa Proxmox, sin
// normalizar (bytes y fracciones). La normalización a GB y porcentajes la hace
// el servicio de estado del nodo.
type NodeStatusDTO struct {
	CPU        float64 // uso de CPU: fracción [0,1] sobre CPUs
	CPUs       int     // hilos lógicos (cpuinfo.cpus): sobre estos se calcula CPU
	MemTotal   int64   // bytes
	MemUsada   int64   // bytes
	DiscoTotal int64   // bytes del rootfs del nodo
	DiscoUsado int64   // bytes del rootfs del nodo
	UptimeSegs int64
}

// EstadoTareaDTO es el estado de una tarea asíncrona de Proxmox.
type EstadoTareaDTO struct {
	Terminada  bool   // Proxmox reporta status "stopped"
	ExitStatus string // "OK" si salió bien; si no, el mensaje de error de Proxmox
}
