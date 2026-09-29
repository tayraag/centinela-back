# Simulador de Proxmox VE

Un "Proxmox de mentira" que corre en tu PC, para desarrollar y probar el backend (y el front) **sin acceso al Proxmox real**: sin Tailscale, sin VPN y sin depender de los permisos del token.

---

## ¿Por qué existe?

El backend no maneja las máquinas virtuales por sí mismo: le pide todo a la API de Proxmox (listar instancias, encender, apagar, sacar snapshots...). El problema es que el Proxmox real:

- está en una red privada a la que no todos tienen acceso,
- usa un token con permisos acotados, que a veces no alcanzan para probar,
- y es infraestructura real: un `stop` mal hecho apaga una máquina de verdad.

El simulador resuelve eso: es un programa chico que **responde igual que Proxmox**, con el mismo formato, los mismos campos y los mismos errores, pero todo pasa en memoria, en tu máquina.

## ¿Cómo encaja?

El backend no sabe si del otro lado hay un Proxmox real o el simulador: solo mira `PROXMOX_URL` en el `.env`.

```
                         ┌──────────────────────────────────────────┐
  Front ──► API ──►  cliente Proxmox ──► PROXMOX_URL                │
          (backend)   (client.go)          │                        │
                                           ├─► https://10.10.20.1:8006   Proxmox real (servidor)
                                           └─► http://localhost:8081     Simulador (tu PC)
                         └──────────────────────────────────────────┘
```

Cambiar de uno a otro es cambiar una línea del `.env`. El código del backend es exactamente el mismo.

## ¿De dónde saca las respuestas?

De las respuestas reales que capturamos pegándole a Proxmox, en la carpeta [`API proxmox respuestas/`](../API%20proxmox%20respuestas/):

- `data extraida de las peticiones.md`: inventario, estado del nodo, acciones, tareas, snapshots, creación y configuración (RF-02 a RF-11).
- `rf05- vm- rrddata.md` y `rf05- lxc- rrddata.md`: métricas de una VM y de un contenedor.

El simulador arranca con **las mismas instancias, los mismos nombres y los mismos valores** que esas capturas. Lo único que cambia es el nombre del nodo, que sale de `PROXMOX_NODE` (en las capturas era `pve`, hoy es `proxmox`), y el usuario de los UPID, que sale de `PROXMOX_TOKEN_ID`.

---

## Cómo usarlo

**1. En tu `.env`**, apuntá el backend al simulador. Token ID y secreto van igual que para el Proxmox real: el simulador los exige, así que si están mal te da 401, igual que el real.

```env
PROXMOX_URL=http://localhost:8081/api2/json
PROXMOX_NODE=proxmox
PROXMOX_TOKEN_ID=centinela-api@pve!backend-token
PROXMOX_TOKEN_SECRET=<el secreto que te pasaron>
```

**2. Levantá el simulador** en una terminal:

```bash
go run ./cmd/proxmox-simulador
```

```
🧪 Simulador de Proxmox VE escuchando en http://localhost:8081/api2/json
   Nodo "proxmox" · token centinela-api@pve!backend-token · tareas de 3s · 8 instancias
```

**3. Levantá la API** en otra terminal, como siempre:

```bash
go run ./cmd/api
```

Listo: `GET /api/instances` ahora trae las instancias del simulador. Cada pedido que le llega al simulador se ve en su terminal (`✅ GET /api2/json/cluster/resources → 200`).

> El estado vive en memoria: **al reiniciar el simulador, todo vuelve al inventario inicial.** Es a propósito, para que siempre arranques desde el mismo punto.

---

## Qué trae cargado

| VMID | Tipo | Nombre                      | Estado inicial | Notas |
| ---- | ---- | --------------------------- | -------------- | ----- |
| 100  | VM   | `PruebaLucas`               | encendida      | Administrada por HA (sus tareas se llaman `hastart`/`hastop`, como en la captura). Tiene 2 snapshots. **Protegida** en el backend. |
| 101  | LXC  | `back`                      | encendida      | **Protegida** en el backend (100–105 son infraestructura de El Centinela). |
| 110  | VM   | `Servidor-Prueba`           | apagada        | 2 cores, 2 GB, 20 GB de disco. |
| 201  | LXC  | `Contenedor-Prueba-Postman` | apagada        | Config de la captura de RF-10. |
| 9000 | VM   | `test-vm-go-9000`           | apagada        | |
| 9001 | VM   | `test-vm-go-9001`           | apagada        | |
| 9002 | LXC  | `test-lxc-go-9002`          | encendida      | |
| 9003 | VM   | `test-vm-go-9003`           | encendida      | |

Además, `cluster/resources` devuelve el nodo, dos storages (`local-lvm` y `local`) y la zona de red, igual que la captura. El backend los descarta y se queda solo con VMs y contenedores.

> "Protegida" es una regla **del backend**, no del simulador: `POST /api/instances/100/stop` responde `403 INSTANCE_PROTECTED` antes de llegar a Proxmox. Pegándole directo al simulador sí se puede apagar la 100.

---

## Qué endpoints imita

Todos bajo `/api2/json`, con el formato de Proxmox (`{"data": ...}`).

| RF | Endpoint | Qué hace |
| -- | -------- | -------- |
| RF-02, 03 | `GET /cluster/resources` | Inventario completo. Acepta `?type=vm\|node\|storage\|sdn`. |
| RF-02, 07, 11 | `GET /nodes/{node}/status` | Salud del nodo: CPU, memoria (sube cuando encendés instancias), disco, uptime. |
| — | `GET /nodes/{node}/qemu` · `GET /nodes/{node}/lxc` | Listado por tipo. |
| RF-05, 11 | `GET /nodes/{node}/{tipo}/{vmid}/status/current` | Estado detallado de una instancia. |
| RF-04 | `POST /nodes/{node}/{tipo}/{vmid}/status/{accion}` | `start`, `stop`, `shutdown`, `reboot` y, solo en VMs, `reset`. Devuelve un UPID. |
| RF-04, 11 | `GET /nodes/{node}/tasks/{upid}/status` | Estado de una tarea: `running` y después `stopped` con su `exitstatus`. |
| RF-05 | `GET /nodes/{node}/{tipo}/{vmid}/rrddata?timeframe=hour` | 60 puntos de métricas. También `day`, `week`, `month` y `year`. |
| RF-06 | `GET` / `POST /nodes/{node}/{tipo}/{vmid}/snapshot` | Listar (con la entrada `current`, "You are here!") y crear (`snapname`, `description`). |
| RF-06 | `POST .../snapshot/{nombre}/rollback` | Volver a un snapshot. |
| RF-06 | `DELETE .../snapshot/{nombre}` | Borrar un snapshot. No está en las capturas; imita el comportamiento documentado de Proxmox. |
| RF-07 | `POST /nodes/{node}/qemu` · `POST /nodes/{node}/lxc` | Crear una VM o un contenedor, con los mismos parámetros que la captura. |
| RF-10 | `GET` / `PUT /nodes/{node}/{tipo}/{vmid}/config` | Leer y editar la config (memoria, cores, nombre...). |

Cualquier otra ruta responde `501 Method '...' not implemented`, como Proxmox.

---

## Cómo se comporta (lo importante para desarrollar)

### Las acciones tardan, como en Proxmox

En Proxmox, encender o apagar **no es instantáneo**. Proxmox responde enseguida con un **UPID** (el "número de trámite" de la tarea) y la operación termina unos segundos después. El simulador hace lo mismo:

1. `POST .../110/status/start` → responde al instante con `UPID:proxmox:00083D71:...:qmstart:110:centinela-api@pve!backend-token:`
2. Durante 3 segundos, la 110 sigue `stopped` y la tarea está en `"status": "running"`.
3. Después, la 110 pasa a `running` y la tarea queda en `"status": "stopped", "exitstatus": "OK"`.

La duración se cambia con `PROXMOX_SIM_DURACION_TAREA` (en segundos; con `0` las tareas terminan al instante).

> El UPID completo, **con el `:` del final**, es el ID de la tarea. Sin el `:` final, Proxmox (y el simulador) lo rechazan.

### Errores que vas a ver (y que el front tiene que saber mostrar)

| Situación | Respuesta del simulador (igual que Proxmox) |
| --------- | ------------------------------------------- |
| Encender algo que ya está encendido | La tarea se acepta, pero termina con `exitstatus: "VM 9003 already running"`. |
| `shutdown` o `reboot` de algo apagado | La tarea termina con `exitstatus: "VM 110 not running"`. |
| Dos acciones seguidas sobre la misma instancia | La segunda da `500 can't lock file '/var/lock/qemu-server/lock-110.conf' - got timeout`. |
| Editar o sacar un snapshot mientras se crea o hace rollback | `500 VM 100 is locked (snapshot)`. |
| Snapshot con nombre repetido | `500 snapshot name 'x' already used`. |
| Crear con un VMID que ya existe (VMs y contenedores comparten IDs) | `500 unable to create VM 115 - VM 115 already exists on node 'proxmox'`. |
| Parámetro inválido o faltante | `400` con el detalle por campo en `errors`, ej. `{"ostemplate": "property is missing and it is not optional"}`. |
| VMID que no existe | `500 Configuration file 'nodes/proxmox/qemu-server/999.conf' does not exist`. |
| Token incorrecto o faltante | `401 invalid token value!`. |

### Cambios de configuración "pendientes"

Si le cambiás memoria o cores a una **VM encendida**, Proxmox guarda el cambio pero la VM lo toma recién en el próximo reinicio:

- `GET .../config` devuelve la config **con** el cambio (lo que hace Proxmox por defecto).
- `GET .../config?current=1` devuelve la que está **en uso**.
- `cluster/resources` y `status/current` siguen mostrando los valores viejos hasta un `reboot` o un `stop` + `start`.

En contenedores (LXC) y en VMs apagadas, el cambio se aplica en el momento.

### Snapshots

- Crear un snapshot es una tarea (`qmsnapshot` / `vzsnapshot`): aparece en el listado cuando la tarea termina.
- Hacer rollback deja la instancia **apagada**, con la memoria y los cores que tenía al sacar el snapshot.

### Métricas

- Los valores (CPU, memoria, red) **varían solos**, así los gráficos del front se mueven. Son "pseudoaleatorios": para el mismo instante dan siempre lo mismo, lo que hace que los tests sean reproducibles.
- `rrddata` devuelve 60 puntos, como la captura. Los momentos en que la instancia estaba **apagada** traen solo `time`, `maxcpu`, `maxmem`, `maxdisk` y `disk`, igual que el final de la captura de la VM. Si una pantalla muestra métricas, tiene que bancarse puntos sin `cpu`.

---

## Modos de falla: probar qué pasa cuando Proxmox anda mal

Para probar las pantallas de error sin romper nada, arrancá el simulador con `PROXMOX_SIM_FALLA`:

```bash
PROXMOX_SIM_FALLA=caido go run ./cmd/proxmox-simulador
```

| Modo    | Qué hace el simulador          | Qué responde la API del backend        |
| ------- | ------------------------------ | -------------------------------------- |
| `caido` | Todo responde `500`            | `502 PROXMOX_UNAVAILABLE`              |
| `token` | Todo responde `401`            | `502 PROXMOX_UNAVAILABLE` (y 🔑 en el log de la API) |
| `lento` | Cada respuesta tarda 15 s      | `504 PROXMOX_UNAVAILABLE` (el backend corta a los 10 s) |

También podés simplemente **apagar el simulador**: la API responde `502` y en su log aparece ⚠️ con el motivo.

---

## Configuración

Todas se leen del mismo `.env` del backend.

| Variable | Default | Para qué |
| -------- | ------- | -------- |
| `PROXMOX_TOKEN_ID` / `PROXMOX_TOKEN_SECRET` | *(obligatorias)* | El token que exige, igual que el real. |
| `PROXMOX_NODE` | `proxmox` | Nombre del nodo simulado. |
| `PROXMOX_SIM_PORT` | `8081` | Puerto donde escucha. |
| `PROXMOX_SIM_DURACION_TAREA` | `3` | Segundos que tarda cada tarea (start, stop, snapshot...). |
| `PROXMOX_SIM_FALLA` | *(vacío)* | Modo falla: `caido`, `token` o `lento`. |

---

## Probarlo a mano

Con el simulador corriendo (reemplazá `<secreto>` por tu `PROXMOX_TOKEN_SECRET`):

```bash
TOKEN='Authorization: PVEAPIToken=centinela-api@pve!backend-token=<secreto>'
SIM=http://localhost:8081/api2/json

curl -s -H "$TOKEN" "$SIM/cluster/resources?type=vm"                       # inventario
curl -s -H "$TOKEN" -X POST "$SIM/nodes/proxmox/qemu/110/status/start"     # encender (devuelve UPID)
curl -s -H "$TOKEN" "$SIM/nodes/proxmox/tasks/<UPID>/status"               # ver cómo va la tarea
curl -s -H "$TOKEN" "$SIM/nodes/proxmox/lxc/101/rrddata?timeframe=hour"    # métricas
curl -s -H "$TOKEN" -X POST -d snapname=antes_del_cambio "$SIM/nodes/proxmox/lxc/101/snapshot"
```

---

## En qué se diferencia del Proxmox real

Conviene tenerlo presente para no llevarse sorpresas al pasar al servidor:

- **No hay permisos por recurso.** Si el token es correcto, puede hacer todo. El token real de mínimo privilegio puede responder `403 Permission check failed` en operaciones para las que no tiene permiso. El backend ya lo maneja: lo loguea con 🔑 y responde `502`.
- **No persiste nada:** al reiniciarlo vuelve al inventario inicial.
- **Las métricas son generadas**, no medidas. El historial de `rrddata` es aproximado: solo recuerda el último encendido y el último apagado.
- **No hay consola, VNC, backups, migraciones, firewall ni storage real.** Crear una VM no instala nada; solo la agrega al inventario.
- **Un solo nodo.**
- **Validaciones parciales:** valida lo que usamos (VMID, memoria, cores, nombres de snapshot, formato de disco, `ostemplate`), no todo el esquema de Proxmox.
- **`status/current` de un LXC devuelve un objeto**, como el Proxmox real. En la captura figura un array con dos contenedores, pero esa respuesta en realidad corresponde a `GET /nodes/{node}/lxc` (el listado), que el simulador también implementa.

---

## Cómo agregar un endpoint nuevo

Cuando una tarea necesite algo de Proxmox que el simulador todavía no tiene:

1. **Capturá la respuesta real** (Postman o `curl` contra Proxmox) y agregala a `API proxmox respuestas/`, así queda la referencia.
2. **Agregá la ruta** en `rutas()` de `cmd/proxmox-simulador/simulador.go` y el handler en el archivo del tema (`instancias.go`, `snapshots.go`, `config.go`, `metricas.go`, `tareas.go`).
3. **Respondé con los mismos campos** que la captura, usando `responder(w, data)` para `{"data": ...}` y `errorPVE` / `errorParametros` para los errores.
4. **Sumá un test** en `simulador_test.go`. Si el backend va a consumir el endpoint, agregá también un test de contrato que use el cliente real (`internal/adapters/secondary/proxmox`) contra el simulador, como `TestContrato_ClienteDelBackend_ListarInstancias`.

```bash
go test ./cmd/proxmox-simulador/ -v
```

## Mapa de archivos

| Archivo | Contenido |
| ------- | --------- |
| `cmd/proxmox-simulador/main.go` | Arranque: lee el `.env` y levanta el servidor. |
| `simulador.go` | Rutas, autenticación por token, modos de falla y formato de respuestas y errores. |
| `inventario.go` | Las instancias iniciales (de las capturas) y su config. |
| `instancias.go` | Inventario, estado del nodo, estado de cada instancia y acciones de energía. |
| `tareas.go` | Tareas asíncronas, UPID y locks. |
| `metricas.go` | Métricas actuales y `rrddata`. |
| `snapshots.go` | Listar, crear, rollback y borrar snapshots. |
| `config.go` | Crear instancias y leer/editar su config. |
| `simulador_test.go` | Tests del simulador y de contrato con el cliente del backend. |
