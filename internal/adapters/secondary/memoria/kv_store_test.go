package memoria_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/kvtest"
	"el-centinela/internal/adapters/secondary/memoria"
	"el-centinela/internal/core/ports"
)

func TestKVStoreMemoria_Contrato(t *testing.T) {
	kvtest.ProbarContrato(t, func(t *testing.T) (ports.KeyValueStore, func(time.Duration)) {
		var mu sync.Mutex
		reloj := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
		store := memoria.NuevoConReloj(func() time.Time { mu.Lock(); defer mu.Unlock(); return reloj })
		return store, func(d time.Duration) { mu.Lock(); reloj = reloj.Add(d); mu.Unlock() }
	})
}

// Un suscriptor que no lee no debe trabar a quien publica.
func TestKVStoreMemoria_SuscriptorLentoNoBloquea(t *testing.T) {
	store := memoria.Nuevo()
	ctx := context.Background()
	_, cancelar, _ := store.Subscribe(ctx, "canal")
	defer cancelar()

	listo := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			_ = store.Publish(ctx, "canal", i)
		}
		close(listo)
	}()
	select {
	case <-listo:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish se bloqueó por un suscriptor que no lee")
	}
}

func TestKVStoreMemoria_Concurrencia(t *testing.T) {
	store := memoria.Nuevo()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			msgs, cancelar, _ := store.Subscribe(ctx, "c")
			_ = store.Set(ctx, "k", i, time.Minute)
			_, _ = store.Get(ctx, "k")
			_ = store.Publish(ctx, "c", i)
			_ = cancelar()
			for range msgs {
			}
		}(i)
	}
	wg.Wait()
}
