// Package kvtest contiene el test de contrato de ports.KeyValueStore: la misma
// batería de pruebas corre contra el adaptador de Redis (con miniredis) y
// contra el de memoria, para garantizar que se comporten igual.
package kvtest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"el-centinela/internal/core/ports"
)

// Fabrica crea un almacén vacío y devuelve también una función para adelantar
// el reloj (miniredis.FastForward o el reloj inyectado del adaptador de memoria).
type Fabrica func(t *testing.T) (store ports.KeyValueStore, avanzar func(time.Duration))

// esperaMensaje es cuánto se espera un mensaje Pub/Sub antes de dar el test por fallido.
const esperaMensaje = 2 * time.Second

// ProbarContrato corre todas las pruebas del contrato sobre el adaptador.
func ProbarContrato(t *testing.T, nuevo Fabrica) {
	ctx := context.Background()

	t.Run("Ping", func(t *testing.T) {
		store, _ := nuevo(t)
		if err := store.Ping(ctx); err != nil {
			t.Fatalf("Ping: %v", err)
		}
	})

	t.Run("Set y Get", func(t *testing.T) {
		store, _ := nuevo(t)
		if err := store.Set(ctx, "clave", "valor", time.Minute); err != nil {
			t.Fatal(err)
		}
		if v, err := store.Get(ctx, "clave"); err != nil || v != "valor" {
			t.Fatalf("Get = %q, %v", v, err)
		}
		if _, err := store.Get(ctx, "no-existe"); !errors.Is(err, ports.ErrClaveNoEncontrada) {
			t.Fatalf("Get de una clave inexistente debe dar ErrClaveNoEncontrada, vino %v", err)
		}
		_ = store.Set(ctx, "clave", "otro", time.Minute)
		if v, _ := store.Get(ctx, "clave"); v != "otro" {
			t.Errorf("Set sobre una clave existente debe reemplazarla, quedó %q", v)
		}
	})

	t.Run("Incrementar cuenta, renueva el vencimiento y es atómico", func(t *testing.T) {
		store, avanzar := nuevo(t)
		for esperado := int64(1); esperado <= 3; esperado++ {
			if v, err := store.Incrementar(ctx, "contador", 90*time.Second); err != nil || v != esperado {
				t.Fatalf("Incrementar = %d, %v; se esperaba %d", v, err, esperado)
			}
			avanzar(60 * time.Second) // cada INCR renueva los 90 s
		}
		avanzar(31 * time.Second)
		if _, err := store.Get(ctx, "contador"); !errors.Is(err, ports.ErrClaveNoEncontrada) {
			t.Fatalf("Sin incrementos, el contador debe vencer: %v", err)
		}
		if v, _ := store.Incrementar(ctx, "contador", time.Minute); v != 1 {
			t.Errorf("Vencido, vuelve a empezar en 1: %d", v)
		}
		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _, _ = store.Incrementar(ctx, "paralelo", time.Minute) }()
		}
		wg.Wait()
		if v, _ := store.Get(ctx, "paralelo"); v != "50" {
			t.Errorf("50 incrementos simultáneos deben dar 50, dio %s", v)
		}
	})

	t.Run("SetNX guarda solo si no existe", func(t *testing.T) {
		store, avanzar := nuevo(t)
		if ok, err := store.SetNX(ctx, "activa", "uno", time.Minute); err != nil || !ok {
			t.Fatalf("Primer SetNX: %v %v", ok, err)
		}
		if ok, _ := store.SetNX(ctx, "activa", "dos", time.Minute); ok {
			t.Fatal("SetNX sobre una clave existente no debe guardar")
		}
		if v, _ := store.Get(ctx, "activa"); v != "uno" {
			t.Errorf("Conserva el primer valor: %q", v)
		}
		avanzar(61 * time.Second)
		if ok, _ := store.SetNX(ctx, "activa", "tres", time.Minute); !ok {
			t.Error("Vencida la clave, SetNX vuelve a guardar")
		}
		var ganadores int64
		var mu sync.Mutex
		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if ok, _ := store.SetNX(ctx, "carrera", "x", time.Minute); ok {
					mu.Lock()
					ganadores++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if ganadores != 1 {
			t.Errorf("Entre 50 SetNX simultáneos gana uno solo, ganaron %d", ganadores)
		}
	})

	t.Run("Set serializa cualquier valor igual en todos los adaptadores", func(t *testing.T) {
		store, _ := nuevo(t)
		casos := []struct {
			valor    interface{}
			esperado string
		}{
			{"texto", "texto"},
			{[]byte("bytes"), "bytes"},
			{42, "42"},
			{int64(-7), "-7"},
			{3.5, "3.5"},
			{true, "true"},
			{map[string]int{"a": 1}, `{"a":1}`},
			{struct {
				Nombre string `json:"nombre"`
			}{"x"}, `{"nombre":"x"}`},
		}
		for _, c := range casos {
			if err := store.Set(ctx, "k", c.valor, time.Minute); err != nil {
				t.Fatalf("Set(%v): %v", c.valor, err)
			}
			if v, _ := store.Get(ctx, "k"); v != c.esperado {
				t.Errorf("Set(%#v) → Get = %q, se esperaba %q", c.valor, v, c.esperado)
			}
		}
		if err := store.Set(ctx, "k", nil, time.Minute); err == nil {
			t.Error("Set con valor nil debe fallar")
		}
	})

	t.Run("TTL: vence a tiempo y ttl 0 no vence", func(t *testing.T) {
		store, avanzar := nuevo(t)
		_ = store.Set(ctx, "temporal", "x", 5*time.Minute)
		_ = store.Set(ctx, "permanente", "x", 0)
		avanzar(4*time.Minute + 59*time.Second)
		if _, err := store.Get(ctx, "temporal"); err != nil {
			t.Fatalf("A los 4:59 todavía debe existir: %v", err)
		}
		avanzar(2 * time.Second)
		if _, err := store.Get(ctx, "temporal"); !errors.Is(err, ports.ErrClaveNoEncontrada) {
			t.Fatalf("A los 5:01 debe haber vencido, vino %v", err)
		}
		if _, err := store.GetDel(ctx, "temporal"); !errors.Is(err, ports.ErrClaveNoEncontrada) {
			t.Fatalf("GetDel de una clave vencida debe dar ErrClaveNoEncontrada, vino %v", err)
		}
		avanzar(365 * 24 * time.Hour)
		if _, err := store.Get(ctx, "permanente"); err != nil {
			t.Errorf("Con ttl 0 no debe vencer nunca: %v", err)
		}
	})

	t.Run("Set sobre una clave reemplaza también el TTL", func(t *testing.T) {
		store, avanzar := nuevo(t)
		_ = store.Set(ctx, "k", "1", time.Minute)
		_ = store.Set(ctx, "k", "2", time.Hour)
		avanzar(30 * time.Minute)
		if v, err := store.Get(ctx, "k"); err != nil || v != "2" {
			t.Errorf("Debe quedar el valor y el TTL nuevos: %q, %v", v, err)
		}
	})

	t.Run("GetDel consume la clave una sola vez", func(t *testing.T) {
		store, _ := nuevo(t)
		_ = store.Set(ctx, "unico", "valor", time.Minute)
		if v, err := store.GetDel(ctx, "unico"); err != nil || v != "valor" {
			t.Fatalf("Primer GetDel = %q, %v", v, err)
		}
		if _, err := store.GetDel(ctx, "unico"); !errors.Is(err, ports.ErrClaveNoEncontrada) {
			t.Fatalf("Segundo GetDel debe dar ErrClaveNoEncontrada, vino %v", err)
		}
		if _, err := store.Get(ctx, "unico"); !errors.Is(err, ports.ErrClaveNoEncontrada) {
			t.Fatal("Después de GetDel la clave no debe existir")
		}
	})

	t.Run("GetDel es atómico con llamadas concurrentes", func(t *testing.T) {
		store, _ := nuevo(t)
		_ = store.Set(ctx, "carrera", "premio", time.Minute)
		var wg sync.WaitGroup
		var mu sync.Mutex
		ganadores := 0
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if v, err := store.GetDel(ctx, "carrera"); err == nil && v == "premio" {
					mu.Lock()
					ganadores++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if ganadores != 1 {
			t.Fatalf("Exactamente un GetDel debe obtener el valor, lo obtuvieron %d", ganadores)
		}
	})

	t.Run("Del borra varias claves e ignora las inexistentes", func(t *testing.T) {
		store, _ := nuevo(t)
		_ = store.Set(ctx, "a", "1", time.Minute)
		_ = store.Set(ctx, "b", "2", time.Minute)
		_ = store.Set(ctx, "c", "3", time.Minute)
		if err := store.Del(ctx, "a", "b", "no-existe"); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"a", "b"} {
			if _, err := store.Get(ctx, k); !errors.Is(err, ports.ErrClaveNoEncontrada) {
				t.Errorf("La clave %s debería haberse borrado", k)
			}
		}
		if v, _ := store.Get(ctx, "c"); v != "3" {
			t.Error("Del no debe tocar otras claves")
		}
		if err := store.Del(ctx); err != nil {
			t.Errorf("Del sin claves no debe fallar: %v", err)
		}
	})

	t.Run("Sliding window limita, permite revertir y descarta eventos viejos", func(t *testing.T) {
		store, _ := nuevo(t)
		base := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
		for i := 1; i <= 3; i++ {
			registro, err := store.RegistrarEnVentana(ctx, "ratelimit:test", fmt.Sprintf("intento-%d", i), base.Add(time.Duration(i)*time.Second), time.Minute, 3)
			if err != nil || !registro.Permitido || registro.Cantidad != int64(i) {
				t.Fatalf("registro %d inesperado: %+v, %v", i, registro, err)
			}
		}
		bloqueado, err := store.RegistrarEnVentana(ctx, "ratelimit:test", "intento-4", base.Add(4*time.Second), time.Minute, 3)
		if err != nil || bloqueado.Permitido || bloqueado.Cantidad != 3 {
			t.Fatalf("el cuarto evento debía bloquearse: %+v, %v", bloqueado, err)
		}
		if err := store.EliminarDeVentana(ctx, "ratelimit:test", "intento-2"); err != nil {
			t.Fatal(err)
		}
		permitido, err := store.RegistrarEnVentana(ctx, "ratelimit:test", "intento-5", base.Add(5*time.Second), time.Minute, 3)
		if err != nil || !permitido.Permitido || permitido.Cantidad != 3 {
			t.Fatalf("después de revertir debía permitir: %+v, %v", permitido, err)
		}
		trasVentana, err := store.RegistrarEnVentana(ctx, "ratelimit:test", "intento-6", base.Add(2*time.Minute), time.Minute, 3)
		if err != nil || !trasVentana.Permitido || trasVentana.Cantidad != 1 {
			t.Fatalf("los eventos viejos debían descartarse: %+v, %v", trasVentana, err)
		}
	})

	t.Run("Sliding window es atómica bajo concurrencia", func(t *testing.T) {
		store, _ := nuevo(t)
		base := time.Now()
		var wg sync.WaitGroup
		var mu sync.Mutex
		permitidos := 0
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				registro, err := store.RegistrarEnVentana(ctx, "ratelimit:concurrente", fmt.Sprintf("m-%d", i), base, time.Minute, 5)
				if err == nil && registro.Permitido {
					mu.Lock()
					permitidos++
					mu.Unlock()
				}
			}(i)
		}
		wg.Wait()
		if permitidos != 5 {
			t.Fatalf("debían permitirse exactamente 5 registros, se permitieron %d", permitidos)
		}
	})

	t.Run("AcquireLock serializa y no libera un lock ajeno", func(t *testing.T) {
		store, avanzar := nuevo(t)
		liberarPrimero, err := store.AcquireLock(ctx, "lock:recurso", time.Minute)
		if err != nil {
			t.Fatal(err)
		}

		// Simula la caída del primer dueño: el TTL vence y otro proceso toma
		// la clave. La liberación tardía del primero no debe borrar el lock nuevo.
		avanzar(2 * time.Minute)
		liberarSegundo, err := store.AcquireLock(ctx, "lock:recurso", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err := liberarPrimero(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Get(ctx, "lock:recurso"); err != nil {
			t.Fatalf("el dueño anterior no debe liberar el lock nuevo: %v", err)
		}
		if err := liberarSegundo(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Get(ctx, "lock:recurso"); !errors.Is(err, ports.ErrClaveNoEncontrada) {
			t.Fatalf("el dueño actual debe liberar el lock: %v", err)
		}
	})

	t.Run("Publish y Subscribe", func(t *testing.T) {
		store, _ := nuevo(t)
		mensajes, cancelar, err := store.Subscribe(ctx, "eventos")
		if err != nil {
			t.Fatal(err)
		}
		defer cancelar()

		_ = store.Publish(ctx, "otro-canal", "no me tiene que llegar")
		if err := store.Publish(ctx, "eventos", map[string]string{"tipo": "INSTANCE_CREATED"}); err != nil {
			t.Fatal(err)
		}
		m := recibir(t, mensajes)
		if m.Canal != "eventos" || m.Contenido != `{"tipo":"INSTANCE_CREATED"}` {
			t.Errorf("Mensaje inesperado: %+v", m)
		}
		_ = store.Publish(ctx, "eventos", "segundo")
		if m := recibir(t, mensajes); m.Contenido != "segundo" {
			t.Errorf("Los mensajes deben llegar en orden, vino %q", m.Contenido)
		}
	})

	t.Run("Publish llega a todos los suscriptores del canal", func(t *testing.T) {
		store, _ := nuevo(t)
		uno, cancelar1, _ := store.Subscribe(ctx, "difusion")
		dos, cancelar2, _ := store.Subscribe(ctx, "difusion")
		defer cancelar1()
		defer cancelar2()
		_ = store.Publish(ctx, "difusion", "hola")
		if recibir(t, uno).Contenido != "hola" || recibir(t, dos).Contenido != "hola" {
			t.Error("Los dos suscriptores deben recibir el mensaje")
		}
	})

	t.Run("cancelar la suscripción cierra el canal", func(t *testing.T) {
		store, _ := nuevo(t)
		mensajes, cancelar, _ := store.Subscribe(ctx, "temporal")
		if err := cancelar(); err != nil {
			t.Fatal(err)
		}
		select {
		case _, abierto := <-mensajes:
			if abierto {
				t.Error("Después de cancelar no deben llegar mensajes")
			}
		case <-time.After(esperaMensaje):
			t.Fatal("Después de cancelar el canal debe cerrarse")
		}
		if err := store.Publish(ctx, "temporal", "nadie escucha"); err != nil {
			t.Errorf("Publicar sin suscriptores no debe fallar: %v", err)
		}
		_ = cancelar() // cancelar dos veces no debe romper nada
	})
}

func recibir(t *testing.T, mensajes <-chan *ports.Mensaje) *ports.Mensaje {
	t.Helper()
	select {
	case m, ok := <-mensajes:
		if !ok {
			t.Fatal("El canal se cerró antes de recibir el mensaje")
		}
		return m
	case <-time.After(esperaMensaje):
		t.Fatal("No llegó el mensaje publicado")
		return nil
	}
}
