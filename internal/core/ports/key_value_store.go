package ports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// ==========================================
// Puerto: Almacén clave-valor y mensajería Pub/Sub (Redis)
// ==========================================

// ErrClaveNoEncontrada indica que la clave no existe o ya venció su TTL.
var ErrClaveNoEncontrada = errors.New("clave no encontrada o expirada")

// Mensaje es un mensaje recibido por una suscripción Pub/Sub. Es un tipo propio
// (y no *redis.Message) para que el núcleo no dependa del driver de Redis.
type Mensaje struct {
	Canal     string // canal por el que llegó
	Contenido string // payload tal como se publicó (ver SerializarValor)
}

// KeyValueStore es el almacén efímero compartido por todo el backend: claves
// con TTL automático y mensajería Pub/Sub. Se crea una sola vez al arrancar la
// API (cmd/api/main.go) y se inyecta donde haga falta.
//
// Lo implementan:
//   - internal/adapters/secondary/redis:   Redis real.
//   - internal/adapters/secondary/memoria: respaldo en la memoria del proceso
//     (modo degradado cuando Redis no está disponible; ver docs/redis.md).
type KeyValueStore interface {
	// Set guarda value en key. ttl = 0 significa sin vencimiento.
	// value se guarda como texto según SerializarValor.
	Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error

	// Get devuelve el valor de key, o ErrClaveNoEncontrada.
	Get(ctx context.Context, key string) (string, error)

	// GetDel devuelve el valor y borra la clave en una sola operación atómica
	// (consumo de un solo uso). Si dos llamadas compiten, solo una obtiene el
	// valor; la otra recibe ErrClaveNoEncontrada.
	GetDel(ctx context.Context, key string) (string, error)

	// Del borra las claves indicadas (las inexistentes se ignoran).
	Del(ctx context.Context, keys ...string) error

	// Publish envía message a todos los suscriptores de channel.
	// message se serializa según SerializarValor.
	Publish(ctx context.Context, channel string, message interface{}) error

	// Subscribe se suscribe a channel. Devuelve el canal por el que llegan los
	// mensajes y la función para cancelar la suscripción (cierra el canal).
	// Cuando Subscribe devuelve, la suscripción ya está activa.
	Subscribe(ctx context.Context, channel string) (<-chan *Mensaje, func() error, error)

	// Ping comprueba que el almacén responda.
	Ping(ctx context.Context) error
}

// SerializarValor define cómo se guarda cualquier valor como texto, igual en
// todos los adaptadores: string y []byte tal cual, números y bool en su forma
// decimal/literal, y el resto (structs, mapas, slices) como JSON.
func SerializarValor(v interface{}) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case []byte:
		return string(x), nil
	case bool:
		return strconv.FormatBool(x), nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprint(x), nil
	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 32), nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case nil:
		return "", errors.New("no se puede guardar un valor nil")
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return "", fmt.Errorf("no se pudo serializar el valor: %w", err)
		}
		return string(b), nil
	}
}
