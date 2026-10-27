# Redis en el backend

Redis es el **almacén clave-valor con vencimiento automático (TTL)** y el **canal de mensajes Pub/Sub** del backend. El dominio no habla con Redis directamente: usa el puerto `ports.KeyValueStore`, y el adaptador concreto se elige al arrancar la API.

```
  servicios (auth, eventos, ...) ──► ports.KeyValueStore ──► adapters/secondary/redis    (Redis real)
                                                        └──► adapters/secondary/memoria  (modo degradado)
```

## El puerto: `ports.KeyValueStore`

Definido en `internal/core/ports/key_value_store.go`.

| Método | Qué hace |
| ------ | -------- |
| `Set(ctx, key, value, ttl)` | Guarda `value` en `key`. `ttl = 0` significa que no vence. |
| `Get(ctx, key)` | Devuelve el valor, o `ports.ErrClaveNoEncontrada` si no existe o venció. |
| `GetDel(ctx, key)` | Lee y borra **en una sola operación atómica**. Si dos llamadas compiten, solo una obtiene el valor. Sirve para tokens de un solo uso. |
| `Del(ctx, keys...)` | Borra claves; las inexistentes se ignoran. |
| `RegistrarEnVentana(ctx, key, member, ahora, ventana, limite)` | Registra atómicamente un evento en una ventana deslizante y avisa si ya alcanzó el límite. En Redis usa `ZREMRANGEBYSCORE`, `ZCARD`, `ZADD` y `EXPIRE` dentro de Lua. |
| `EliminarDeVentana(ctx, key, member)` | Revierte una reserva de la ventana; se usa cuando un login no resultó fallido. |
| `Publish(ctx, channel, message)` | Envía `message` a todos los suscriptores de `channel`. |
| `Subscribe(ctx, channel)` | Devuelve `(<-chan *ports.Mensaje, cancelar, error)`. Cuando vuelve, la suscripción ya está activa; `cancelar()` cierra el canal. |
| `Ping(ctx)` | Comprueba que el almacén responda. |

**Cómo se guardan los valores** (`ports.SerializarValor`, igual en los dos adaptadores): `string` y `[]byte` tal cual; números y `bool` en su forma literal (`42`, `3.5`, `true`); structs, mapas y slices como **JSON**. `Get` siempre devuelve el texto, y quien lo lee lo deserializa.

**Por qué `ports.Mensaje` y no `*redis.Message`:** si el puerto devolviera el tipo de la librería, el núcleo del backend dependería de Redis, que es justo lo que la arquitectura hexagonal quiere evitar. Además, el adaptador en memoria no podría implementarlo.

## Cómo usarlo en una funcionalidad nueva

El almacén se crea **una sola vez** en `cmd/api/main.go` (`conectarRedis()`) y se inyecta en los servicios. No hace falta inicializar nada más:

```go
// en el servicio
type miServicio struct {
	kv ports.KeyValueStore
}

func NewMiServicio(kv ports.KeyValueStore) *miServicio { return &miServicio{kv: kv} }

// uso
_ = s.kv.Set(ctx, "cache:instancias", listado, 30*time.Second)
valor, err := s.kv.Get(ctx, "cache:instancias")
if errors.Is(err, ports.ErrClaveNoEncontrada) { /* no está o venció */ }

// Pub/Sub
mensajes, cancelar, err := s.kv.Subscribe(ctx, "eventos")
defer cancelar()
for m := range mensajes { /* m.Contenido */ }
_ = s.kv.Publish(ctx, "eventos", evento) // evento se envía como JSON
```

```go
// en cmd/api/main.go
miServicio := services.NewMiServicio(kvStore)
```

**Convención de claves:** `<dominio>:<tipo>:<id>`, por ejemplo `auth:pre2fa:<jti>`. Siempre con TTL, salvo que haya un motivo para que no venza.

## Claves en uso

| Clave | Valor | TTL | Quién la usa |
| ----- | ----- | --- | ------------ |
| `auth:pre2fa:<jti>` | `usuario_id` | 5 min | Login → 2FA. Se consume con `GetDel` al verificar el código. |
| `auth:session:<session_id>` | JSON: `sesion_id`, `usuario_id`, `jti_access`, `jti_refresh`, `fecha_expiracion` | vida del refresh (30 días) | Réplica de `sesiones_activas` para validar cada request. PostgreSQL sigue siendo la fuente de verdad. |
| `ratelimit:login:<sha256(IP,email)>` | Sorted set de intentos fallidos | 15 min | Máximo 5 fallos por tupla IP/correo; el sexto responde `429` con `Retry-After: 900`. |
| `ratelimit:2fa:<jti>` | Sorted set de verificaciones fallidas | 5 min | Al tercer fallo se elimina también `auth:pre2fa:<jti>` y el token responde `TOKEN_REVOKED`. |
| `ratelimit:forgot:ip:<sha256(IP)>` | Sorted set de solicitudes | 60 min | Máximo 3 solicitudes de recuperación por IP. |
| `ratelimit:forgot:email:<sha256(email)>` | Sorted set de solicitudes | 60 min | Máximo 3 solicitudes por correo destino. |
| `node:status:current` | JSON del estado normalizado del nodo, con la foto del inventario (vmid, tipo y estado de cada instancia) para contar las de un OPERATOR sin volver a Proxmox; esa foto nunca se publica | 10 s | Caché de `GET /api/node/status`: mientras existe no se consulta Proxmox. |
| `node:status:last_known` | el mismo JSON | **sin TTL** | Última lectura buena del nodo: se devuelve con `stale: true` si Proxmox no responde. Ver `docs/contrato-etapa1.md`. |
| `lock:instance:{vmid}:operation` | UUID del request dueño del lock | 60 s | `RequireInstanceIdle` (`ExclusionInstancias`): una sola operación mutante a la vez por instancia. Se toma con `SET NX` antes de despachar y se libera (Lua, comparando el UUID) cuando la tarea ya quedó `RUNNING` en `tareas_asincronas`. Si está tomado, `409 INSTANCE_BUSY`. Otro proceso largo (snapshot, migración) puede tomarlo para bloquear la instancia. |
| `lock:instance:provisioning` | UUID del request dueño del lock | 30 s | `CuotasService`: serializa (para las creaciones y para los aumentos de CPU/RAM de `PUT /api/instances/:instanceRef/config`) la validación, reserva y creación para impedir overbooking. Se adquiere con `SET NX` y se libera mediante Lua comparando el UUID. |
| `alert:pending:<host\|instance>:<id>:<métrica>` | Ciclos seguidos sobre el umbral | 3 ciclos (`ALERT_POLL_INTERVAL` × 3) | Evaluador de alertas: `INCR` en cada ciclo sobre el umbral; desde 2 se dispara la alerta. Se borra cuando el valor baja del umbral (la racha tiene que ser seguida). |
| `alert:active:<host\|instance>:<id>:<métrica>` | `alertId` de la alerta emitida | 24 h | Deduplicación: se crea con `SET NX` al disparar (si ya existe, no se reenvía). Se borra al bajar del 80% y se publica `ALERT_RESOLVED`. La métrica `down` es la caída no ordenada de una instancia. |
| `alert:state:instances` | JSON vmid → estado, nombre y tipo | 24 h | Estado de cada instancia en el ciclo anterior, para detectar las que se apagaron o desaparecieron sin una tarea de Centinela. |
| `alert:evaluator:turn` | `1` | 90% del intervalo | Turno del ciclo: con varias réplicas de la API, solo la que lo toma evalúa (si no, un mismo ciclo contaría dos veces). |
| `aprovisionamiento:reservas` | JSON de CPU, RAM y disco aceptados pero todavía no consolidados en Proxmox | **sin TTL** | `CuotasService`: descuenta solicitudes en curso; asocia la reserva al VMID para no duplicar lo ya visible y solo la libera cuando Proxmox confirma la tarea como `COMPLETED`. Los resultados inciertos requieren reconciliación. |
| `ws_ticket:<uuid>` | JSON: `usuario_id`, `sesion_id` | 30 s | Ticket de un solo uso para abrir `GET /api/events`. Se consume con `GetDel`. Ver [eventos-tiempo-real.md](eventos-tiempo-real.md). |
| `ws:ticket:<uuid>` | JSON: `usuario_id`, `sesion_id` | 30 s | Ticket de un solo uso para abrir el WebSocket de métricas (`GET /api/ws/instances/:instanceRef/metrics`). Se consume con `GetDel` y permite cortar solo la conexión de una sesión revocada. |

**Canal Pub/Sub:** `centinela:events` lleva los eventos para los clientes (por ejemplo, `TASK_FINISHED`) y los avisos de control (`LOGOUT`, `SESSIONS_REVOKED`) que cortan los streams. Un `SESSIONS_REVOKED` con `sesionId` corta solo esa sesión; sin `sesionId`, corta todas las del usuario.

## Configuración

| Variable | Default | Descripción |
| -------- | ------- | ----------- |
| `REDIS_ADDR` | `localhost:6379` | Host y puerto. Si la API corre dentro de Docker Compose: `redis:6379`. |
| `REDIS_PASSWORD` | *(vacía)* | Tiene que coincidir con `--requirepass` del contenedor (`centinela_redis_pass` en local). |
| `REDIS_DB` | `0` | Número de base. Un valor no numérico o negativo **impide arrancar** la API. |

**Timeouts y reintentos** (`internal/adapters/secondary/redis/kv_store.go`):
- Conexión: 3 s. Lectura y escritura de cada comando: 2 s.
- Cada comando reintenta hasta 3 veces ante errores de red, con espera de 50 a 500 ms.
- Al arrancar se hacen hasta 3 intentos de `Ping` (esperas de 0,5 s y 1 s), por si el contenedor todavía está levantando. Si Redis **responde con un error** (por ejemplo, contraseña incorrecta), no se reintenta.

## Arranque y modo degradado

Al levantar la API se ejecuta `Ping`:

| Situación | Log | Comportamiento |
| --------- | --- | -------------- |
| Redis responde | `[INFO] Conexión con Redis establecida exitosamente.` | Todo sobre Redis. |
| Redis no responde (no está, contraseña incorrecta, etc.) | `[WARN] ...` + `[WARN] MODO DEGRADADO: ...` | La API **arranca igual** con el adaptador en memoria. |
| `REDIS_DB` inválida | `[FATAL] REDIS_DB inválida ...` | La API **no arranca**: es un error de configuración. |

**Decisión:** ante una caída, la API no aborta. Si abortara, un entorno sin Redis, como el servidor de prueba mientras infra no lo instale, se quedaría sin login.

**Qué implica el modo degradado:**
- ✅ Renovación, logout y revocación de sesiones siguen funcionando dentro del proceso, con los mismos TTL y operaciones atómicas.
- 🛑 Las rutas protegidas por rate limiting (`login`, verificación 2FA y solicitud de recuperación) responden `503 RATE_LIMIT_UNAVAILABLE`. No se usa el contador local como sustituto porque, con varias réplicas, permitiría superar los límites distribuidos.
- ✅ Pub/Sub funciona **dentro del mismo proceso**: los suscriptores de esta instancia reciben lo que esta instancia publica.
- ⚠️ **Al reiniciar la API se pierde todo**: los usuarios tienen que volver a loguearse. Las sesiones siguen en PostgreSQL, pero el token temporal pre-2FA se pierde.
- ⚠️ **No se comparte entre instancias.** Con más de una instancia de la API, los mensajes Pub/Sub y tokens pre-2FA de una no los ve la otra. Para producción con varias instancias y para habilitar las rutas críticas de autenticación, Redis es obligatorio.
- ⚠️ Si Redis **vuelve** después, la API no se reconecta sola: hay que reiniciarla.

**Si Redis se cae con la API ya andando** (arrancó conectada):
- Las sesiones ya abiertas **siguen funcionando**: la validación cae a PostgreSQL.
- Login, verificación 2FA y solicitud de recuperación responden `503 RATE_LIMIT_UNAVAILABLE` mientras no se pueda aplicar el límite distribuido.
- Cuando Redis vuelve, todo se normaliza solo, gracias a los reintentos del cliente.

## Laboratorio local

```bash
docker compose up -d redis                          # Redis en 127.0.0.1:6379, con contraseña
go run ./cmd/api                                    # tiene que loguear [INFO] Conexión con Redis...

# Mirar qué hay adentro (sin instalar redis-cli en tu PC):
docker exec -it centinela-redis redis-cli -a centinela_redis_pass --no-auth-warning
KEYS auth:*
TTL auth:pre2fa:<jti>
```

El puerto se publica solo en `127.0.0.1`: no queda expuesto en la red ni en el Wi-Fi.

## Tests

```bash
go test ./internal/adapters/secondary/redis/ ./internal/adapters/secondary/memoria/ -v
```

- `internal/adapters/secondary/kvtest/contrato.go`: la **misma batería** para los dos adaptadores. Cubre `Set`, `Get`, `GetDel`, ventanas deslizantes (incluida su atomicidad concurrente), `Del`, TTL, serialización, `Publish`, `Subscribe` y `Ping`.
- El adaptador de Redis se prueba con **miniredis** (un Redis en memoria que habla el mismo protocolo), así que no necesita Docker. Además hay tests de conexión: contraseña, base, reintentos y recuperación.
