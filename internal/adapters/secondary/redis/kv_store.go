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
