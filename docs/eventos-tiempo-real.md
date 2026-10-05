# Canal de eventos en tiempo real (RF-11)

El backend avisa al front, sin que la pantalla tenga que preguntar, cuando pasa algo: por ahora, **cuando termina una tarea** (encender o apagar una instancia). El canal es **SSE** (Server-Sent Events): una conexión HTTP que queda abierta y por la que el back va mandando eventos.

El formato de cada evento está en [contrato-eventos.md](contrato-eventos.md).

## Cómo se conecta el front (ticket de un solo uso)

El `EventSource` del navegador **no puede mandar el header `Authorization`**. Poner el JWT en la URL sería inseguro, porque las URLs quedan en logs, historial y proxies. Por eso se usa un **ticket de un solo uso**:

```
1. POST /api/events/ticket          (con Authorization: Bearer <accessToken>)
   → 200 { "ticket": "3cb3438a-7dec-4116-b54a-369501674c92" }      vive 30 s

2. GET  /api/events?ticket=<ticket> (sin Authorization)
   → 200 text/event-stream          el ticket se consume: no sirve dos veces
```

| Respuesta de `GET /api/events` | Cuándo |
| ------------------------------ | ------ |
| `200 text/event-stream` | Ticket válido: el stream queda abierto. |
| `401 EVENTS_TICKET_MISSING` | Falta `?ticket=`. |
| `401 EVENTS_TICKET_INVALID` | El ticket no existe, venció (30 s), ya se usó o su sesión se cerró. |
| `503 EVENTS_UNAVAILABLE` | Error interno del almacén de eventos. |

### Qué llega por el stream

```
: conectado

id: 75d0322d-1d76-4dbf-ad0d-2571c458aa63
data: {"id":"75d0322d-...","tipo":"TASK_FINISHED","severidad":"INFO","recursoTipo":"VM","recursoId":"110","mensaje":"La tarea de encendido finalizó correctamente","fechaHora":"2026-09-30T11:24:03-03:00","detalles":{"accion":"start","estado":"COMPLETED","tareaId":"01a0f2b3-..."}}

: ping

event: cierre
data: {"motivo":"LOGOUT"}
```

- **Eventos**: cada uno es un `data:` con un `RealtimeEvent` en JSON. En `EventSource` llegan por `onmessage`.
- **`: ping`**: un comentario cada 25 s para que ningún proxy corte la conexión. `EventSource` lo ignora.
- **`event: cierre`**: el backend va a cortar el stream. Motivos:
  - `LOGOUT`: se cerró **esta** sesión.
  - `SESSIONS_REVOKED`: se revocaron todas las sesiones del usuario (lo desactivaron o eliminaron, o le resetearon la contraseña o el 2FA).
  - `USER_INACTIVE`: el usuario ya no está activo.

### Ejemplo para el front

```js
async function abrirEventos(accessToken, alRecibir) {
  const { ticket } = await fetch("/api/events/ticket", {
    method: "POST",
    headers: { Authorization: `Bearer ${accessToken}` },
  }).then((r) => r.json());

  const fuente = new EventSource(`/api/events?ticket=${ticket}`);
  fuente.onmessage = (e) => alRecibir(JSON.parse(e.data));   // RealtimeEvent
  fuente.addEventListener("cierre", (e) => {
    fuente.close();                                           // no reconectar
    const { motivo } = JSON.parse(e.data);                    // LOGOUT | SESSIONS_REVOKED | USER_INACTIVE
    // mandar al login si corresponde
  });
  fuente.onerror = () => {
    // IMPORTANTE: EventSource reintenta solo con la MISMA URL, pero el ticket ya
    // se consumió y va a recibir 401. Hay que cerrar y abrir con un ticket nuevo.
    fuente.close();
    setTimeout(() => abrirEventos(accessToken, alRecibir), 3000);
  };
  return fuente;
}
```

## Quién recibe cada evento

Se decide **en el momento de cada evento**, consultando la base:

| Rol | Recibe |
| --- | ------ |
| `ADMIN` | Todos los eventos. |
| `OPERATOR` | Solo los eventos de instancias (VM o LXC) sobre las que tiene permiso, con cualquier nivel. Los que no son de una instancia (por ejemplo, del nodo) son solo para ADMIN. |

Como se consulta en vivo, **si un admin le quita un permiso a un operador, deja de recibir esos eventos al instante**, sin reconectar.

## TASK_FINISHED: de dónde sale

1. Una acción de energía (`POST /api/instances/:vmid/start`, `/stop` o `/status/{action}`) se dispara en Proxmox y responde `202 { "upid": "...", "tareaId": "..." }`.
2. La tarea se registra en `tareas_asincronas` con estado `RUNNING` y entra al **pool de seguimiento**.
3. Los workers del pool le preguntan a Proxmox cómo va (`GET /nodes/{node}/tasks/{upid}/status`) cada 1 s.
4. Cuando termina, se guarda `COMPLETED` o `FAILED`, se audita el resultado (mismo código de acción que el registro `PENDING`: `START`, `STOP`, `SHUTDOWN`, `REBOOT`, con `EXITO` o `FALLA`) y se publica `TASK_FINISHED` con el mismo `tareaId`, así el front puede asociarlo a la acción que disparó.

| Resultado | `severidad` | `mensaje` | `detalles` |
| --------- | ----------- | --------- | ---------- |
| OK | `INFO` | `La tarea de encendido finalizó correctamente` | `tareaId`, `estado: COMPLETED`, `accion` |
| Error | `WARNING` | `La tarea de apagado falló` | lo mismo, con `estado: FAILED` y `error` (el mensaje de Proxmox) |

Si Proxmox no da por terminada la tarea en 10 minutos (desde que se creó), queda `FAILED`. El UPID **no** viaja al front.

### El pool de seguimiento (`internal/core/services/seguimiento_tareas.go`)

- **Consultas acotadas:** solo `UPID_WORKERS` workers (default 8) le consultan a Proxmox, así que nunca hay más consultas simultáneas que eso, aunque haya decenas de tareas en curso. Cada worker consulta una tarea **una vez** y, si no terminó, la vuelve a encolar para dentro de 1 s: todas las tareas avanzan a la par.
- **No bloquea el request:** si la cola (100 lugares) está llena, la tarea igual queda `RUNNING` en la base y la toma el reconciliador.
- **Reconciliador:** cada 5 s (y al arrancar) encola las tareas `RUNNING` de la base que no se están siguiendo: las que no entraron por cola llena y las que quedaron de un reinicio de la API. Las que ya pasaron el límite de 10 minutos se cierran como `FAILED`.
- **Apagado ordenado:** ante `SIGINT` (Ctrl+C) o `SIGTERM` (`systemctl stop`, deploy) la API deja de aceptar requests, cierra los streams SSE (el front reconecta con un ticket nuevo), termina los requests en curso y espera a que cada worker termine su consulta. Las tareas sin terminar quedan `RUNNING` y el reconciliador las retoma en el próximo arranque, así que su `TASK_FINISHED` se publica igual.

## Cómo funciona por dentro (para el back)

```
start/stop ──► SeguimientoTareas ──► Publicar(TASK_FINISHED) ─┐
logout ──────► AuthService ─────────► aviso LOGOUT ───────────┤    Redis Pub/Sub
revocar ─────► revocarSesiones ─────► aviso SESSIONS_REVOKED ─┤──► centinela:events ──► cada stream abierto
                                                              │                         (filtra y corta)
```

- **Bus**: el canal `centinela:events` de `ports.KeyValueStore` ([redis.md](redis.md)). Con varias instancias de la API, un logout en una corta el stream abierto en otra.
- **Mensajes del bus** (`ports.MensajeBus`): `{"tipo":"EVENT","evento":{...}}`, `{"tipo":"LOGOUT","usuarioId","sesionId"}` y `{"tipo":"SESSIONS_REVOKED","usuarioId"}`.
- **Para publicar un evento nuevo**, desde un servicio:

  ```go
  ev, _ := ports.NewRealtimeEvent(ports.EventoInstanciaEstado, ports.SeveridadInfo, "La VM 110 se apagó")
  _ = eventosService.Publicar(ctx, ev.ConRecurso(ports.RecursoVM, "110"))
  ```

- **Ticket en Redis**: `ws_ticket:<uuid v4>` → `{"usuario_id","sesion_id"}`, con `EX 30`, consumido con `GETDEL`. Guarda también la sesión para cortar solo el stream de esa sesión en un logout y para la auditoría.

## Auditoría

| Acción | Cuándo | `detalles` |
| ------ | ------ | ---------- |
| `EVENTOS_CONEXION` | Se abre un stream | `sesion_id` |
| `EVENTOS_CIERRE` | El backend corta un stream | `sesion_id`, `motivo` |
| `LOGOUT` y `VERIFICAR_2FA` | (ya existían) | ahora incluyen `sesion_id` |

Si el cliente cierra el stream por su cuenta (cerró la pestaña o se cortó la red), no se registra: no es una acción de seguridad y generaría ruido con cada reconexión.

## Seguridad

- El JWT **nunca** viaja en la URL: solo el ticket, que sirve una vez y durante 30 s.
- El ticket **no queda en los logs**: el logger oculta el campo `ticket` de la respuesta y no loguea la query de `/api/events`.
- Al abrir el stream se verifica que la sesión del ticket siga activa y que el usuario esté activo.

## Probarlo en local

Con `docker compose up -d db redis`, el simulador de Proxmox y la API corriendo:

```bash
TICKET=$(curl -s -X POST -H "Authorization: Bearer $ACCESS" http://localhost:8080/api/events/ticket | jq -r .ticket)
curl -N "http://localhost:8080/api/events?ticket=$TICKET"                        # queda escuchando
curl -s -X POST -H "Authorization: Bearer $ACCESS" http://localhost:8080/api/instances/110/start   # en otra terminal
# a los ~3 s llega el TASK_FINISHED por el primer curl
```
