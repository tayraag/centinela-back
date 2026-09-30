// Package memoria implementa ports.KeyValueStore en la memoria del proceso.
//
// Es el MODO DEGRADADO que usa la API cuando Redis no está disponible al
// arrancar (por ejemplo, en un servidor donde todavía no se instaló). Respeta
// los mismos TTL y la misma atomicidad de GetDel que Redis, con dos
// limitaciones (ver docs/redis.md):
//   - los datos se pierden al reiniciar la API (hay que volver a loguearse);
//   - no se comparten entre procesos: el Pub/Sub solo llega a suscriptores de
//     la misma instancia de la API.
package memoria

import (
	"context"
	"sync"
	"time"

	"el-centinela/internal/core/ports"
)

const (
	// cadaCuantoLimpiar: cada tanto se barren las claves vencidas para que no se acumulen.
	cadaCuantoLimpiar = time.Minute
	// bufferSuscripcion: mensajes que puede acumular un suscriptor lento antes de
	// que se descarten los nuevos (Redis también descarta si el cliente no lee).
	bufferSuscripcion = 100
)

type entrada struct {
	valor string
	vence time.Time // cero = sin vencimiento
}

type suscripcion struct {
	canal  chan *ports.Mensaje
	cerrar sync.Once
}

// KVStore es un mapa clave → valor con vencimiento, más Pub/Sub en proceso.
// Es seguro para uso concurrente.
type KVStore struct {
	mu             sync.Mutex
	datos          map[string]entrada
	ultimaLimpieza time.Time
	ahora          func() time.Time

	muSubs sync.Mutex
	subs   map[string]map[*suscripcion]struct{}
}

var _ ports.KeyValueStore = (*KVStore)(nil)

// Nuevo crea el almacén vacío.
func Nuevo() *KVStore { return NuevoConReloj(time.Now) }

// NuevoConReloj permite inyectar el reloj (para probar los TTL en tests).
func NuevoConReloj(ahora func() time.Time) *KVStore {
	return &KVStore{datos: map[string]entrada{}, subs: map[string]map[*suscripcion]struct{}{}, ahora: ahora}
}

func (s *KVStore) Ping(context.Context) error { return nil }

func (s *KVStore) Set(_ context.Context, key string, value interface{}, ttl time.Duration) error {
	texto, err := ports.SerializarValor(value)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ahora := s.ahora()
	e := entrada{valor: texto}
	if ttl > 0 {
		e.vence = ahora.Add(ttl)
	}
	s.datos[key] = e
	if ahora.Sub(s.ultimaLimpieza) > cadaCuantoLimpiar {
		for k, v := range s.datos {
			if s.vencida(v, ahora) {
				delete(s.datos, k)
			}
		}
		s.ultimaLimpieza = ahora
	}
	return nil
}

func (s *KVStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leer(key, false)
}

// GetDel lee y borra bajo el mismo lock: es atómico, como en Redis.
func (s *KVStore) GetDel(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leer(key, true)
}

func (s *KVStore) Del(_ context.Context, keys ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		delete(s.datos, k)
	}
	return nil
}

// leer debe llamarse con s.mu tomado.
func (s *KVStore) leer(key string, borrar bool) (string, error) {
	e, ok := s.datos[key]
	if !ok {
		return "", ports.ErrClaveNoEncontrada
	}
	if s.vencida(e, s.ahora()) {
		delete(s.datos, key)
		return "", ports.ErrClaveNoEncontrada
	}
	if borrar {
		delete(s.datos, key)
	}
	return e.valor, nil
}

func (s *KVStore) vencida(e entrada, ahora time.Time) bool {
	return !e.vence.IsZero() && !ahora.Before(e.vence)
}

// Publish entrega el mensaje a cada suscriptor del canal sin bloquear: si un
// suscriptor tiene el buffer lleno, ese mensaje se descarta para él.
func (s *KVStore) Publish(_ context.Context, channel string, message interface{}) error {
	texto, err := ports.SerializarValor(message)
	if err != nil {
		return err
	}
	s.muSubs.Lock()
	defer s.muSubs.Unlock()
	for sub := range s.subs[channel] {
		select {
		case sub.canal <- &ports.Mensaje{Canal: channel, Contenido: texto}:
		default:
		}
	}
	return nil
}

func (s *KVStore) Subscribe(_ context.Context, channel string) (<-chan *ports.Mensaje, func() error, error) {
	sub := &suscripcion{canal: make(chan *ports.Mensaje, bufferSuscripcion)}
	s.muSubs.Lock()
	if s.subs[channel] == nil {
		s.subs[channel] = map[*suscripcion]struct{}{}
	}
	s.subs[channel][sub] = struct{}{}
	s.muSubs.Unlock()

	cancelar := func() error {
		sub.cerrar.Do(func() {
			s.muSubs.Lock()
			delete(s.subs[channel], sub)
			if len(s.subs[channel]) == 0 {
				delete(s.subs, channel)
			}
			close(sub.canal)
			s.muSubs.Unlock()
		})
		return nil
	}
	return sub.canal, cancelar, nil
}
