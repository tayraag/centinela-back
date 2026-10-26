# Contrato del canal de eventos en tiempo real (RF-11)

## Qué es el canal

El canal de eventos en tiempo real transporta notificaciones generadas por el
backend hacia los clientes conectados. Este documento define **solo el contrato
de datos** del evento genérico que viajará por ese canal.

El canal que los transporta es SSE (`GET /api/events`) sobre Redis Pub/Sub, y
está documentado en [eventos-tiempo-real.md](eventos-tiempo-real.md). Hoy se
publica `TASK_FINISHED`; el canal ya distribuye también `ALERT_TRIGGERED` (alertas
del host) y los demás tipos, que quedan definidos para cuando se implementen sus
emisores, que deben usar exactamente este esquema.

- Definición del contrato (Go): `internal/core/ports/event_port.go` (struct `RealtimeEvent`).
- Definición del contrato (TypeScript): `frontend/centinela/src/types/notifications.ts` (interface `RealtimeEvent`).

Convención de nombres: los **campos** van en español camelCase y los **valores de
enum** en inglés MAYÚSCULA con guión bajo.

## Campos del evento

| Campo       | Tipo Go        | Tipo JSON | Significado                            |
|-------------|----------------|-----------|----------------------------------------|
| id          | uuid.UUID      | string    | Identificador único del evento         |
| tipo        | string         | string    | Tipo de evento (ver enums)             |
| severidad   | string         | string    | Nivel de severidad (ver enums)         |
| recursoTipo | string         | string    | Tipo de recurso afectado (ver enums)   |
| recursoId   | string         | string    | Referencia pública (`inst_...`) de la VM o LXC; se omite en HOST |
| mensaje     | string         | string    | Texto legible para el usuario          |
| fechaHora   | time.Time      | RFC3339   | Momento en que se generó el evento     |
| detalles    | map[string]any | object    | Datos extra opcionales (según el tipo) |

- `id` permite deduplicar eventos repetidos del lado del cliente.
- `recursoId` nunca es el VMID de Proxmox: dentro del backend el evento lleva el
  VMID (`"110"`) y el handler del stream lo traduce a la referencia pública
  antes de mandarlo, la misma `id` que devuelve `GET /api/instances`. Si un
  texto de `detalles` (el error de Proxmox) nombra el VMID, también se reemplaza.
- Para todos los roles, los textos del evento (`mensaje` y los de `detalles`,
  como el error de Proxmox en `exitstatus` y `error`) llegan saneados: se
  quitan rutas del sistema, archivos y módulos Perl, números de línea, UPID y
  las líneas de traza, y queda la frase útil (`unable to create VM ...:
  storage '...' does not exist`). Si no queda nada útil, llega el texto
  canónico `"No se pudo completar la operación en el hipervisor. Contacte al administrador."`. Los campos de depuración (`upid`, `rawOutput`,
  `stack`, `socket`, ...) nunca viajan; la tarea se identifica solo con
  `tareaId`. El texto original queda en el log y en la auditoría.
- Para quien no es ADMIN, los nombres reales de nodos y storages que aparezcan
  en `mensaje` o en los textos de `detalles` se reemplazan por sus alias
  (`nodo-principal`, `Almacenamiento Local Rápido`, ...). Ver
  `docs/contrato-etapa1.md`.
- `recursoTipo`, `recursoId` y `detalles` se completan según el tipo de evento
  (ver `ConRecurso` y `ConDetalles` en el código). `recursoId` y `detalles` no
  viajan cuando no aplican: un evento del HOST no trae `recursoId`.

## Valores permitidos

### Tipo de evento (`tipo`)

| Constante Go          | Valor                  | Significado                       |
|-----------------------|------------------------|-----------------------------------|
| EventoInstanciaEstado | INSTANCE_STATE_CHANGED | Cambió el estado de una instancia |
| EventoInstanciaCreada | INSTANCE_CREATED       | Se creó una instancia             |
| EventoSaturacion      | RESOURCE_SATURATION    | Un recurso superó su umbral       |
| EventoTareaFinalizada | TASK_FINISHED          | Terminó una tarea asincrónica     |
| EventoAlerta          | ALERT_TRIGGERED        | Se disparó una alerta (p. ej. del host) |

### Severidad (`severidad`)

| Constante Go      | Valor    | Significado |
|-------------------|----------|-------------|
| SeveridadInfo     | INFO     | Informativo |
| SeveridadWarning  | WARNING  | Advertencia |
| SeveridadCritical | CRITICAL | Crítico     |

### Tipo de recurso (`recursoTipo`)

| Constante Go | Valor | Significado                               | Lo reciben                                      |
|--------------|-------|-------------------------------------------|-------------------------------------------------|
| RecursoVM    | VM    | Máquina virtual                           | ADMIN y OPERATOR con permiso sobre esa instancia |
| RecursoLXC   | LXC   | Contenedor LXC                            | ADMIN y OPERATOR con permiso sobre esa instancia |
| RecursoHost  | HOST  | Servidor físico (el nodo de Proxmox)      | Solo ADMIN; al OPERATOR se le descarta en silencio |

`HOST` reemplaza al valor `NODE` de versiones anteriores del contrato (nunca se
llegó a emitir un evento `NODE`).

### Estado de tarea

No es un campo propio del evento: viaja dentro de `detalles` en los eventos
`TASK_FINISHED`.

| Constante Go   | Valor     | Significado  |
|----------------|-----------|--------------|
| TareaRunning   | RUNNING   | En ejecución |
| TareaCompleted | COMPLETED | Completada   |
| TareaFailed    | FAILED    | Fallida      |

### Detalles de `TASK_FINISHED`

`detalles` trae siempre las seis claves; las que no aplican van en `null`:
`tareaId`, `accion` (`START`, `STOP`, `SHUTDOWN`, `REBOOT`, `DELETE`, `CREATE` o `SNAPSHOT`
para aprovisionamiento), `estado`
(`COMPLETED` o `FAILED`), `exitstatus` (lo que informó Proxmox, o `null`),
`motivo` y `error`. `motivo` solo tiene valor si `estado` es `FAILED`:

| Constante Go       | Valor         | Significado                                              |
|--------------------|---------------|----------------------------------------------------------|
| MotivoProxmoxError | PROXMOX_ERROR | Proxmox terminó la tarea con error (`exitstatus` ≠ `OK`) |
| MotivoTimeout      | TIMEOUT       | No terminó en 10 min (3 al recuperarla tras un reinicio) |

## Ejemplos

### Evento de fin de tarea (`TASK_FINISHED`)

```json
{
  "id": "0f8fad5b-d9cb-469f-a165-70867728950e",
  "tipo": "TASK_FINISHED",
  "severidad": "INFO",
  "recursoTipo": "VM",
  "recursoId": "100",
  "mensaje": "La tarea de encendido finalizó correctamente",
  "fechaHora": "2026-09-21T14:30:05Z",
  "detalles": {
    "tareaId": "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
    "accion": "START",
    "estado": "COMPLETED",
    "exitstatus": "OK",
    "motivo": null,
    "error": null
  }
}
```

Si falla, `severidad` es `WARNING` y `detalles` queda, por ejemplo:

```json
{ "tareaId": "3f2504e0-4f89-11d3-9a0c-0305e82c3302", "accion": "STOP", "estado": "FAILED", "exitstatus": "CT 201 not running", "motivo": "PROXMOX_ERROR", "error": "CT 201 not running" }
```

### Alerta del host (`ALERT_TRIGGERED`)

Una alerta de infraestructura del servidor físico: no refiere a una instancia,
así que **no trae `recursoId`**, y solo la reciben los usuarios **ADMIN**. La
forma de `detalles` depende de la alerta; por ejemplo, un umbral superado:

```json
{
  "id": "5b0c8e9a-2f4d-4c1e-9b7a-1d2e3f4a5b6c",
  "tipo": "ALERT_TRIGGERED",
  "severidad": "CRITICAL",
  "recursoTipo": "HOST",
  "mensaje": "El uso de CPU del host superó el 90%",
  "fechaHora": "2026-10-16T10:15:00-03:00",
  "detalles": {
    "metrica": "cpu",
    "valor": 92.4,
    "umbral": 90
  }
}
```

O una caída del almacenamiento del host:

```json
{
  "id": "8d1e2f3a-4b5c-4d6e-8f7a-9b0c1d2e3f4a",
  "tipo": "ALERT_TRIGGERED",
  "severidad": "WARNING",
  "recursoTipo": "HOST",
  "mensaje": "El storage local-lvm superó el 85% de uso",
  "fechaHora": "2026-10-16T10:20:00-03:00",
  "detalles": {
    "metrica": "storage",
    "storage": "local-lvm",
    "valor": 86.1,
    "umbral": 85
  }
}
```

### Evento de saturación (`RESOURCE_SATURATION`)

```json
{
  "id": "b6a1c2d3-e4f5-4678-9abc-def012345678",
  "tipo": "RESOURCE_SATURATION",
  "severidad": "CRITICAL",
  "recursoTipo": "HOST",
  "mensaje": "El uso de CPU del host superó el 90%",
  "fechaHora": "2026-09-21T14:35:10Z",
  "detalles": {
    "metrica": "cpu",
    "valor": 92.4,
    "umbral": 90
  }
}
```

## Swagger / OpenAPI

Los esquemas y rutas del canal SSE se encuentran completamente documentados y disponibles en la Swagger UI (`/swagger/index.html`):
- `POST /api/events/ticket`: Emisión de ticket de un solo uso (30 s) protegido por JWT.
- `GET /api/events`: Stream SSE autenticado por ticket query param, emitiendo `http.SSEEventPayload` con el sobre del evento.
- Modelos `http.TaskSuccess` y `http.TaskFailed` representando la forma concreta del campo `detalles` para el evento `TASK_FINISHED` (con sus 6 claves garantizadas).

