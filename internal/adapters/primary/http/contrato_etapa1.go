package http

// MetricaCPUNode documenta el uso agregado de CPU del nodo.
type MetricaCPUNode struct {
	UsagePercent float64 `json:"usagePercent" binding:"required" minimum:"0" maximum:"100" example:"37.5"`
	Cores        int     `json:"cores" binding:"required" minimum:"1" example:"16"`
}

// MetricaCapacidadNode documenta capacidad y uso de RAM o almacenamiento.
type MetricaCapacidadNode struct {
	UsedGb       float64 `json:"usedGb" binding:"required" minimum:"0" example:"42.25"`
	TotalGb      float64 `json:"totalGb" binding:"required" minimum:"0" example:"128"`
	UsagePercent float64 `json:"usagePercent" binding:"required" minimum:"0" maximum:"100" example:"33.01"`
}

// ResumenEstadoInstancias documenta los totales de un tipo de instancia.
type ResumenEstadoInstancias struct {
	Running int `json:"running" binding:"required" minimum:"0" example:"6"`
	Stopped int `json:"stopped" binding:"required" minimum:"0" example:"2"`
	Paused  int `json:"paused" binding:"required" minimum:"0" example:"1"`
	Total   int `json:"total" binding:"required" minimum:"0" example:"9"`
}

// ResumenInstanciasNode separa los totales de máquinas virtuales y contenedores.
type ResumenInstanciasNode struct {
	VMs ResumenEstadoInstancias `json:"vms" binding:"required"`
	LXC ResumenEstadoInstancias `json:"lxc" binding:"required"`
}

// EstadoNodeResponse es el contrato objetivo del endpoint planificado de estado del nodo.
type EstadoNodeResponse struct {
	CPU              MetricaCPUNode        `json:"cpu" binding:"required"`
	RAM              MetricaCapacidadNode  `json:"ram" binding:"required"`
	Storage          MetricaCapacidadNode  `json:"storage" binding:"required"`
	UptimeSeconds    int64                 `json:"uptimeSeconds" binding:"required" minimum:"0" example:"86400"`
	InstancesSummary ResumenInstanciasNode `json:"instancesSummary" binding:"required"`
	Stale            bool                  `json:"stale" binding:"required" example:"false"`
	FetchedAt        string                `json:"fetchedAt" binding:"required" format:"date-time" example:"2026-10-03T14:30:05Z"`
}

// AccionAceptadaResponse es el cuerpo 202 de las acciones de ciclo de vida aceptadas.
// upid identifica la tarea creada en Proxmox; tareaId identifica la tarea registrada
// por el backend y es el mismo valor que recibe el evento TASK_FINISHED al terminar.
//
// tareaId se omite cuando el registro de la tarea falla. La acción ya se aplicó en
// Proxmox para ese momento, por lo que la respuesta conserva únicamente upid y el
// cliente no puede correlacionar el evento: debe reconciliar por upid.
type AccionAceptadaResponse struct {
	Upid    string  `json:"upid" binding:"required" example:"UPID:pve:0008380E:0131F1BF:6A84EA54:vzstart:110:root@pam:ctid=110:starttime=6934A2E3:"`
	TareaID *string `json:"tareaId,omitempty" example:"3f2504e0-4f89-11d3-9a0c-0305e82c3301"`
}

// InstanciaInventarioResponse documenta cada elemento del inventario operativo.
type InstanciaInventarioResponse struct {
	ID          int     `json:"id" binding:"required" example:"110"`
	Name        string  `json:"name" binding:"required" example:"api-produccion"`
	Type        string  `json:"type" binding:"required" enums:"vm,lxc" example:"vm"`
	Node        string  `json:"node" binding:"required" example:"pve-01"`
	Status      string  `json:"status" binding:"required" example:"running"`
	IP          *string `json:"ip" binding:"required" extensions:"x-nullable" example:"192.0.2.10"`
	CPUUsage    float64 `json:"cpuUsage" binding:"required" minimum:"0" maximum:"1" example:"0.24"`
	RAMUsage    int64   `json:"ramUsage" binding:"required" minimum:"0" example:"2147483648"`
	MaxRAM      int64   `json:"maxRam" binding:"required" minimum:"0" example:"4294967296"`
	NivelAcceso string  `json:"nivelAcceso" binding:"required" enums:"FULL_ACCESS,READ_ONLY" example:"FULL_ACCESS"`
	ActiveTask  *string `json:"activeTask" binding:"required" extensions:"x-nullable" example:"3f2504e0-4f89-11d3-9a0c-0305e82c3301"`
}
