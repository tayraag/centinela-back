package ports

import (
	"context"
	"time"
)

// ==========================================
// Estado consolidado del nodo (GET /api/node/status)
// ==========================================

// MetricaCPU es el uso de CPU del nodo, normalizado.
type MetricaCPU struct {
	UsagePercent float64 `json:"usagePercent"` // 0–100, 2 decimales
	Cores        int     `json:"cores"`        // hilos lógicos (base del porcentaje)
}

// MetricaCapacidad es el uso de RAM o almacenamiento, normalizado.
type MetricaCapacidad struct {
	UsedGb       float64 `json:"usedGb"`       // GB (1024³ bytes), 2 decimales
	TotalGb      float64 `json:"totalGb"`      // GB (1024³ bytes), 2 decimales
	UsagePercent float64 `json:"usagePercent"` // 0–100, 2 decimales
}

// ResumenEstado cuenta las instancias de un tipo según su estado.
type ResumenEstado struct {
	Running int `json:"running"`
	Stopped int `json:"stopped"`
	Paused  int `json:"paused"`
	Total   int `json:"total"`
}

// ResumenInstancias separa VMs y contenedores.
type ResumenInstancias struct {
	VMs ResumenEstado `json:"vms"`
	LXC ResumenEstado `json:"lxc"`
}

// CuotaAsignable contiene los recursos que el asistente puede ofrecer para
// nuevas instancias. FreeRamMb excluye siempre el headroom del hipervisor.
type CuotaAsignable struct {
	MaxCPUCores     int   `json:"maxCpuCores"`
	FreeRamMB       int64 `json:"freeRamMb"`
	FreeStorageGB   int64 `json:"freeStorageGb"`
	HeadroomPercent int   `json:"headroomPercent"`
}

// EstadoNodo es la lectura normalizada del nodo. Es lo que se guarda en Redis
// (node:status:current y node:status:last_known).
type EstadoNodo struct {
	CPU              MetricaCPU        `json:"cpu"`
	RAM              MetricaCapacidad  `json:"ram"`
	Storage          MetricaCapacidad  `json:"storage"`
	Allocatable      CuotaAsignable    `json:"allocatable"`
	UptimeSeconds    int64             `json:"uptimeSeconds"`
	InstancesSummary ResumenInstancias `json:"instancesSummary"`
	FetchedAt        time.Time         `json:"fetchedAt"` // cuándo se leyó de Proxmox
	// Instancias es la foto del inventario con la que se armó InstancesSummary.
	// Queda en la caché para contar solo las instancias de un OPERATOR sin
	// volver a consultar Proxmox; nunca se publica tal cual.
	Instancias []InstanciaDelNodo `json:"instancias,omitempty"`
}

// InstanciaDelNodo es lo mínimo de cada instancia para armar un resumen.
type InstanciaDelNodo struct {
	Vmid   int    `json:"vmid"`
	Tipo   string `json:"tipo"`   // qemu | lxc
	Estado string `json:"estado"` // running | stopped | paused
}

// ResumirInstancias cuenta las instancias por tipo y estado. Con incluir nil
// cuenta todas; si no, solo las que devuelven true (las de un OPERATOR).
func ResumirInstancias(instancias []InstanciaDelNodo, incluir func(vmid int) bool) ResumenInstancias {
	var resumen ResumenInstancias
	for _, inst := range instancias {
		if incluir != nil && !incluir(inst.Vmid) {
			continue
		}
		grupo := &resumen.VMs
		if inst.Tipo == TipoInstanciaLXC {
			grupo = &resumen.LXC
		}
		switch inst.Estado {
		case "running":
			grupo.Running++
		case "stopped":
			grupo.Stopped++
		case "paused":
			grupo.Paused++
		}
		grupo.Total++
	}
	return resumen
}

// NodoService obtiene el estado del nodo con caché en dos niveles.
type NodoService interface {
	// ObtenerEstado devuelve la lectura vigente (stale = false) o, si Proxmox no
	// responde, la última conocida (stale = true). Si tampoco hay una lectura
	// previa devuelve el error de Proxmox (ErrProxmoxNoDisponible / ErrProxmoxTimeout).
	ObtenerEstado(ctx context.Context) (estado *EstadoNodo, stale bool, err error)

	// ObtenerHistorial devuelve la serie histórica del host para el gráfico del
	// Dashboard, ya normalizada a los tres porcentajes que muestra el panel. El
	// gráfico la carga al entrar, así que no arranca de cero: la fuente es el RRD
	// de Proxmox, cacheado en Redis para no golpear la infraestructura en cada
	// visita.
	ObtenerHistorial(ctx context.Context) ([]PuntoHistorialNodo, error)
}

// ==========================================
// Histórico del nodo (GET /api/node/history)
// ==========================================

// MuestraNodoRRD es una muestra cruda del histórico del host, tal como sale de
// Proxmox en /nodes/{node}/rrddata. La normaliza el servicio.
type MuestraNodoRRD struct {
	Timestamp  int64   // segundos UNIX
	CPU        float64 // fracción [0,1] sobre los hilos lógicos del host
	MemUsada   int64   // bytes
	MemTotal   int64   // bytes
	DiscoUsado int64   // bytes del rootfs del nodo
	DiscoTotal int64   // bytes del rootfs del nodo
}

// PuntoHistorialNodo es una muestra del histórico ya normalizada: los tres
// porcentajes que dibuja el gráfico del Dashboard.
//
// RAM y almacenamiento se omiten cuando el punto del RRD no trae su total
// (maxmem/maxdisk): publicar 0% hacía que el gráfico mostrara una caída al piso.
type PuntoHistorialNodo struct {
	Timestamp      int64    `json:"timestamp"`                // segundos UNIX
	CPUPercent     float64  `json:"cpuPercent"`               // 0–100, 2 decimales
	RAMPercent     *float64 `json:"ramPercent,omitempty"`     // 0–100, 2 decimales; ausente = sin dato
	StoragePercent *float64 `json:"storagePercent,omitempty"` // 0–100, 2 decimales; ausente = sin dato
}
