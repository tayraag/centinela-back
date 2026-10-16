package ports

import (
	"context"
	"errors"

	"el-centinela/internal/core/domain"
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

// ErrRecursosInsuficientes indica que la cuota solicitada supera la capacidad
// disponible del nodo o storage. Se traduce a 409 RESOURCE_QUOTA_EXCEEDED.
var ErrRecursosInsuficientes = errors.New("recursos insuficientes para aprovisionar la instancia")

// ErrCoordinacionAprovisionamiento indica que no pudo mantenerse la reserva
// compartida que evita carreras entre solicitudes concurrentes.
var ErrCoordinacionAprovisionamiento = errors.New("no se pudo coordinar la reserva de aprovisionamiento")

// ErrCreacionNoDespachada indica que el cliente falló antes de enviar el POST
// de creación. Permite liberar una reserva con certeza sin confundir ese caso
// con una respuesta perdida después de que Proxmox pudo aceptar la operación.
var ErrCreacionNoDespachada = errors.New("la creación no fue despachada a Proxmox")

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

	// Telemetría en tiempo real, en las unidades de Proxmox (0 cuando la
	// instancia está detenida). Uso interno: hacia el front sale convertida
	// (ver domain.PorcentajeCPU e InstanciaDetalleResponse).
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
	Ip          *string        `json:"ip"`          // dirección IP de la instancia (si está disponible)
	CpuUsage    *float64       `json:"cpuUsage"`    // porcentaje 0 a 100; null si no está encendida
	RamUsage    *int64         `json:"ramUsage"`    // bytes usados; null si no está encendida
	MaxRam      *int64         `json:"maxRam"`      // bytes máximos
	NivelAcceso string         `json:"nivelAcceso"` // "FULL_ACCESS" | "READ_ONLY" (o "FULL_ACCESS" para ADMIN)
	ActiveTask  *ActiveTaskDTO `json:"activeTask"`  // tarea en curso o null
}

// ActiveTaskDTO es la tarea RUNNING de una instancia en el inventario.
type ActiveTaskDTO struct {
	TareaID string `json:"tareaId"`
	Action  string `json:"action"` // START | STOP | SHUTDOWN | REBOOT | DELETE
	Status  string `json:"status"` // RUNNING
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

	// Shutdown realiza un apagado ordenado de la VM o contenedor. Devuelve el UPID.
	Shutdown(ctx context.Context, node string, vmid int, vmType string) (string, error)

	// Reboot reinicia la VM o contenedor de forma genérica con los parámetros exigidos.
	Reboot(ctx context.Context, node string, vmid int, vmType string) (string, error)

	// EliminarInstancia elimina permanentemente una VM o contenedor del cluster.
	// La instancia DEBE estar detenida antes de llamar a este método; si está
	// encendida Proxmox responde con error y se retorna ErrInstanciaOcupada.
	// Devuelve el UPID de la tarea de borrado (qmdestroy / vzdestroy).
	EliminarInstancia(ctx context.Context, node string, vmid int, vmType string) (upid string, err error)

	// EstadoTarea consulta GET /nodes/{node}/tasks/{upid}/status.
	EstadoTarea(ctx context.Context, upid string) (*EstadoTareaDTO, error)

	// ObtenerEstadoNodo consulta GET /nodes/{node}/status (salud física del hipervisor).
	ObtenerEstadoNodo(ctx context.Context, node string) (*NodeStatusDTO, error)

	// ObtenerInterfaces devuelve las interfaces de red de una instancia:
	//   - qemu: GET /nodes/{node}/qemu/{vmid}/agent/network-get-interfaces (QEMU Guest Agent);
	//   - lxc:  GET /nodes/{node}/lxc/{vmid}/interfaces.
	// Un contenedor apagado devuelve una lista vacía sin error (Proxmox responde
	// {"data": null}). Una VM apagada o sin agente devuelve el error de Proxmox.
	ObtenerInterfaces(ctx context.Context, node, tipo string, vmid int) ([]InterfazRed, error)

	// ObtenerStorages consulta GET /nodes/{node}/storage.
	ObtenerStorages(ctx context.Context, node string) ([]StorageDTO, error)

	// ObtenerContenidoStorage consulta GET /nodes/{node}/storage/{storage}/content.
	ObtenerContenidoStorage(ctx context.Context, node, storage string) ([]StorageContentDTO, error)

	// ObtenerMetricasRRD consulta GET /nodes/{node}/{tipo}/{vmid}/rrddata?timeframe={timeframe}.
	ObtenerMetricasRRD(ctx context.Context, nodo, tipo string, vmid int, timeframe string) ([]domain.PuntoMetricaDTO, error)

	// ObtenerEstadoActual consulta GET /nodes/{node}/{tipo}/{vmid}/status/current.
	ObtenerEstadoActual(ctx context.Context, nodo, tipo string, vmid int) (*domain.MetricasEnVivoDTO, error)

	// ObtenerConfig consulta GET /nodes/{node}/{tipo}/{vmid}/config. Con enUso
	// = true (current=1) devuelve la configuración que la instancia está usando;
	// con false, la que tendrá después de aplicar los cambios pendientes.
	ObtenerConfig(ctx context.Context, nodo, tipo string, vmid int, enUso bool) (*ConfigInstanciaDTO, error)

	// ActualizarConfig hace PUT /nodes/{node}/{tipo}/{vmid}/config con los
	// cores y la memoria (MiB) dados; 0 = no se cambia. digest (el de
	// ObtenerConfig) hace que Proxmox rechace el cambio si la configuración se
	// modificó en el medio.
	ActualizarConfig(ctx context.Context, nodo, tipo string, vmid int, cores, memoriaMB int, digest string) error
}

// ConfigInstanciaDTO es la CPU y RAM configuradas de una instancia.
type ConfigInstanciaDTO struct {
	Cores     int    // vCPU (cores); en un LXC sin límite de CPU vale 0
	MemoriaMB int    // RAM en MiB
	Digest    string // huella del archivo de configuración (control de concurrencia)
}

// StorageDTO representa un almacenamiento tal como lo informa Proxmox en /nodes/{node}/storage.
type StorageDTO struct {
	Storage string `json:"storage"`
	Type    string `json:"type"`
	Total   int64  `json:"total"`
	Used    int64  `json:"used"`
	Avail   int64  `json:"avail"`
	Active  int    `json:"active"`
	Content string `json:"content"`
	Shared  int    `json:"shared"`
	Enabled int    `json:"enabled"`
}

// StorageContentDTO representa un elemento de contenido en un storage (ISO, plantilla, etc.).
type StorageContentDTO struct {
	Volid   string `json:"volid"`
	Content string `json:"content"`
	Format  string `json:"format"`
	Size    int64  `json:"size"`
	CTime   int64  `json:"ctime,omitempty"`
}

// InterfazRed es una interfaz de red de una instancia, normalizada: el formato
// de qemu (prefix numérico, tipo ipv4/ipv6) y el de lxc (prefix string, tipo
// inet/inet6) quedan iguales.
type InterfazRed struct {
	Nombre      string
	MAC         string
	Direcciones []DireccionIP
}

// DireccionIP es una dirección asignada a una interfaz.
type DireccionIP struct {
	IP      string
	Prefijo int
	Version int // 4 | 6
}

// NodeStatusDTO es el estado físico del nodo tal como lo informa Proxmox, sin
// normalizar (bytes y fracciones). La normalización a GB y porcentajes la hace
// el servicio de estado del nodo.
type NodeStatusDTO struct {
	CPU             float64 // uso de CPU: fracción [0,1] sobre CPUs
	CPUs            int     // hilos lógicos (cpuinfo.cpus): sobre estos se calcula CPU
	PhysicalCores   int     // núcleos físicos totales (cpuinfo.cores ya suma todos los sockets)
	MemTotal        int64   // bytes
	MemUsada        int64   // bytes
	DiscoTotal      int64   // bytes del rootfs del nodo
	DiscoUsado      int64   // bytes del rootfs del nodo
	DiscoDisponible int64   // bytes asignables del rootfs (rootfs.avail)
	UptimeSegs      int64
}

// EstadoTareaDTO es el estado de una tarea asíncrona de Proxmox.
type EstadoTareaDTO struct {
	Terminada  bool   // Proxmox reporta status "stopped"
	ExitStatus string // "OK" si salió bien; si no, el mensaje de error de Proxmox
}

// CrearInstanciaProxmoxInput contiene la configuración normalizada que el
// adaptador traduce a los parámetros form-urlencoded de qemu o lxc.
type CrearInstanciaProxmoxInput struct {
	Tipo        string // qemu | lxc
	Nodo        string
	Nombre      string
	Descripcion string
	Password    string // obligatorio solo para LXC
	Cores       int
	MemoriaMB   int
	StorageID   string
	DiscoGB     int
	TemplateID  string
	Bridge      string
	VLAN        *int
	Firewall    bool
	IPv4Tipo    string // dhcp | static
	IPv4Address string
	IPv4Mask    string
	IPv4Gateway string
}

// ResultadoAprovisionamiento vincula el VMID reservado con el UPID que debe
// registrar el worker. Accion contiene qmcreate o vzcreate.
type ResultadoAprovisionamiento struct {
	VMID      int
	UPID      string
	Accion    string
	ReservaID string
}

// AprovisionamientoPort separa las escrituras de creación del contrato de
// consulta/operación de instancias existente. El cliente real implementa ambos.
type AprovisionamientoPort interface {
	AprovisionarInstancia(ctx context.Context, input CrearInstanciaProxmoxInput) (*ResultadoAprovisionamiento, error)
}
