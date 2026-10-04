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

## Alcance de esta etapa

Esta etapa solo fija las lecturas de estado e inventario. Las acciones aceptadas
y su catálogo completo de errores corresponden a T02; el detalle de eventos SSE
corresponde a T03 y conserva por ahora los contratos existentes en
`contrato-eventos.md` y `eventos-tiempo-real.md`.
