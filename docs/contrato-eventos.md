# Contrato del canal de eventos en tiempo real (RF-11)

## Qué es el canal

El canal de eventos en tiempo real transporta notificaciones generadas por el
backend hacia los clientes conectados. Este documento define **solo el contrato
de datos** del evento genérico que viajará por ese canal.

El canal que los transporta es SSE (`GET /api/events`) sobre Redis Pub/Sub, y
está documentado en [eventos-tiempo-real.md](eventos-tiempo-real.md). Hoy se
publican `TASK_FINISHED` y las alertas `ALERT_TRIGGERED` / `ALERT_RESOLVED` del
evaluador de umbrales; los demás tipos quedan definidos para cuando se
implementen sus emisores, que deben usar exactamente este esquema.

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
| recursoId   | string         | string o null | Referencia pública (`inst_...`) de la VM o LXC; `null` en HOST |
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
  (ver `ConRecurso` y `ConDetalles` en el código). `recursoId` viaja siempre:
  en un evento del HOST vale `null`. `detalles` se omite cuando no aplica.

## Valores permitidos

### Tipo de evento (`tipo`)

| Constante Go          | Valor                  | Significado                       |
|-----------------------|------------------------|-----------------------------------|
| EventoInstanciaEstado | INSTANCE_STATE_CHANGED | Cambió el estado de una instancia |
| EventoInstanciaCreada | INSTANCE_CREATED       | Se creó una instancia             |
| EventoSaturacion      | RESOURCE_SATURATION    | Un recurso superó su umbral       |
| EventoTareaFinalizada | TASK_FINISHED          | Terminó una tarea asincrónica     |
| EventoAlerta          | ALERT_TRIGGERED        | Se disparó una alerta (p. ej. del host) |
| EventoAlertaResuelta  | ALERT_RESOLVED         | Una alerta volvió a la normalidad       |

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
`tareaId`, `accion` (`START`, `STOP`, `SHUTDOWN`, `REBOOT`, `DELETE`, `CREATE`, `SNAPSHOT`, `ROLLBACK_SNAPSHOT` o `DELETE_SNAPSHOT`
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

### Alertas (`ALERT_TRIGGERED` y `ALERT_RESOLVED`)

Las emite el **evaluador de umbrales**, un worker que cada `ALERT_POLL_INTERVAL`
(30 s por defecto) revisa el host y las instancias:

| Recurso   | Métrica              | Se dispara con      | Se resuelve con |
|-----------|----------------------|---------------------|-----------------|
| Host      | RAM (`ram`)          | ≥ 85%               | < 80%           |
| Host      | CPU (`cpu`)          | ≥ 90%               | < 80%           |
| Host      | Disco raíz (`disk`)  | ≥ 90%               | < 80%           |
| Instancia encendida | CPU (`cpu`) | ≥ 95%           | < 80%           |
| Instancia encendida | Memoria (`mem`) | ≥ 95%       | < 80%           |
| Instancia | Caída (`down`)       | estaba encendida y se apagó o desapareció sin una tarea de Centinela | vuelve a encender |

**Histéresis y deduplicación.** Un valor sobre el umbral tiene que repetirse
**dos ciclos seguidos** para disparar la alerta: un pico de un solo ciclo no
emite nada ni se audita. Disparada, la alerta no se repite mientras el valor
no baje del umbral de recuperación (80%): oscilar entre 84% y 86% no genera
eventos nuevos. Al bajar de 80% llega `ALERT_RESOLVED`. La caída no necesita
dos ciclos (un cambio de estado no es un pico) y "sin una tarea de Centinela"
significa sin una tarea de esa instancia en curso ni creada en los últimos 10
minutos (un stop, un rollback o un borrado ordenados desde El Centinela no
alertan).

- `ALERT_TRIGGERED`: severidad `CRITICAL`. Se publica en el bus en el mismo
  ciclo en que se confirma, así que llega por SSE en menos de un segundo.
- `ALERT_RESOLVED`: severidad `INFO`, con el mismo `alertId` de la alerta.
- `detalles`: `alertId`, `type` (`host_saturation`, `resource_saturation` o
  `instance_down`), `metrica`, `valorActual`, `umbralCritico` (el de
  activación), `duracionPersistencia` (solo en la alerta: cuánto duró la
  saturación antes de alertar, por ejemplo `"60s"`), `umbralRecuperacion`
  (solo en la resolución: 80) y `acknowledged: false`. Los valores y umbrales
  no van en `instance_down`.
- Las del host traen `recursoId: null` y solo las reciben los usuarios **ADMIN**;
  las de una instancia traen su referencia pública en `recursoId` y las reciben
  el ADMIN y los OPERATOR con permiso sobre ella.

**Correo.** Cada `ALERT_TRIGGERED` (todas son `CRITICAL`) se avisa también por
correo, en segundo plano para no demorar el ciclo: a los ADMIN activos y, si
la alerta es de una instancia, también a los OPERATOR activos con
`FULL_ACCESS` sobre ella. Asunto `[ALERTA CRÍTICA] Saturación de Recursos en
HOST proxmox` (o `... en VM srv-database`; en una caída, `Caída de Instancia
en ...`); el cuerpo (texto plano y HTML) trae el recurso, la métrica, el valor
contra el umbral, la hora en UTC y de Argentina y una recomendación. Con
`EMAIL_PROVIDER=mock` el correo queda en el log del backend; en producción
sale por SMTP con STARTTLS (puerto 587) o TLS (465). La resolución no manda
correo.

**Auditoría.** `ALERT_TRIGGERED` y `ALERT_RESOLVED` quedan en la tabla
`auditoria` con resultado `SUCCESS`, sin usuario, `resource_type` `HOST`,
`VM` o `LXC` y `resource_id` el VMID o `host`. En `detalles` van la métrica,
los valores, el mensaje, el `timestamp` (UTC) y, en la alerta, los
`destinatarios` del correo. Las alertas nuevas se guardan con alcance
explícito en `auditoria.organizacion_id`, una fila por organización autorizada
por la política del correo y con sus propios destinatarios. Aparecen en listado
y exportación administrativos como `[sistema]`, sin usuario. Los registros
históricos sin actor ni alcance permanecen excluidos; no se exponen globalmente.


```json
{
  "id": "5b0c8e9a-2f4d-4c1e-9b7a-1d2e3f4a5b6c",
  "tipo": "ALERT_TRIGGERED",
  "severidad": "CRITICAL",
  "recursoTipo": "HOST",
  "recursoId": null,
  "mensaje": "Saturación crítica: Uso de Memoria RAM en el host al 89.2% (límite 85%)",
  "fechaHora": "2026-10-27T12:45:00Z",
  "detalles": {
    "alertId": "0192e4a1-7c9b-7123-b123-000000000001",
    "type": "host_saturation",
    "metrica": "ram",
    "valorActual": 89.2,
    "umbralCritico": 85,
    "duracionPersistencia": "60s",
    "acknowledged": false
  }
}
```

```json
{
  "id": "8d1e2f3a-4b5c-4d6e-8f7a-9b0c1d2e3f4a",
  "tipo": "ALERT_RESOLVED",
  "severidad": "INFO",
  "recursoTipo": "VM",
  "recursoId": "inst_3Hk9aQ2mXv7LpR4sT1bN8c",
  "mensaje": "El uso de CPU de la instancia srv-database volvió a la normalidad (42.0%).",
  "fechaHora": "2026-10-27T10:20:00-03:00",
  "detalles": {
    "alertId": "0192e4a1-7c9b-7123-b123-000000000002",
    "type": "resource_saturation",
    "metrica": "cpu",
    "valorActual": 42,
    "umbralCritico": 95,
    "umbralRecuperacion": 80,
    "acknowledged": false
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

