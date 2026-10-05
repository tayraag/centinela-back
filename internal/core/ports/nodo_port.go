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

// EstadoNodo es la lectura normalizada del nodo. Es lo que se guarda en Redis
// (node:status:current y node:status:last_known).
type EstadoNodo struct {
	CPU              MetricaCPU        `json:"cpu"`
	RAM              MetricaCapacidad  `json:"ram"`
	Storage          MetricaCapacidad  `json:"storage"`
	UptimeSeconds    int64             `json:"uptimeSeconds"`
	InstancesSummary ResumenInstancias `json:"instancesSummary"`
	FetchedAt        time.Time         `json:"fetchedAt"` // cuándo se leyó de Proxmox
}

// NodoService obtiene el estado del nodo con caché en dos niveles.
type NodoService interface {
	// ObtenerEstado devuelve la lectura vigente (stale = false) o, si Proxmox no
	// responde, la última conocida (stale = true). Si tampoco hay una lectura
	// previa devuelve el error de Proxmox (ErrProxmoxNoDisponible / ErrProxmoxTimeout).
	ObtenerEstado(ctx context.Context) (estado *EstadoNodo, stale bool, err error)
}
