# Contrato HTTP de etapa 1: estado e inventario

## Estado actual y objetivo

La lectura de inventario `GET /api/instances` está **operativa**. Devuelve las
VM y los contenedores visibles para cualquier usuario autenticado con rol
`ADMIN` u `OPERATOR`, incluida su telemetría actual.

La lectura consolidada `GET /api/node/status` está **planificada**. La definición
de este documento y Swagger fija el contrato objetivo, pero la ruta todavía no
está registrada y hoy responde `404 NOT_FOUND`. El frontend no debe invocarla
como una capacidad disponible hasta que una etapa posterior anuncie su
implementación.

Ambas operaciones usarán `Authorization: Bearer <accessToken>`. Un `ADMIN` puede
consultar todo el inventario; un `OPERATOR` recibe solo las instancias que tiene
asignadas. El estado consolidado del nodo no agrega un filtro por instancia y
estará disponible para ambos roles autenticados.

## Estado consolidado del nodo — planificado

`GET /api/node/status` tendrá una respuesta `200 application/json` con:

- `cpu`: `usagePercent` entre 0 y 100 y `cores` como cantidad de núcleos.
- `ram` y `storage`: `usedGb`, `totalGb` y `usagePercent` entre 0 y 100.
- `uptimeSeconds`: tiempo activo del nodo, entero no negativo en segundos.
- `instancesSummary`: grupos `vms` y `lxc`; cada uno contiene `running`,
  `stopped`, `paused` y `total` como enteros no negativos.
- `stale`: `true` cuando se entrega la última lectura almacenada porque no pudo
  renovarse; `false` cuando la lectura está vigente.
- `fetchedAt`: instante de obtención de la lectura en formato RFC3339.

Todos los campos son obligatorios y no admiten `null`.

```json
{
  "cpu": { "usagePercent": 37.5, "cores": 16 },
  "ram": { "usedGb": 42.25, "totalGb": 128, "usagePercent": 33.01 },
  "storage": { "usedGb": 380, "totalGb": 1024, "usagePercent": 37.11 },
  "uptimeSeconds": 86400,
  "instancesSummary": {
    "vms": { "running": 6, "stopped": 2, "paused": 1, "total": 9 },
    "lxc": { "running": 3, "stopped": 1, "paused": 0, "total": 4 }
  },
  "stale": false,
  "fetchedAt": "2026-10-03T14:30:05Z"
}
```

## Inventario de instancias — operativo

`GET /api/instances` devuelve un array. Cada elemento contiene siempre:

- `id`, `name`, `type`, `node` y `status`.
- `ip`: cadena o `null`. La implementación actual todavía no obtiene la IP, por
  lo que hoy el valor es `null`.
- `cpuUsage`: fracción entre 0 y 1; vale 0 cuando la instancia está detenida.
- `ramUsage` y `maxRam`: bytes usados y bytes máximos asignados.
- `nivelAcceso`: `FULL_ACCESS` o `READ_ONLY`; para `ADMIN` siempre se informa
  `FULL_ACCESS`.
- `activeTask`: identificador de la tarea en curso o `null`.

`type` usa `vm` o `lxc`. La respuesta no omite telemetría, `nivelAcceso`, `ip` ni
`activeTask`; estos dos últimos expresan ausencia con `null`.

```json
[
  {
    "id": 110,
    "name": "api-produccion",
    "type": "vm",
    "node": "pve-01",
    "status": "running",
    "ip": null,
    "cpuUsage": 0.24,
    "ramUsage": 2147483648,
    "maxRam": 4294967296,
    "nivelAcceso": "FULL_ACCESS",
    "activeTask": null
  }
]
```

La operación lee Proxmox en vivo. Si la consulta de tareas activas falla, el
inventario sigue respondiendo y `activeTask` queda en `null`; si falla Proxmox o
la consulta de permisos, se usa el sobre de error `{ "errorCode", "message" }`
ya vigente.

## Acciones aceptadas — operativo

Las acciones de energía sobre una instancia son `POST` y responden `202 Accepted`.
El verbo `POST` es el que el servidor tiene registrado; no existe `PUT` ni `PATCH`
para estas rutas.

| Ruta                   | Método | Estado      | Respuesta |
|------------------------|--------|-------------|-----------|
| `:vmid/start`          | POST   | operativo   | `202`     |
| `:vmid/stop`           | POST   | operativo   | `202`     |
| `:vmid/status/:action` | POST   | operativo   | `202`     |
| `:vmid/pause`          | POST   | planificada | `404`     |

**Detalle:** las tres rutas operativas son `POST /api/instances/:vmid/start`,
`POST /api/instances/:vmid/stop` y `POST /api/instances/:vmid/status/:action`;
las tres responden `202` con `AccionAceptadaResponse`. La cuarta,
`POST /api/instances/:vmid/pause`, hoy responde `404 NOT_FOUND`.

El cuerpo `202` es el esquema `http.AccionAceptadaResponse`:

- `upid`: obligatorio. Es el UPID de la tarea que Proxmox crea para la acción.
- `tareaId`: **opcional**. Es el identificador de la tarea registrada por el
  backend y es el mismo valor que llega en el evento `TASK_FINISHED`.

```json
{ "upid": "UPID:pve:0008380E:0131F1BF:6A84EA54:vzstart:110:root@pam:ctid=110:starttime=6934A2E3:", "tareaId": "3f2504e0-4f89-11d3-9a0c-0305e82c3301" }
```

La acción es asíncrona: el `202` confirma que Proxmox aceptó la orden, **no** que
la instancia ya cambió de estado. El frontend debe esperar el evento
`TASK_FINISHED` o releer el estado de la instancia.

`POST /api/instances/:vmid/status/:action` acepta `start`, `stop`, `shutdown` y
`reboot`. Cualquier otra acción responde `400 INVALID_ACTION`.

### Pause: diferencia con lo solicitado

La acción `Pause` **no está implementada**. No existe la ruta
`POST /api/instances/:vmid/pause`, no está en la lista de acciones válidas del
endpoint genérico y el adaptador de Proxmox no expone una operación de pausa.
Hoy esa ruta responde `404 NOT_FOUND` y `pause` en el endpoint genérico responde
`400 INVALID_ACTION`. El contrato queda documentado en Swagger con
`x-implementation-status: planned`; el frontend no debe ofrecer la acción.

## Catálogo de errores

Todo error `4xx` y `5xx` usa el mismo sobre, ya vigente en el servidor y definido
en `docs/estandar_http.md`:

- `errorCode`: código interno en mayúsculas.
- `message`: descripción legible del error.

```json
{ "errorCode": "INSTANCE_BUSY", "message": "La instancia se encuentra ejecutando otra tarea. Aguarde a que finalice." }
```

Estos son los ocho pares error/status que las acciones de energía pueden devolver:

| HTTP | `errorCode` representativo | Causa                                     |
|------|----------------------------|-------------------------------------------|
| 400  | `INVALID_VMID`             | El `vmid` de la ruta no es un entero      |
| 401  | `MISSING_TOKEN`            | Token ausente, inválido o sesión revocada |
| 403  | `INSTANCE_ACCESS_DENIED`   | Falta de permisos o nivel insuficiente    |
| 404  | `INSTANCE_NOT_FOUND`       | La instancia no existe en Proxmox         |
| 409  | `INSTANCE_BUSY`            | La instancia ejecuta otra tarea           |
| 500  | `INTERNAL_ERROR`           | Permisos o error inesperado de Proxmox    |
| 502  | `PROXMOX_UNAVAILABLE`      | Proxmox caído, sin red o token rechazado  |
| 504  | `PROXMOX_TIMEOUT`          | Proxmox no respondió a tiempo             |

**Códigos adicionales que emite el servidor para el mismo estado:**

- `401`: `MISSING_TOKEN`, `INVALID_TOKEN`, `TOKEN_REVOKED`.
- `403`: `INSTANCE_ACCESS_DENIED`, `INSTANCE_PROTECTED`, `WRONG_TOKEN_TYPE`,
  `2FA_REQUIRED`, `PASSWORD_CHANGE_REQUIRED`, `NO_ROLE`, `NO_USER`,
  `INVALID_USER_ID`.
- `400` en `status/{action}` agrega `INVALID_ACTION`.
- `403` en el borrado agrega `INSUFFICIENT_PERMISSIONS` e `INVALID_ROLE`.

`INSTANCE_PROTECTED` solo aparece en las rutas que pasan por
`RejectProtectedInstance`: `stop`, `status/{action}` y el borrado. El arranque
`start` **no** registra ese middleware, por lo que no puede emitir
`INSTANCE_PROTECTED`.

### Ambigüedad del timeout

`502 PROXMOX_UNAVAILABLE` y `504 PROXMOX_TIMEOUT` se distinguen a propósito:

- `PROXMOX_UNAVAILABLE` significa que la orden **nunca llegó** a Proxmox.
  Reintentar es seguro.
- `PROXMOX_TIMEOUT` significa que Proxmox **no respondió a tiempo**. La orden
  **puede haberse aplicado**: la instancia podría estar encendiéndose o
  apagándose. El frontend debe advertir al usuario que verifique el estado antes
  de reintentar, y no debe duplicar la acción a ciegas.

`409 INSTANCE_BUSY` no es una falla de infraestructura: Proxmox está en línea y
la acción se puede volver a pedir cuando llegue el `TASK_FINISHED` anterior.

## Diferencias entre lo solicitado y el comportamiento observado

Estas diferencias se verificaron leyendo el servidor; ninguna se corrigió porque
esta etapa es solo documental.

| Solicitado                     | Observado                         |
|--------------------------------|-----------------------------------|
| Campo `error` de tipo `string` | No existe en el sobre real        |
| Acción `Pause`                 | No implementada; `404 NOT_FOUND`  |
| `TOKEN_MISSING` y afines       | El servidor emite `MISSING_TOKEN` |
| `202` con `tareaId` siempre    | El borrado devuelve `204`         |

**Detalle:** el sobre real es `{ "errorCode", "message" }`, no `{ "error" }`. Los
códigos de autenticación reales son `MISSING_TOKEN`, `INVALID_TOKEN` y
`TOKEN_REVOKED`; no existe un código propio para expiración. El borrado responde
`204` sin cuerpo y `tareaId` **se omite** cuando el registro de la tarea falla.

Sobre el último punto, dos consecuencias prácticas:

1. **Borrado sin seguimiento.** El borrado es síncrono y devuelve `204` sin
   cuerpo: no devuelve `upid` ni `tareaId` y no genera evento de tarea. Solo
   puede seguirse por auditoría.
2. **Seguimiento opcional.** Si el registro de la tarea falla, la acción ya se
   aplicó en Proxmox y la respuesta `202` conserva únicamente `upid`. El cliente
   no debe tratar la ausencia de `tareaId` como error ni esperar un
   `TASK_FINISHED` correlacionable; en ese caso debe reconciliar por `upid`.

## Alcance de esta etapa

Esta etapa fija las lecturas de estado e inventario junto con las acciones
aceptadas y su catálogo completo de errores. El detalle de eventos SSE
corresponde a T03 y conserva por ahora los contratos existentes en
`contrato-eventos.md` y `eventos-tiempo-real.md`.
