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
