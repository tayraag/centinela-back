# Contrato HTTP de etapa 1: estado e inventario

## Estado actual y objetivo

La lectura de inventario `GET /api/instances` está **operativa**. Devuelve las
VM y los contenedores visibles para cualquier usuario autenticado con rol
`ADMIN` u `OPERATOR`, incluida su telemetría actual.

La lectura consolidada `GET /api/node/status` también está **operativa**: lee la
salud física del hipervisor y la cachea en Redis.

Ambas operaciones usan `Authorization: Bearer <accessToken>`. Un `ADMIN` puede
consultar todo el inventario; un `OPERATOR` recibe solo las instancias que tiene
asignadas. El estado consolidado del nodo no agrega un filtro por instancia y
está disponible para ambos roles autenticados.

## Estado consolidado del nodo — operativo

**Control de acceso (D1):** Endpoint accesible para cualquier usuario autenticado (`Authorization: Bearer <accessToken>`), habilitado tanto para el rol ADMIN como para el rol OPERATOR. No requiere permisos específicos sobre instancias.

Lo puede leer cualquier usuario autenticado, ADMIN u OPERATOR. No pasa por el
chequeo de permisos por instancia porque describe el hipervisor y no una VM
concreta. Un OPERATOR ve los totales de `instancesSummary` de todo el nodo, no
solo los de sus instancias asignadas.

`GET /api/node/status` responde `200 application/json` con:

- `cpu`: `usagePercent` entre 0 y 100 y `cores` como cantidad de núcleos.
- `ram` y `storage`: `usedGb`, `totalGb` y `usagePercent` entre 0 y 100.
- `allocatable`: cuotas seguras para el asistente de creación:
  - `maxCpuCores`: total de núcleos físicos del host (`cpuinfo.cores`, que ya
    incluye todos los sockets); se diferencia de `cpu.cores`, que informa los
    hilos lógicos;
  - `freeRamMb`: RAM libre asignable en MiB, después de descontar el uso actual
    y reservar estrictamente el 15% de la RAM física para Proxmox;
  - `freeStorageGb`: espacio asignable del disco raíz (`rootfs.avail`) en GiB;
  - `headroomPercent`: porcentaje reservado, actualmente `15`.
- `uptimeSeconds`: tiempo activo del nodo, entero no negativo en segundos.
- `instancesSummary`: grupos `vms` y `lxc`; cada uno contiene `running`,
  `stopped`, `paused` y `total` como enteros no negativos.
- `stale`: `true` cuando se entrega la última lectura almacenada porque no pudo
  renovarse; `false` cuando la lectura está vigente.
- `fetchedAt`: instante de obtención de la lectura en formato RFC3339.

Todos los campos son obligatorios y no admiten `null`. RAM y almacenamiento se
expresan en GB de 1024³ bytes con 2 decimales; `storage` es el disco raíz del
nodo. `cores` son los hilos lógicos del nodo, sobre los que Proxmox calcula
`cpu.usagePercent`.

**Caché y lecturas degradadas.** La lectura se guarda en Redis con dos claves:
`node:status:current` (TTL de 10 s) y `node:status:last_known` (sin TTL).

- Mientras `node:status:current` está vigente se responde sin consultar
  Proxmox, con `stale: false`.
- Al vencer se consulta Proxmox; si responde, se actualizan ambas claves y se
  devuelve la lectura nueva con `stale: false`.
- Si Proxmox no responde y existe `node:status:last_known`, se devuelve esa
  lectura con `stale: true`; `fetchedAt` indica cuándo se obtuvo, para que el
  frontend muestre su antigüedad.
- Solo si Proxmox no responde y no hay ninguna lectura previa, se responde
  `502 PROXMOX_UNAVAILABLE` o `504 PROXMOX_TIMEOUT`.

```json
{
  "cpu": { "usagePercent": 37.5, "cores": 16 },
  "ram": { "usedGb": 42.25, "totalGb": 128, "usagePercent": 33.01 },
  "storage": { "usedGb": 380, "totalGb": 1024, "usagePercent": 37.11 },
  "allocatable": {
    "maxCpuCores": 16,
    "freeRamMb": 68147,
    "freeStorageGb": 644,
    "headroomPercent": 15
  },
  "uptimeSeconds": 86400,
  "instancesSummary": {
    "vms": { "running": 6, "stopped": 2, "paused": 1, "total": 9 },
    "lxc": { "running": 3, "stopped": 1, "paused": 0, "total": 4 }
  },
  "stale": false,
  "fetchedAt": "2026-10-03T14:30:05Z"
}
```

## Identificadores de instancia: referencia pública opaca

La API **nunca expone el VMID de Proxmox** (100, 101, ...): con números
consecutivos se puede deducir cuántas instancias hay y probar rutas de
instancias ajenas (IDOR y enumeración). En su lugar, cada instancia tiene una
**referencia pública** con la forma `inst_` + 22 caracteres alfanuméricos, por
ejemplo `inst_3Hk9aQ2mXv7LpR4sT1bN8c`.

- Es **estable**: la misma instancia tiene siempre la misma referencia, así que
  el front puede usarla como clave (caché, rutas del navegador, selección).
- Es **opaca**: no se puede deducir el VMID ni el orden de creación, y no se
  puede fabricar una válida. Se cifra con el secreto `INSTANCE_ID_SALT` (mínimo
  32 caracteres) del servidor; sin esa variable la API no arranca. Cambiar el
  secreto cambia todas las referencias.
- Es la `id` del inventario, la `:instanceRef` de todas las rutas
  `/api/instances/:instanceRef/...` y del WebSocket, el `recursoId` de los
  eventos SSE de VM y LXC y el `instanciaId`/`resourceId` de la auditoría.
- **Los campos que se llamaban `vmid` conservan el nombre**, pero ahora son un
  string con la referencia: el detalle, el `202` de creación, los mensajes del
  WebSocket, los permisos de usuario (`permisos[].vmid` e
  `instanciasPermitidas`) y los `vmid` dentro de `detalles` de la auditoría.
- Una referencia alterada, inventada, de otro servidor o un VMID numérico en la
  ruta responde `404 RESOURCE_NOT_FOUND`, **exactamente igual** que una
  instancia que no existe en Proxmox: el error no dice por qué falló.

```json
{ "errorCode": "RESOURCE_NOT_FOUND", "message": "La instancia solicitada no existe." }
```

## Nombres de nodos y storages: alias para quien no es ADMIN

Para no revelar la infraestructura interna (hostnames de Proxmox, nombres y
tipos de pools de almacenamiento), un usuario que **no es ADMIN** ve alias
neutros en lugar de los nombres reales:

- `node` del inventario y `nodo` del detalle: el alias del nodo, por defecto
  `nodo-principal` para el nodo configurado (`PROXMOX_NODE`).
- Los textos de los eventos SSE (por ejemplo el error de Proxmox en
  `detalles.error`): los nombres reales de nodos y storages se reemplazan por
  sus alias (`local-lvm` → `Almacenamiento Local Rápido`, `local` →
  `Almacenamiento Estándar`).
- Un nodo o storage que no está en el catálogo recibe un alias genérico estable
  (`nodo-xxxxxxxx`, `almacenamiento-xxxxxxxx`) que no permite deducir el nombre.

El ADMIN sigue viendo los nombres reales en todas partes (inventario, detalle,
`GET /api/node/storages`, `GET /api/node/templates`). `POST /api/instances`
acepta tanto el nombre real como el alias en `nodo`, `storageId` y el storage
de `templateOso`, y los traduce antes de llamar a Proxmox. Los alias se
configuran con `TOPOLOGY_NODE_ALIASES` y `TOPOLOGY_STORAGE_ALIASES`
(`real=alias,real=alias`); son opcionales.

## Inventario de instancias — operativo

`GET /api/instances` devuelve un array. Cada elemento contiene siempre:

- `id` (la referencia pública de la instancia, string), `name`, `type`, `node`
  (alias del nodo si el usuario no es ADMIN) y `status`.
- `ip`: cadena o `null`. Se obtiene en vivo: en las VM, con el QEMU Guest
  Agent; en los contenedores, con sus interfaces. Se informa la primera IPv4
  (sin contar loopback) o, si no hay, la primera IPv6 global. Vale `null` si la
  instancia está apagada, no tiene agente o red, o no respondió a tiempo (2 s por
  instancia y 2,2 s para todo el listado): nunca provoca un error.
- `cpuUsage`: porcentaje de 0 a 100, con 2 decimales.
- `ramUsage`: bytes usados.
- `maxRam`: bytes máximos asignados; se informa también con la instancia apagada.
- Con la instancia apagada (`status` distinto de `running`), `cpuUsage` y
  `ramUsage` valen `null`, nunca 0: para el front, `null` significa apagada.
- `nivelAcceso`: `FULL_ACCESS` o `READ_ONLY`; para `ADMIN` siempre se informa
  `FULL_ACCESS`.
- `activeTask`: objeto `{ tareaId, action, status }` con la tarea en curso de la
  instancia, o `null` si no tiene ninguna. Esquema `http.ActiveTaskResponse`:
  - `tareaId`: el mismo valor que devolvió el `202` de la acción y que llega en
    el `TASK_FINISHED`.
  - `action`: `START`, `STOP`, `SHUTDOWN`, `REBOOT` o `DELETE`, en mayúsculas
    como `detalles.accion` del evento.
  - `status`: siempre `RUNNING`; cuando la tarea termina, `activeTask` vuelve a
    `null`.

`type` usa `vm` o `lxc`. La respuesta no omite telemetría, `nivelAcceso`, `ip` ni
`activeTask`; `ip`, `activeTask`, `cpuUsage` y `ramUsage` expresan ausencia con
`null`.

**Convención de CPU y RAM en toda la API** (definida por la PM): la CPU viaja
siempre en porcentaje de 0 a 100, y CPU y RAM usada valen `null` con la instancia
apagada. Vale para el inventario, el detalle (`GET /api/instances/:instanceRef`, campos
`cpu` y `mem`), el WebSocket de métricas (`cpu` y `mem`) y el estado del nodo
(`cpu.usagePercent`).

```json
[
  {
    "id": "inst_3Hk9aQ2mXv7LpR4sT1bN8c",
    "name": "api-produccion",
    "type": "vm",
    "node": "pve-01",
    "status": "running",
    "ip": null,
    "cpuUsage": 24.5,
    "ramUsage": 2147483648,
    "maxRam": 4294967296,
    "nivelAcceso": "FULL_ACCESS",
    "activeTask": null
  },
  {
    "id": "inst_0Zq4Wd8nKe2YtB6uJm1xPa",
    "name": "db-pruebas",
    "type": "lxc",
    "node": "pve-01",
    "status": "stopped",
    "ip": null,
    "cpuUsage": null,
    "ramUsage": null,
    "maxRam": 1073741824,
    "nivelAcceso": "FULL_ACCESS",
    "activeTask": {
      "tareaId": "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
      "action": "START",
      "status": "RUNNING"
    }
  }
]
```

La operación lee Proxmox en vivo. Si la consulta de tareas activas falla, el
inventario sigue respondiendo y `activeTask` queda en `null`; si falla Proxmox o
la consulta de permisos, se usa el sobre de error `{ "errorCode", "message" }`
ya vigente.

## Aprovisionamiento — operativo, solo ADMIN

`POST /api/instances` crea de forma asíncrona una VM o un LXC. Solo lo puede
invocar un `ADMIN`; un `OPERATOR` recibe `403 INSUFFICIENT_PERMISSIONS`.

El contrato RF-07 es plano; `nodo` es opcional y, si se omite, usa
`PROXMOX_NODE`. `red.modo` acepta `dhcp` o `static`. Una IP estática puede
incluir el prefijo CIDR en `ip` o enviarlo en `mascara` (por defecto `/24`).
Para no cortar la migración del frontend también se acepta temporalmente el
payload anidado anterior del wizard: `type`, `node`, `name`, `cpu.cores`,
`ram.memoryMb`, `disk.storageId/sizeGb`,
`network.bridge/vlan/firewall/ipv4` y `os.templateId`. En ese formato, LXC
requiere `password`. Las integraciones nuevas deben usar el contrato plano.

```json
{
  "tipo": "vm",
  "nombre": "srv-prod-01",
  "cores": 2,
  "ramMb": 2048,
  "discoGb": 20,
  "storageId": "local-lvm",
  "templateOso": "debian-12.iso",
  "red": { "modo": "dhcp" }
}
```

Antes de llamar a Proxmox, `CuotasService` toma el lock distribuido
`lock:instance:provisioning` (`SET NX`, TTL 30 s), valida la capacidad
disponible de CPU lógica, RAM y disco y registra una reserva temporal. Si otra
creación posee el lock, espera y reintenta durante un máximo de 2 s. El lock se
libera mediante comparación atómica de su UUID para no borrar el de otro
request si el anterior venció.

La RAM asignable descuenta el mayor valor entre el consumo actual y la RAM ya
configurada en las instancias, reserva además el 15% de la RAM física para
Proxmox y descuenta las solicitudes aceptadas aún no consolidadas. La CPU usa
los procesadores lógicos de `cpuinfo.cpus`; el disco usa `avail` del storage
seleccionado. Las solicitudes concurrentes descuentan sus reservas para no
aprobar dos veces el mismo remanente. Si la capacidad no alcanza responde
`409 RESOURCE_QUOTA_EXCEEDED`; si no puede mantener la coordinación compartida,
responde `503 PROVISIONING_COORDINATION_UNAVAILABLE` sin crear la instancia.
La validación consulta primero la foto compartida
`aprovisionamiento:capacidad:{node}` en Redis; ante ausencia o vencimiento lee
Proxmox y la conserva 10 segundos. Una orden aceptada invalida esa clave y
`node:status:current`; el worker repite la invalidación al cerrar la tarea para
que la siguiente lectura refleje el estado definitivo. No se vacía Redis
completo, porque también contiene sesiones y tickets SSE.

Después de validar, el backend consulta `GET /cluster/nextid` y envía la orden
a `/nodes/{node}/qemu` o `/nodes/{node}/lxc`. Solo responde `202` después de
insertar el UPID con estado `RUNNING` en `tareas_asincronas` y su auditoría
inicial dentro de la misma transacción:

```json
{
  "vmid": "inst_5Rv1Lc9hGs3NwF7kDy2Tqe",
  "tareaId": "0192a8b3-4f89-7e23-9a0c-0305e82c3301"
}
```

`vmid` conserva su nombre pero ahora es un string: la referencia pública de la
instancia nueva (la misma `id` que tendrá en el inventario). El número que
asignó Proxmox no se devuelve.

`tareaId` es la referencia pública opaca administrada por Centinela. El UPID
real se conserva en PostgreSQL para que el worker consulte Proxmox y correlacione
la auditoría, pero no se incluye en respuestas HTTP, listados administrativos ni
exportaciones.

Si falla ese registro luego de que Proxmox aceptó la orden, devuelve
`500 TASK_TRACKING_UNAVAILABLE` y advierte que no se debe reintentar sin
verificar el estado. Al terminar, el worker libera la reserva y publica
`TASK_FINISHED` con `accion: "CREATE"` y la referencia de la instancia en
`recursoId`. Un resultado
`OK` o `WARNINGS` asigna atómicamente `FULL_ACCESS` al usuario creador antes de
publicar el evento.

## Acciones aceptadas — operativo

Las acciones de energía sobre una instancia son `POST` y responden `202 Accepted`.
El verbo `POST` es el que el servidor tiene registrado; no existe `PUT` ni `PATCH`
para estas rutas. El borrado usa `DELETE` y responde igual, `202 Accepted`.

| Ruta                          | Método | Rol            | Estado      | Respuesta |
|-------------------------------|--------|----------------|-------------|-----------|
| `:instanceRef/start`          | POST   | ADMIN/OPERATOR | operativo   | `202`     |
| `:instanceRef/stop`           | POST   | ADMIN/OPERATOR | operativo   | `202`     |
| `:instanceRef/status/:action` | POST   | ADMIN/OPERATOR | operativo   | `202`     |
| `:instanceRef`                | DELETE | solo ADMIN     | operativo   | `202`     |
| `:instanceRef/pause`          | POST   | ADMIN/OPERATOR | planificada | `404`     |

Un OPERATOR necesita `FULL_ACCESS` sobre la instancia para las acciones de
energía.

**Detalle:** las tres rutas operativas son `POST /api/instances/:instanceRef/start`,
`POST /api/instances/:instanceRef/stop` y `POST /api/instances/:instanceRef/status/:action`;
las tres responden `202` con `AccionAceptadaResponse`. La cuarta,
`POST /api/instances/:instanceRef/pause`, hoy responde `404 NOT_FOUND`.

El cuerpo `202` es el esquema `http.AccionAceptadaResponse`:

- `tareaId`: **obligatorio**. Es el identificador opaco de la tarea registrada
  por el backend y es el mismo valor que llega en el evento `TASK_FINISHED`.
- El UPID de Proxmox permanece privado y nunca se entrega al frontend (ni en
  las respuestas ni en los eventos SSE ni en el WebSocket).

```json
{ "tareaId": "3f2504e0-4f89-11d3-9a0c-0305e82c3301" }
```

La acción es asíncrona: el `202` confirma que Proxmox aceptó la orden, **no** que
la instancia ya cambió de estado. El frontend debe esperar el evento
`TASK_FINISHED` o releer el estado de la instancia.

`POST /api/instances/:instanceRef/status/:action` acepta `start`, `stop`, `shutdown` y
`reboot`. Cualquier otra acción responde `400 INVALID_ACTION`.

### Borrado — operativo, solo ADMIN

`DELETE /api/instances/:instanceRef` elimina de forma permanente una VM o un
contenedor. Solo lo puede invocar un `ADMIN`: un `OPERATOR` recibe
`403 INSUFFICIENT_PERMISSIONS`. Tampoco se pueden borrar las instancias
protegidas de la infraestructura (`403 INSTANCE_PROTECTED`).

La instancia tiene que estar detenida: si no está `stopped` responde
`409 INSTANCE_INVALID_STATE` sin tocar Proxmox. Si está detenida, Proxmox crea la
tarea de borrado y el servidor responde `202` con el mismo
`AccionAceptadaResponse` que las acciones de energía:

```json
{ "tareaId": "3f2504e0-4f89-11d3-9a0c-0305e82c3303" }
```

El borrado es asíncrono: mientras dura, la instancia aparece en el inventario con
`activeTask.action: "DELETE"`. Al terminar llega un `TASK_FINISHED` con
`accion: "DELETE"`; si fue `COMPLETED`, la instancia ya no figura en
`GET /api/instances`.

### Pause: diferencia con lo solicitado

La acción `Pause` **no está implementada**. No existe la ruta
`POST /api/instances/:instanceRef/pause`, no está en la lista de acciones válidas del
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

Estos son los pares error/status que las acciones de energía pueden devolver
(ocho estados HTTP; `409` tiene dos códigos):

| HTTP | `errorCode` representativo | Causa                                     |
|------|----------------------------|-------------------------------------------|
| 401  | `MISSING_TOKEN`            | Token ausente, inválido o sesión revocada |
| 403  | `INSTANCE_ACCESS_DENIED`   | Falta de permisos o nivel insuficiente    |
| 404  | `RESOURCE_NOT_FOUND`       | Referencia inválida o instancia inexistente |
| 409  | `INSTANCE_BUSY`            | La instancia ejecuta otra tarea           |
| 409  | `INSTANCE_INVALID_STATE`   | Estado incompatible con la acción         |
| 500  | `INTERNAL_ERROR`           | Error al verificar permisos               |
| 500  | `PROXMOX_ERROR`            | Error de Proxmox no clasificado           |
| 502  | `PROXMOX_UNAVAILABLE`      | Proxmox caído, sin red o token rechazado  |
| 503  | `INSTANCE_LOCK_UNAVAILABLE`| No se pudo verificar si está ocupada      |
| 504  | `PROXMOX_TIMEOUT`          | Proxmox no respondió a tiempo             |

**Saneamiento de errores.** Ningún error (4xx o 5xx) devuelve el texto que
mandó Proxmox: cada código tiene su mensaje fijo, y un error de Proxmox que no
encaja en ningún código tipificado responde `500 PROXMOX_ERROR` con el texto
canónico `"No se pudo completar la operación en el hipervisor. Contacte al administrador."`. El detalle real (con la traza de Proxmox) queda
solo en el log del servidor. Además, todo mensaje de error pasa por un filtro
que quita rutas del sistema (`/usr/...`), archivos y módulos Perl (`.pm`,
`PVE::...`), números de línea y UPID.

**Exclusión mutua (`RequireInstanceIdle`).** Las operaciones que modifican una
instancia (`start`, `stop`, `status/{action}` y el borrado; también la edición
de configuración y los snapshots, cuando existan) pasan por una guarda que
permite **una sola operación a la vez por instancia**. Si hay una tarea
`RUNNING` de esa instancia o el lock `lock:instance:{vmid}:operation` está
tomado, responde `409 INSTANCE_BUSY` **sin contactar a Proxmox**. Ante una
ráfaga de pedidos simultáneos, pasa uno solo y el resto recibe `409`. Si no se
puede consultar la base, responde `503 INSTANCE_LOCK_UNAVAILABLE` y tampoco se
envía nada a Proxmox.

**Códigos adicionales que emite el servidor para el mismo estado:**

- `401`: `MISSING_TOKEN`, `INVALID_TOKEN`, `TOKEN_REVOKED`.
- `403`: `INSTANCE_ACCESS_DENIED`, `INSTANCE_PROTECTED`, `WRONG_TOKEN_TYPE`,
  `2FA_REQUIRED`, `PASSWORD_CHANGE_REQUIRED`, `NO_ROLE`, `NO_USER`,
  `INVALID_USER_ID`.
- `400` en `status/{action}` agrega `INVALID_ACTION`.
- `403` en el borrado agrega `INSUFFICIENT_PERMISSIONS` e `INVALID_ROLE`.
- `409` en el borrado usa la misma constante `INSTANCE_INVALID_STATE` (o `INSTANCE_BUSY`) que las acciones de energía.

`INSTANCE_PROTECTED` aparece en todas las rutas que pasan por
`RejectProtectedInstance`: `start`, `stop`, `status/{action}` y el borrado.

### Matriz de estados del ciclo de vida

Antes de mandar la orden a Proxmox, el servidor lee el estado de la instancia. Si
no es el que la acción necesita, responde
`409 INSTANCE_INVALID_STATE` (“La instancia se encuentra en un estado
incompatible para la acción solicitada”) y **no** escribe nada en Proxmox:

| Acción                       | Estado requerido |
|------------------------------|------------------|
| `start`                      | `stopped`        |
| `stop`, `shutdown`, `reboot` | `running`        |

`INSTANCE_INVALID_STATE` se resuelve cambiando de acción (por ejemplo, no se
enciende una instancia encendida); `INSTANCE_BUSY` se resuelve esperando el
`TASK_FINISHED` de la tarea en curso.

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

Estas diferencias se verificaron leyendo el servidor y siguen vigentes: el
contrato documenta el comportamiento real, no el solicitado.

| Solicitado                     | Observado                         |
|--------------------------------|-----------------------------------|
| Campo `error` de tipo `string` | No existe en el sobre real        |
| Acción `Pause`                 | No implementada; `404 NOT_FOUND`  |
| `TOKEN_MISSING` y afines       | El servidor emite `MISSING_TOKEN` |
| UPID visible en el frontend   | Se reemplaza por `tareaId` opaco  |

**Detalle:** el sobre real es `{ "errorCode", "message" }`, no `{ "error" }`. Los
códigos de autenticación reales son `MISSING_TOKEN`, `INVALID_TOKEN` y
`TOKEN_REVOKED`; no existe un código propio para expiración.

**Seguimiento obligatorio.** El servidor solo responde `202` cuando pudo
persistir la tarea y generar su `tareaId`. Si Proxmox aceptó la orden pero falla
el registro local, responde `500 TASK_TRACKING_UNAVAILABLE`; el cliente debe
verificar el estado antes de reintentar. El UPID no se usa como alternativa
pública. Esto vale también para el borrado, que antes respondía `204` sin cuerpo
y ahora es asíncrono como las demás acciones.

## Canal de eventos en tiempo real — operativo

El canal avisa cuando termina una tarea asíncrona. El transporte es **SSE**
(`text/event-stream`), no JSON: cada evento viaja dentro de un frame SSE y no en
el cuerpo de una respuesta.

### Autenticación por ticket de un solo uso

`GET /api/events` **no** usa `Authorization: Bearer`. El `EventSource` del
navegador no puede mandar ese header y poner el JWT en la URL lo dejaría en logs,
historial y proxies. El flujo es de dos pasos:

1. `POST /api/events/ticket` **sí** lleva `Authorization: Bearer <accessToken>` y
   responde `200` con el esquema `http.SSETicketResponse`, cuyo único campo es
   `ticket`.
2. `GET /api/events?ticket=<ticket>` lleva el ticket en la query. El ticket es un
   UUID de un solo uso, válido 30 s, y se consume con `GETDEL` al abrir el
   stream.

Si se conecta dos veces o después de vencer, el ticket ya no sirve y hay que
pedir uno nuevo.

| Ruta                 | Método | Autenticación | Éxito                          |
|----------------------|--------|---------------|--------------------------------|
| `/api/events/ticket` | POST   | `BearerAuth`  | `200` `http.SSETicketResponse` |
| `/api/events`        | GET    | `?ticket=`    | `200` `text/event-stream`      |

Errores que emite el servidor, con el sobre `{ "errorCode", "message" }`:

| Ruta                 | HTTP | `errorCode`             | Causa                                |
|----------------------|------|-------------------------|--------------------------------------|
| `/api/events/ticket` | 401  | `MISSING_TOKEN`         | Falta el header `Authorization`      |
| `/api/events/ticket` | 401  | `INVALID_TOKEN`         | Token ausente o inválido             |
| `/api/events/ticket` | 401  | `TOKEN_REVOKED`         | Sesión cerrada o revocada            |
| `/api/events/ticket` | 503  | `EVENTS_UNAVAILABLE`    | No se pudo emitir el ticket          |
| `/api/events`        | 401  | `EVENTS_TICKET_MISSING` | Falta `?ticket=` o viene vacío       |
| `/api/events`        | 401  | `EVENTS_TICKET_INVALID` | Venció, ya se usó o sesión cerrada   |
| `/api/events`        | 503  | `EVENTS_UNAVAILABLE`    | Error interno del almacén de eventos |

### Framing

El servidor manda `Cache-Control: no-cache`, `Connection: keep-alive` y
`X-Accel-Buffering: no` para que un proxy nginx no acumule el stream. El tráfico
tiene cuatro formas:

| Forma           | Cuándo               | EventSource        |
|-----------------|----------------------|--------------------|
| `: conectado`   | Al abrir el stream   | La ignora          |
| `id:` + `data:` | Por cada evento      | `onmessage`        |
| `: ping`        | Cada 25 s            | La ignora          |
| `event: cierre` | Al cortar el backend | `addEventListener` |

**Detalle:** el frame de evento son dos líneas: `id: <evento.id>` y
`data: <http.SSEEventPayload en JSON>`. El `id` permite deduplicar. El frame de
corte lleva `data: {"motivo": "..."}` con el esquema `http.SSECierrePayload`.

El `event: cierre` se emite cuando el backend corta el stream, con estos motivos:

| `motivo`           | Causa                                       |
|--------------------|---------------------------------------------|
| `LOGOUT`           | Se cerró esta sesión                        |
| `SESSIONS_REVOKED` | Se revocaron todas las sesiones del usuario |
| `USER_INACTIVE`    | El usuario ya no está activo                |

`EventSource` reconecta solo con la misma URL, y el ticket ya fue consumido: el
cliente tiene que cerrar la fuente y abrir un stream nuevo con un ticket nuevo.

### Payload del evento

El `data:` de cada evento es el esquema `http.SSEEventPayload`:

| Campo         | Tipo           | Obligatorio |
|---------------|----------------|-------------|
| `id`          | string         | sí          |
| `tipo`        | enum           | sí          |
| `severidad`   | enum           | sí          |
| `recursoTipo` | enum           | sí          |
| `recursoId`   | string         | no          |
| `mensaje`     | string         | sí          |
| `fechaHora`   | string RFC3339 | sí          |
| `detalles`    | object         | no          |

**Detalle:**

- `id` es un UUID y es el mismo valor que viaja en la línea `id:` del frame.
- `fechaHora` es RFC3339, declarado en Swagger con `format: date-time`.
- `detalles` se omite cuando el evento no trae datos extra.
- `tipo` admite `INSTANCE_STATE_CHANGED`, `INSTANCE_CREATED`,
  `RESOURCE_SATURATION`, `TASK_FINISHED` y `ALERT_TRIGGERED`.
- `severidad` admite `INFO`, `WARNING` y `CRITICAL`.
- `recursoTipo` admite `VM`, `LXC` y `HOST` (el servidor físico; reemplaza al
  antiguo `NODE`).
- `recursoId` es la referencia pública de la instancia (`inst_...`, la misma `id`
  del inventario) en los eventos de VM y LXC; se omite en los del `HOST`. Si
  `detalles.error` o `detalles.exitstatus` traen un texto de Proxmox que nombra
  el VMID ("CT 201 not running"), el número se reemplaza por la referencia.
- Los eventos de `HOST`, como las alertas `ALERT_TRIGGERED`, solo llegan a
  usuarios `ADMIN`; un `OPERATOR` no los recibe aunque tenga permisos sobre
  instancias. Ejemplos en `contrato-eventos.md`.

`detalles` tiene forma `http.SSEDetallesEvento`: todos sus campos son
opcionales porque la forma depende del `tipo` del evento. Hoy el único tipo que
se publica es `TASK_FINISHED`.

#### Payload de `TASK_FINISHED`

En `TASK_FINISHED`, `detalles` trae **siempre las seis claves**; las que no
aplican valen `null`, nunca se omiten:

| Campo        | Tipo             | Valor                                                  |
|--------------|------------------|--------------------------------------------------------|
| `tareaId`    | string           | El `tareaId` del `202`                                 |
| `accion`     | enum             | `START`, `STOP`, `SHUTDOWN`, `REBOOT`, `DELETE` o `CREATE` |
| `estado`     | enum             | `COMPLETED` o `FAILED`                                 |
| `exitstatus` | string o `null`  | Lo que informó Proxmox; `null` si no la cerró          |
| `motivo`     | enum o `null`    | `null` salvo con `estado` `FAILED`                     |
| `error`      | string o `null`  | `null` salvo con `estado` `FAILED`                     |

`motivo` solo tiene valor cuando `estado` es `FAILED`, y entonces vale:

- `PROXMOX_ERROR`: Proxmox terminó la tarea con un `exitstatus` distinto de
  `OK`. `exitstatus` y `error` traen el texto de Proxmox.
- `TIMEOUT`: Proxmox no dio por terminada la tarea en 10 minutos desde que se
  creó, o el backend se reinició y, al arrancar, la tarea ya tenía más de 3
  minutos y Proxmox no la había terminado. `exitstatus` es `null` y `error` lo
  explica.

Sus dos formas concretas son:

- `http.TaskSuccess`: `estado` `COMPLETED`, `exitstatus` `OK`, `motivo` y
  `error` en `null`. Severidad `INFO`.
- `http.TaskFailed`: `estado` `FAILED`, `motivo` `PROXMOX_ERROR` o `TIMEOUT` y
  `error` con el detalle. Severidad `WARNING`.

```json
{
  "id": "75d0322d-1d76-4dbf-ad0d-2571c458aa63",
  "tipo": "TASK_FINISHED",
  "severidad": "INFO",
  "recursoTipo": "VM",
  "recursoId": "110",
  "mensaje": "La tarea de encendido finalizó correctamente",
  "fechaHora": "2026-09-30T11:24:03-03:00",
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

`detalles` de una tarea fallida, por error de Proxmox y por tiempo:

```json
{ "tareaId": "3f2504e0-4f89-11d3-9a0c-0305e82c3302", "accion": "STOP", "estado": "FAILED", "exitstatus": "CT 201 not running", "motivo": "PROXMOX_ERROR", "error": "CT 201 not running" }
{ "tareaId": "3f2504e0-4f89-11d3-9a0c-0305e82c3304", "accion": "START", "estado": "FAILED", "exitstatus": null, "motivo": "TIMEOUT", "error": "Excedido el límite máximo de ejecución de 3 minutos" }
```

### Entrada de solo documentación en Swagger

OpenAPI 2.0 no tiene forma nativa de declarar un stream SSE ni un `oneOf`, y
`swag` solo emite los esquemas que alcanza desde una anotación de ruta. Por eso
Swagger incluye la entrada `/events/contrato-sse`, marcada con
`x-implementation-status: documentation-only`: **no es una ruta invocable**. Es el
índice `http.SSETiposPayload`, que publica `http.TaskSuccess`,
`http.TaskFailed` y `http.SSECierrePayload`.

La ruta es deliberadamente distinta de `GET /api/events` porque `swag` sobrescribe
la operación cuando dos handlers declaran el mismo path y método: duplicar
`/api/events` borraría la documentación real del stream. El frontend debe consumir
el stream desde `GET /api/events` y usar `/events/contrato-sse` solo como
referencia de tipos.

## Alcance de esta etapa

Esta etapa fija las lecturas de estado e inventario, las acciones aceptadas
(incluido el borrado) con su catálogo completo de errores y el canal de eventos en tiempo real: payload,
autenticación por ticket y framing. El detalle interno del canal sigue en
`contrato-eventos.md` y `eventos-tiempo-real.md`, que esta etapa no reemplaza.
