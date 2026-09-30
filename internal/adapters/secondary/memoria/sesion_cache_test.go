package memoria_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/cachetest"
	"el-centinela/internal/adapters/secondary/memoria"
	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
)

func TestSesionCacheMemoria_Contrato(t *testing.T) {
	cachetest.ProbarContrato(t, func(t *testing.T) (ports.SesionCache, func(time.Duration)) {
		var mu sync.Mutex
		reloj := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
		cache := memoria.NewSesionCacheConReloj(func() time.Time { mu.Lock(); defer mu.Unlock(); return reloj })
		return cache, func(d time.Duration) { mu.Lock(); reloj = reloj.Add(d); mu.Unlock() }
	})
}

func TestSesionCacheMemoria_Concurrencia(t *testing.T) {
	cache := memoria.NewSesionCache()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := uuid.New()
			_ = cache.GuardarSesion(ctx, ports.SesionCacheada{SesionID: id}, time.Minute)
			_, _ = cache.ObtenerSesion(ctx, id)
			_ = cache.EliminarSesiones(ctx, id)
		}()
	}
	wg.Wait()
}

// Si llegan varias verificaciones 2FA a la vez con el mismo token, solo una puede ganar.
func TestSesionCacheMemoria_EliminarPre2FAUnaSolaVez(t *testing.T) {
	cache := memoria.NewSesionCache()
	ctx := context.Background()
	_ = cache.GuardarPre2FA(ctx, "jti", uuid.New(), time.Minute)

	var wg sync.WaitGroup
	var mu sync.Mutex
	ganadores := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := cache.EliminarPre2FA(ctx, "jti"); ok {
				mu.Lock()
				ganadores++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ganadores != 1 {
		t.Fatalf("Exactamente una eliminación debe ganar, ganaron %d", ganadores)
	}
}
