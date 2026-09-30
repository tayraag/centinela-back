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
- ✅ Login, 2FA, renovación, logout y revocación de sesiones **funcionan igual**, con los mismos TTL y la misma atomicidad de `GetDel`.
- ✅ Pub/Sub funciona **dentro del mismo proceso**: los suscriptores de esta instancia reciben lo que esta instancia publica.
- ⚠️ **Al reiniciar la API se pierde todo**: los usuarios tienen que volver a loguearse. Las sesiones siguen en PostgreSQL, pero el token temporal pre-2FA se pierde.
- ⚠️ **No se comparte entre instancias.** Con más de una instancia de la API, los mensajes Pub/Sub y los tokens pre-2FA de una no los ve la otra. Para producción con varias instancias, Redis es obligatorio.
- ⚠️ Si Redis **vuelve** después, la API no se reconecta sola: hay que reiniciarla.

**Si Redis se cae con la API ya andando** (arrancó conectada):
- Las sesiones ya abiertas **siguen funcionando**: la validación cae a PostgreSQL.
- Los **logins nuevos fallan** con "no se pudo iniciar la sesión, intentá nuevamente", porque el pre-2FA vive solo en Redis.
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

- `internal/adapters/secondary/kvtest/contrato.go`: la **misma batería** para los dos adaptadores. Cubre `Set`, `Get`, `GetDel` (incluida la atomicidad con llamadas concurrentes), `Del`, TTL, serialización de valores, `Publish` y `Subscribe` (varios suscriptores y cancelación) y `Ping`.
- El adaptador de Redis se prueba con **miniredis** (un Redis en memoria que habla el mismo protocolo), así que no necesita Docker. Además hay tests de conexión: contraseña, base, reintentos y recuperación.
