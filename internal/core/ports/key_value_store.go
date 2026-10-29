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

// RegistroVentana es el resultado atómico de intentar registrar un evento en
// una ventana deslizante. Permitido=false significa que la ventana ya alcanzó
// el límite y el evento no fue agregado.
type RegistroVentana struct {
	Permitido bool
	Cantidad  int64
	// ReintentarEn: si no se permitió, cuánto falta para que el intento más
	// viejo salga de la ventana y vuelva a haber lugar (Retry-After).
	ReintentarEn time.Duration
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
	// EsDistribuido indica si las operaciones coordinan todas las réplicas de la
	// API. Los límites de autenticación fallan en modo seguro cuando es false.
	EsDistribuido() bool

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

	// Incrementar suma 1 al contador de key (lo crea en 1 si no existe), le
	// renueva el vencimiento a ttl y devuelve el valor nuevo. Atómico entre
	// procesos (INCR + EXPIRE).
	Incrementar(ctx context.Context, key string, ttl time.Duration) (int64, error)

	// SetNX guarda value en key solo si la clave no existe y devuelve true si la
	// guardó. ttl = 0 significa sin vencimiento.
	SetNX(ctx context.Context, key string, value interface{}, ttl time.Duration) (bool, error)

	// RegistrarEnVentana elimina eventos anteriores a ahora-ventana, cuenta los
	// restantes y agrega member solo si todavía no se alcanzó limite. La operación
	// completa debe ser atómica entre procesos.
	RegistrarEnVentana(ctx context.Context, key, member string, ahora time.Time, ventana time.Duration, limite int64) (RegistroVentana, error)

	// EliminarDeVentana revierte una reserva concreta de RegistrarEnVentana.
	EliminarDeVentana(ctx context.Context, key, member string) error

	// AcquireLock toma un lock distribuido identificado por key. Espera hasta
	// adquirirlo, vencer el contexto o producirse un error del almacén. El TTL
	// evita locks huérfanos si el proceso cae; release solo libera el lock si
	// todavía pertenece a quien lo adquirió.
	AcquireLock(ctx context.Context, key string, ttl time.Duration) (release func(context.Context) error, err error)

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
