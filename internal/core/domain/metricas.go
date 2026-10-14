package domain

// PuntoMetricaDTO modela un punto de telemetría histórica obtenido de Proxmox RRDdata.
type PuntoMetricaDTO struct {
	Timestamp     int64   `json:"timestamp"`
	CPUPercent    float64 `json:"cpuPercent"`
	RAMUsedBytes  int64   `json:"ramUsedBytes"`
	RAMTotalBytes int64   `json:"ramTotalBytes"`
	NetInBps      int64   `json:"netInBps"`
	NetOutBps     int64   `json:"netOutBps"`
	DiskReadBps   int64   `json:"diskReadBps"`
	DiskWriteBps  int64   `json:"diskWriteBps"`
}

// MetricasEnVivoDTO proyecta las métricas instantáneas transmitidas por el WebSocket.
type MetricasEnVivoDTO struct {
	Timestamp int64   `json:"timestamp"`
	VMID      int     `json:"vmid"`
	Status    string  `json:"status"`
	CPU       float64 `json:"cpu"`
	CPUs      int     `json:"cpus"`
	Mem       int64   `json:"mem"`
	MaxMem    int64   `json:"maxmem"`
	Disk      int64   `json:"disk"`
	MaxDisk   int64   `json:"maxdisk"`
	NetIn     int64   `json:"netin"`
	NetOut    int64   `json:"netout"`
	DiskRead  int64   `json:"diskread"`
	DiskWrite int64   `json:"diskwrite"`
	Uptime    int64   `json:"uptime"`
}

