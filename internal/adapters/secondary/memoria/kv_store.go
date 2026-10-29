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
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
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
	ventanas       map[string]map[string]time.Time
	ultimaLimpieza time.Time
	ahora          func() time.Time

	muSubs sync.Mutex
	subs   map[string]map[*suscripcion]struct{}
}

var _ ports.KeyValueStore = (*KVStore)(nil)

func (s *KVStore) EsDistribuido() bool { return false }

// Nuevo crea el almacén vacío.
func Nuevo() *KVStore { return NuevoConReloj(time.Now) }

// NuevoConReloj permite inyectar el reloj (para probar los TTL en tests).
func NuevoConReloj(ahora func() time.Time) *KVStore {
	return &KVStore{datos: map[string]entrada{}, ventanas: map[string]map[string]time.Time{}, subs: map[string]map[*suscripcion]struct{}{}, ahora: ahora}
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
		delete(s.ventanas, k)
	}
	return nil
}

// Incrementar suma 1 bajo el lock: atómico, como INCR + EXPIRE en Redis.
func (s *KVStore) Incrementar(_ context.Context, key string, ttl time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var valor int64
	if actual, err := s.leer(key, false); err == nil {
		if valor, err = strconv.ParseInt(actual, 10, 64); err != nil {
			return 0, fmt.Errorf("%s no es un contador: %w", key, err)
		}
	}
	valor++
	e := entrada{valor: strconv.FormatInt(valor, 10)}
	if ttl > 0 {
		e.vence = s.ahora().Add(ttl)
	}
	s.datos[key] = e
	return valor, nil
}

// SetNX guarda solo si la clave no existe (o venció), bajo el lock.
func (s *KVStore) SetNX(_ context.Context, key string, value interface{}, ttl time.Duration) (bool, error) {
	texto, err := ports.SerializarValor(value)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.leer(key, false); err == nil {
		return false, nil
	}
	e := entrada{valor: texto}
	if ttl > 0 {
		e.vence = s.ahora().Add(ttl)
	}
	s.datos[key] = e
	return true, nil
}

func (s *KVStore) RegistrarEnVentana(_ context.Context, key, member string, ahora time.Time, ventana time.Duration, limite int64) (ports.RegistroVentana, error) {
	if ventana <= 0 || limite <= 0 || member == "" {
		return ports.RegistroVentana{}, errors.New("parámetros inválidos para la ventana de tasa")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	eventos := s.ventanas[key]
	if eventos == nil {
		eventos = map[string]time.Time{}
		s.ventanas[key] = eventos
	}
	desde := ahora.Add(-ventana)
	for id, instante := range eventos {
		if !instante.After(desde) {
			delete(eventos, id)
		}
	}
	if int64(len(eventos)) >= limite {
		masViejo := ahora
		for _, instante := range eventos {
			if instante.Before(masViejo) {
				masViejo = instante
			}
		}
		return ports.RegistroVentana{Permitido: false, Cantidad: int64(len(eventos)),
			ReintentarEn: max(masViejo.Add(ventana).Sub(ahora), time.Second)}, nil
	}
	eventos[member] = ahora
	return ports.RegistroVentana{Permitido: true, Cantidad: int64(len(eventos))}, nil
}

func (s *KVStore) EliminarDeVentana(_ context.Context, key, member string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if eventos := s.ventanas[key]; eventos != nil {
		delete(eventos, member)
		if len(eventos) == 0 {
			delete(s.ventanas, key)
		}
	}
	return nil
}

// AcquireLock replica en memoria la semántica del lock distribuido. Es útil en
// desarrollo y modo degradado, aunque naturalmente solo coordina este proceso.
func (s *KVStore) AcquireLock(ctx context.Context, key string, ttl time.Duration) (func(context.Context) error, error) {
	if ttl <= 0 {
		return nil, errors.New("el TTL del lock debe ser mayor que cero")
	}
	token := uuid.NewString()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		ahora := s.ahora()
		e, existe := s.datos[key]
		if !existe || s.vencida(e, ahora) {
			s.datos[key] = entrada{valor: token, vence: ahora.Add(ttl)}
			s.mu.Unlock()
			var once sync.Once
			return func(context.Context) error {
				once.Do(func() {
					s.mu.Lock()
					defer s.mu.Unlock()
					if actual, ok := s.datos[key]; ok && actual.valor == token {
						delete(s.datos, key)
					}
				})
				return nil
			}, nil
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
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
