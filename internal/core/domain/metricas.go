package domain

import "math"

// Convención de las métricas de CPU y RAM que llegan al front (definida por la PM):
//   - CPU en porcentaje de 0 a 100, con 2 decimales. Proxmox la informa como
//     fracción (0 a 1): se convierte con PorcentajeCPU al salir hacia el front.
//   - CPU y RAM usada en null cuando la instancia no está encendida (o el punto
//     histórico no tiene dato): null significa "apagada", nunca 0.

// PorcentajeCPU convierte la fracción de Proxmox (0 a 1) a porcentaje (0 a 100) con 2 decimales.
func PorcentajeCPU(fraccion float64) float64 {
	return math.Round(fraccion*10000) / 100
}

// PuntoMetricaDTO modela un punto de telemetría histórica obtenido de Proxmox RRDdata.
type PuntoMetricaDTO struct {
	Timestamp     int64    `json:"timestamp"`
	CPUPercent    *float64 `json:"cpuPercent"`   // 0 a 100; null si no hay dato (instancia apagada)
	RAMUsedBytes  *int64   `json:"ramUsedBytes"` // null si no hay dato (instancia apagada)
	RAMTotalBytes int64    `json:"ramTotalBytes"`
	NetInBps      int64    `json:"netInBps"`
	NetOutBps     int64    `json:"netOutBps"`
	DiskReadBps   int64    `json:"diskReadBps"`
	DiskWriteBps  int64    `json:"diskWriteBps"`
}

// MetricasEnVivoDTO proyecta las métricas instantáneas transmitidas por el WebSocket.
type MetricasEnVivoDTO struct {
	Timestamp int64    `json:"timestamp"`
	VMID      int      `json:"vmid"`
	Status    string   `json:"status"`
	CPU       *float64 `json:"cpu"` // 0 a 100; null si la instancia no está encendida
	CPUs      int      `json:"cpus"`
	Mem       *int64   `json:"mem"` // bytes; null si la instancia no está encendida
	MaxMem    int64    `json:"maxmem"`
	Disk      int64    `json:"disk"`
	MaxDisk   int64    `json:"maxdisk"`
	NetIn     int64    `json:"netin"`
	NetOut    int64    `json:"netout"`
	DiskRead  int64    `json:"diskread"`
	DiskWrite int64    `json:"diskwrite"`
	Uptime    int64    `json:"uptime"`
}
