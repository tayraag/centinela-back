// Package redis implementa ports.KeyValueStore sobre un Redis real, con el
// cliente oficial github.com/redis/go-redis/v9.
package redis

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// Timeouts y reintentos de la conexión.
const (
	timeoutConexion  = 3 * time.Second        // dial
	timeoutOperacion = 2 * time.Second        // lectura y escritura de cada comando
	reintentosCmd    = 3                      // reintentos del cliente ante errores de red en un comando
	intentosArranque = 5                      // intentos de Ping al arrancar la API
	esperaInicial    = 300 * time.Millisecond // espera inicial entre intentos al arrancar
	esperaMax        = 600 * time.Millisecond // espera máxima entre intentos
)

// Config es la configuración de la conexión.
type Config struct {
	Addr     string // REDIS_ADDR (default localhost:6379)
	Password string // REDIS_PASSWORD
	DB       int    // REDIS_DB (default 0)
}

// ConfigDesdeEntorno lee REDIS_ADDR, REDIS_PASSWORD y REDIS_DB.
func ConfigDesdeEntorno() (Config, error) {
	cfg := Config{Addr: os.Getenv("REDIS_ADDR"), Password: os.Getenv("REDIS_PASSWORD")}
	if cfg.Addr == "" {
		cfg.Addr = "localhost:6379"
	}
	if v := os.Getenv("REDIS_DB"); v != "" {
		db, err := strconv.Atoi(v)
		if err != nil || db < 0 {
			return cfg, fmt.Errorf("REDIS_DB inválida (%q): debe ser un número de base, ej. 0", v)
		}
		cfg.DB = db
	}
	return cfg, nil
}

// KVStore implementa ports.KeyValueStore con Redis.
type KVStore struct {
	client *goredis.Client
}

var _ ports.KeyValueStore = (*KVStore)(nil)

func (s *KVStore) EsDistribuido() bool { return true }

// Nuevo crea el cliente sin conectarse todavía (go-redis conecta al primer comando).
func Nuevo(cfg Config) *KVStore {
	return &KVStore{client: goredis.NewClient(&goredis.Options{
		Addr:               cfg.Addr,
		Password:           cfg.Password,
		DB:                 cfg.DB,
		DialTimeout:        timeoutConexion,
		DialerRetries:      2, // go-redis reintenta 5 veces por defecto cada conexión; con 2 alcanza
		DialerRetryTimeout: 100 * time.Millisecond,
		ReadTimeout:        timeoutOperacion,
		WriteTimeout:       timeoutOperacion,
		PoolTimeout:        timeoutConexion,
		MaxRetries:         reintentosCmd,
		MinRetryBackoff:    50 * time.Millisecond,
		MaxRetryBackoff:    500 * time.Millisecond,
	})}
}

// Conectar crea el cliente y verifica con Ping que Redis responda. Si no hay
// respuesta (por ejemplo, el contenedor todavía está levantando) reintenta
// creando una instancia limpia en cada intento para renovar el socket;
// si Redis responde con un error propio (contraseña incorrecta, base
// inexistente) falla enseguida, porque reintentar no lo va a arreglar.
func Conectar(ctx context.Context, cfg Config) (*KVStore, error) {
	espera := esperaInicial
	var err error
	for intento := 1; intento <= intentosArranque; intento++ {
		store := Nuevo(cfg)
		ctxPing, cancel := context.WithTimeout(ctx, timeoutConexion)
		err = store.Ping(ctxPing)
		cancel()

		if err == nil {
			return store, nil
		}

		_ = store.Close()

		var errRedis goredis.Error
		if errors.As(err, &errRedis) {
			return nil, fmt.Errorf("redis rechazó la conexión en %s (revisar REDIS_PASSWORD / REDIS_DB): %w", cfg.Addr, err)
		}

		if intento < intentosArranque {
			log.Printf("[WARN] Redis no responde en %s (intento %d/%d): %v", cfg.Addr, intento, intentosArranque, err)
			select {
			case <-time.After(espera):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			espera += 100 * time.Millisecond
			if espera > esperaMax {
				espera = esperaMax
			}
		}
	}
	return nil, fmt.Errorf("redis no responde en %s: %w", cfg.Addr, err)
}

// Close cierra las conexiones.
func (s *KVStore) Close() error { return s.client.Close() }

func (s *KVStore) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

func (s *KVStore) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	texto, err := ports.SerializarValor(value)
	if err != nil {
		return err
	}
	if err := s.client.Set(ctx, key, texto, ttl).Err(); err != nil {
		return fmt.Errorf("redis SET %s: %w", key, err)
	}
	return nil
}

func (s *KVStore) Get(ctx context.Context, key string) (string, error) {
	return traducir(s.client.Get(ctx, key).Result())
}

// GetDel usa el comando GETDEL (Redis ≥ 6.2), que es atómico en el servidor.
func (s *KVStore) GetDel(ctx context.Context, key string) (string, error) {
	return traducir(s.client.GetDel(ctx, key).Result())
}

func (s *KVStore) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	if err := s.client.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("redis DEL: %w", err)
	}
	return nil
}

// RegistrarEnVentana implementa un sliding window exacto con un script Lua para
// que ZREMRANGEBYSCORE, ZCARD, ZADD y EXPIRE sean una sola operación atómica.
func (s *KVStore) RegistrarEnVentana(ctx context.Context, key, member string, ahora time.Time, ventana time.Duration, limite int64) (ports.RegistroVentana, error) {
	if ventana <= 0 || limite <= 0 || member == "" {
		return ports.RegistroVentana{}, errors.New("parámetros inválidos para la ventana de tasa")
	}
	const script = `
redis.call("ZREMRANGEBYSCORE", KEYS[1], "-inf", ARGV[1])
local cantidad = redis.call("ZCARD", KEYS[1])
if cantidad >= tonumber(ARGV[3]) then
  redis.call("EXPIRE", KEYS[1], ARGV[4])
  return {0, cantidad}
end
redis.call("ZADD", KEYS[1], ARGV[2], ARGV[5])
redis.call("EXPIRE", KEYS[1], ARGV[4])
return {1, cantidad + 1}
`
	desde := ahora.Add(-ventana).UnixMilli()
	ttlSegundos := int64((ventana + time.Second - 1) / time.Second)
	resultado, err := s.client.Eval(ctx, script, []string{key}, desde, ahora.UnixMilli(), limite, ttlSegundos, member).Int64Slice()
	if err != nil {
		return ports.RegistroVentana{}, fmt.Errorf("redis sliding window %s: %w", key, err)
	}
	if len(resultado) != 2 {
		return ports.RegistroVentana{}, fmt.Errorf("redis sliding window %s devolvió un resultado inválido", key)
	}
	return ports.RegistroVentana{Permitido: resultado[0] == 1, Cantidad: resultado[1]}, nil
}

func (s *KVStore) EliminarDeVentana(ctx context.Context, key, member string) error {
	if err := s.client.ZRem(ctx, key, member).Err(); err != nil {
		return fmt.Errorf("redis ZREM %s: %w", key, err)
	}
	return nil
}

// AcquireLock implementa un lock distribuido con SET NX y un token aleatorio.
// El script de liberación compara el token antes de borrar para no eliminar un
// lock que haya vencido y luego haya sido adquirido por otro proceso.
func (s *KVStore) AcquireLock(ctx context.Context, key string, ttl time.Duration) (func(context.Context) error, error) {
	if ttl <= 0 {
		return nil, errors.New("el TTL del lock debe ser mayor que cero")
	}
	token := uuid.NewString()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		ok, err := s.client.SetNX(ctx, key, token, ttl).Result()
		if err != nil {
			return nil, fmt.Errorf("redis SET NX %s: %w", key, err)
		}
		if ok {
			var once sync.Once
			return func(releaseCtx context.Context) error {
				var releaseErr error
				once.Do(func() {
					const compareAndDelete = `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) else return 0 end`
					if err := s.client.Eval(releaseCtx, compareAndDelete, []string{key}, token).Err(); err != nil {
						releaseErr = fmt.Errorf("redis release lock %s: %w", key, err)
					}
				})
				return releaseErr
			}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *KVStore) Publish(ctx context.Context, channel string, message interface{}) error {
	texto, err := ports.SerializarValor(message)
	if err != nil {
		return err
	}
	if err := s.client.Publish(ctx, channel, texto).Err(); err != nil {
		return fmt.Errorf("redis PUBLISH %s: %w", channel, err)
	}
	return nil
}

// Subscribe espera la confirmación de Redis antes de devolver, así lo que se
// publique después ya llega. El canal devuelto se cierra al cancelar.
func (s *KVStore) Subscribe(ctx context.Context, channel string) (<-chan *ports.Mensaje, func() error, error) {
	pubsub := s.client.Subscribe(ctx, channel)
	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return nil, nil, fmt.Errorf("redis SUBSCRIBE %s: %w", channel, err)
	}

	salida := make(chan *ports.Mensaje)
	entrada := pubsub.Channel()
	fin := make(chan struct{})
	var unaVez sync.Once
	cancelar := func() error {
		var err error
		unaVez.Do(func() { close(fin); err = pubsub.Close() })
		return err
	}

	go func() {
		defer close(salida)
		for {
			select {
			case msg, ok := <-entrada:
				if !ok {
					return
				}
				select {
				case salida <- &ports.Mensaje{Canal: msg.Channel, Contenido: msg.Payload}:
				case <-fin: // cancelaron mientras esperábamos que lo lean
					return
				}
			case <-fin:
				return
			}
		}
	}()
	return salida, cancelar, nil
}

// traducir convierte el "no existe" de go-redis en el error del puerto.
func traducir(valor string, err error) (string, error) {
	if errors.Is(err, goredis.Nil) {
		return "", ports.ErrClaveNoEncontrada
	}
	if err != nil {
		return "", fmt.Errorf("redis: %w", err)
	}
	return valor, nil
}
