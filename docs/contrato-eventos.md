# Contrato del canal de eventos en tiempo real (RF-11)

## Qué es el canal

El canal de eventos en tiempo real transporta notificaciones generadas por el
backend hacia los clientes conectados. Este documento define **solo el contrato
de datos** del evento genérico que viajará por ese canal.

El servidor WebSocket, las colas y los workers que publicarán estos eventos
**todavía no existen**. Cuando se implementen, deben emitir exactamente este
esquema y los valores de enum aquí definidos.

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
| recursoId   | string         | string    | Identificador del recurso afectado     |
| mensaje     | string         | string    | Texto legible para el usuario          |
| fechaHora   | time.Time      | RFC3339   | Momento en que se generó el evento     |
| detalles    | map[string]any | object    | Datos extra opcionales (según el tipo) |

- `id` permite deduplicar eventos repetidos del lado del cliente.
- `recursoTipo`, `recursoId` y `detalles` son opcionales: se completan según el
  tipo de evento (ver `ConRecurso` y `ConDetalles` en el código).

## Valores permitidos

### Tipo de evento (`tipo`)

| Constante Go          | Valor                  | Significado                       |
|-----------------------|------------------------|-----------------------------------|
| EventoInstanciaEstado | INSTANCE_STATE_CHANGED | Cambió el estado de una instancia |
| EventoInstanciaCreada | INSTANCE_CREATED       | Se creó una instancia             |
| EventoSaturacion      | RESOURCE_SATURATION    | Un recurso superó su umbral       |
| EventoTareaFinalizada | TASK_FINISHED          | Terminó una tarea asincrónica     |

### Severidad (`severidad`)

| Constante Go      | Valor    | Significado |
|-------------------|----------|-------------|
| SeveridadInfo     | INFO     | Informativo |
| SeveridadWarning  | WARNING  | Advertencia |
| SeveridadCritical | CRITICAL | Crítico     |

### Tipo de recurso (`recursoTipo`)

| Constante Go | Valor | Significado     |
|--------------|-------|-----------------|
| RecursoVM    | VM    | Máquina virtual |
| RecursoLXC   | LXC   | Contenedor LXC  |
| RecursoNodo  | NODE  | Nodo de Proxmox |

### Estado de tarea

No es un campo propio del evento: viaja dentro de `detalles` en los eventos
`TASK_FINISHED`.

| Constante Go   | Valor     | Significado  |
|----------------|-----------|--------------|
| TareaRunning   | RUNNING   | En ejecución |
| TareaCompleted | COMPLETED | Completada   |
| TareaFailed    | FAILED    | Fallida      |

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
    "estado": "COMPLETED"
  }
}
```

### Evento de saturación (`RESOURCE_SATURATION`)

```json
{
  "id": "b6a1c2d3-e4f5-4678-9abc-def012345678",
  "tipo": "RESOURCE_SATURATION",
  "severidad": "CRITICAL",
  "recursoTipo": "NODE",
  "recursoId": "nodo-01",
  "mensaje": "El uso de CPU del nodo superó el 90%",
  "fechaHora": "2026-09-21T14:35:10Z",
  "detalles": {
    "metrica": "cpu",
    "valor": 92.4,
    "umbral": 90
  }
}
```

## Swagger

`swag` no está instalado en esta máquina, así que el esquema todavía no aparece
en Swagger. El struct `RealtimeEvent` ya lleva las anotaciones `@name` y
`@Description`, por lo que al correr `swag init` el esquema se incluirá
automáticamente. No editar `docs/docs.go` ni `swagger.json` a mano.
