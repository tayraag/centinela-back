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
	ID          int                 `json:"id" binding:"required" example:"110"`
	Name        string              `json:"name" binding:"required" example:"api-produccion"`
	Type        string              `json:"type" binding:"required" enums:"vm,lxc" example:"vm"`
	Node        string              `json:"node" binding:"required" example:"pve-01"`
	Status      string              `json:"status" binding:"required" example:"running"`
	IP          *string             `json:"ip" binding:"required" extensions:"x-nullable" example:"192.0.2.10"`
	CPUUsage    float64             `json:"cpuUsage" binding:"required" minimum:"0" maximum:"1" example:"0.24"`
	RAMUsage    int64               `json:"ramUsage" binding:"required" minimum:"0" example:"2147483648"`
	MaxRAM      int64               `json:"maxRam" binding:"required" minimum:"0" example:"4294967296"`
	NivelAcceso string              `json:"nivelAcceso" binding:"required" enums:"FULL_ACCESS,READ_ONLY" example:"FULL_ACCESS"`
	ActiveTask  *ActiveTaskResponse `json:"activeTask" binding:"required" extensions:"x-nullable"`
}

// ActiveTaskResponse documenta la tarea en curso de una instancia del inventario.
// activeTask vale null cuando la instancia no tiene ninguna tarea RUNNING.
// tareaId es el mismo valor que devolvió el 202 de la acción y que llega en el
// TASK_FINISHED; action es la acción en mayúsculas, igual que detalles.accion.
type ActiveTaskResponse struct {
	TareaID string `json:"tareaId" binding:"required" example:"3f2504e0-4f89-11d3-9a0c-0305e82c3301"`
	Action  string `json:"action" binding:"required" enums:"START,STOP,SHUTDOWN,REBOOT,DELETE" example:"START"`
	Status  string `json:"status" binding:"required" enums:"RUNNING" example:"RUNNING"`
}

// ==========================================
// T03 — Canal de eventos en tiempo real (SSE)
//
// Los tipos de esta sección son documentación pura: reflejan el sobre que el
// servidor ya escribe en `data:` (ports.RealtimeEvent en internal/core/ports)
// y no intervienen en el camino de ejecución. Se declaran acá, y no en ports,
// para que el namespace de Swagger sea `http.*` igual que el resto del
// contrato HTTP de esta etapa.
// ==========================================

// SSEDetallesEvento documenta el objeto `detalles` de un evento del stream.
//
// El servidor lo serializa como `map[string]any`, así que su forma depende del
// `tipo` del evento y ninguno de sus campos puede declararse obligatorio. Hoy
// el único tipo que se publica es `TASK_FINISHED`; sus dos formas concretas
// están en TaskSuccess y TaskFailed. En `TASK_FINISHED` el servidor manda
// siempre las seis claves: `exitstatus`, `motivo` y `error` valen null cuando
// no aplican.
type SSEDetallesEvento struct {
	TareaID    string  `json:"tareaId,omitempty" example:"3f2504e0-4f89-11d3-9a0c-0305e82c3301"`
	Accion     string  `json:"accion,omitempty" enums:"START,STOP,SHUTDOWN,REBOOT,DELETE" example:"START"`
	Estado     string  `json:"estado,omitempty" enums:"RUNNING,COMPLETED,FAILED" example:"COMPLETED"`
	ExitStatus *string `json:"exitstatus,omitempty" extensions:"x-nullable" example:"OK"`
	Motivo     *string `json:"motivo,omitempty" enums:"PROXMOX_ERROR,TIMEOUT" extensions:"x-nullable"`
	Error      *string `json:"error,omitempty" extensions:"x-nullable" example:"CT 201 not running"`
}

// SSEEventPayload es el JSON de cada línea `data:` del stream `GET /api/events`.
//
// El mismo `id` viaja también en la línea `id:` del frame SSE, para que el
// cliente pueda deduplicar. `detalles` se omite cuando el evento no trae datos
// extra.
type SSEEventPayload struct {
	ID          string             `json:"id" binding:"required" example:"75d0322d-1d76-4dbf-ad0d-2571c458aa63"`
	Tipo        string             `json:"tipo" binding:"required" enums:"INSTANCE_STATE_CHANGED,INSTANCE_CREATED,RESOURCE_SATURATION,TASK_FINISHED" example:"TASK_FINISHED"`
	Severidad   string             `json:"severidad" binding:"required" enums:"INFO,WARNING,CRITICAL" example:"INFO"`
	RecursoTipo string             `json:"recursoTipo" binding:"required" enums:"VM,LXC,NODE" example:"VM"`
	RecursoID   string             `json:"recursoId" binding:"required" example:"110"`
	Mensaje     string             `json:"mensaje" binding:"required" example:"La tarea de encendido finalizó correctamente"`
	FechaHora   string             `json:"fechaHora" binding:"required" format:"date-time" example:"2026-09-30T11:24:03-03:00"`
	Detalles    *SSEDetallesEvento `json:"detalles,omitempty"`
}

// TaskSuccess es la forma concreta de `detalles` en un `TASK_FINISHED` que
// terminó en `COMPLETED`: severidad `INFO`, `exitstatus` "OK" y `motivo` y
// `error` en null. Las seis claves viajan siempre.
type TaskSuccess struct {
	TareaID    string  `json:"tareaId" binding:"required" example:"3f2504e0-4f89-11d3-9a0c-0305e82c3301"`
	Accion     string  `json:"accion" binding:"required" enums:"START,STOP,SHUTDOWN,REBOOT,DELETE" example:"START"`
	Estado     string  `json:"estado" binding:"required" enums:"COMPLETED" example:"COMPLETED"`
	ExitStatus string  `json:"exitstatus" binding:"required" enums:"OK" example:"OK"`
	Motivo     *string `json:"motivo" binding:"required" extensions:"x-nullable"`
	Error      *string `json:"error" binding:"required" extensions:"x-nullable"`
}

// TaskFailed es la forma concreta de `detalles` en un `TASK_FINISHED` que
// terminó en `FAILED`: severidad `WARNING` y las mismas seis claves.
//
//   - `motivo` PROXMOX_ERROR: Proxmox terminó la tarea con error; `exitstatus`
//     y `error` traen su texto.
//   - `motivo` TIMEOUT: Proxmox no la dio por terminada en 10 minutos (3 si
//     se recuperó al reiniciar el backend);
//     `exitstatus` es null y `error` lo explica.
type TaskFailed struct {
	TareaID    string  `json:"tareaId" binding:"required" example:"3f2504e0-4f89-11d3-9a0c-0305e82c3301"`
	Accion     string  `json:"accion" binding:"required" enums:"START,STOP,SHUTDOWN,REBOOT,DELETE" example:"STOP"`
	Estado     string  `json:"estado" binding:"required" enums:"FAILED" example:"FAILED"`
	ExitStatus *string `json:"exitstatus" binding:"required" extensions:"x-nullable" example:"CT 201 not running"`
	Motivo     string  `json:"motivo" binding:"required" enums:"PROXMOX_ERROR,TIMEOUT" example:"PROXMOX_ERROR"`
	Error      *string `json:"error" binding:"required" extensions:"x-nullable" example:"CT 201 not running"`
}

// SSECierrePayload es el JSON del frame `event: cierre` con el que el backend
// avisa que va a cortar el stream. El cliente debe cerrar el `EventSource` y no
// reconectar con el mismo ticket: ya fue consumido.
type SSECierrePayload struct {
	Motivo string `json:"motivo" binding:"required" enums:"LOGOUT,SESSIONS_REVOKED,USER_INACTIVE" example:"LOGOUT"`
}

// SSETicketResponse es el cuerpo 200 de `POST /api/events/ticket`.
//
// El ticket es un UUID de un solo uso, válido 30 s, que se consume con `GETDEL`
// al abrir el stream. El JWT nunca viaja en la URL porque `EventSource` no puede
// mandar el header `Authorization`.
type SSETicketResponse struct {
	Ticket string `json:"ticket" binding:"required" example:"3cb3438a-7dec-4116-b54a-369501674c92"`
}

// SSETiposPayload indexa, solo a efectos de documentación, las tres formas
// concretas que un cliente puede recibir del stream `GET /api/events`:
//
//   - `taskSuccess`: el `detalles` de un `TASK_FINISHED` que terminó en `COMPLETED`.
//   - `taskFailed`: el mismo evento terminado en `FAILED`, con `motivo` y `error`.
//   - `cierre`: el cuerpo del frame `event: cierre`.
//
// No es un cuerpo que se pueda recibir: es el índice que permite que swag
// emita los tres esquemas, que de otro modo no aparecen en Swagger.
type SSETiposPayload struct {
	TaskSuccess TaskSuccess      `json:"taskSuccess" binding:"required"`
	TaskFailed  TaskFailed       `json:"taskFailed" binding:"required"`
	Cierre      SSECierrePayload `json:"cierre" binding:"required"`
}

// SSEDetallesTareaFinDocumentacion es el bloque de anotaciones que existe solo
// para documentación: ninguna ruta del servidor lo sirve.
//
// OpenAPI 2.0 no tiene forma nativa de declarar un stream SSE ni un `oneOf`, y
// swag solo emite los esquemas que puede alcanzar desde una anotación de ruta.
// Por eso este bloque, sin registrar ningún handler, publica en Swagger las dos
// formas concretas de `detalles` y el payload del frame de corte, a través del
// índice `http.SSETiposPayload`.
//
// La ruta es deliberadamente distinta de `GET /api/events`: swag sobrescribe la
// operación cuando dos handlers declaran el mismo path y método, así que
// duplicar `/api/events` borraría la documentación real del stream. El estado
// `documentation-only` deja explícito que esta entrada no es invocable.
//
// Esta función nunca se llama: no registra rutas ni altera el comportamiento del
// servidor.
//
// @Summary      Formas concretas del payload del stream SSE (solo documentación)
// @Description  Índice de los esquemas que el stream `GET /api/events` puede emitir: `http.TaskSuccess` (evento `TASK_FINISHED` con `estado` `COMPLETED`), `http.TaskFailed` (el mismo evento con `estado` `FAILED`, `motivo` PROXMOX_ERROR o TIMEOUT y `error`) y `http.SSECierrePayload` (el cuerpo del frame `event: cierre`). Este es el `detalles` de los eventos, no el sobre completo, que es `http.SSEEventPayload`. No es una ruta del servidor y no se puede invocar: el stream real y su framing se documentan en `GET /api/events`.
// @Tags         Eventos en tiempo real
// @Produce      text/event-stream
// @Success      200 {object} SSETiposPayload "índice de las formas concretas del stream"
// @x-implementation-status "documentation-only"
// @Router       /events/contrato-sse [get]
func SSEDetallesTareaFinDocumentacion() {}
