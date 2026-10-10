package redis_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/kvtest"
	"el-centinela/internal/adapters/secondary/redis"
	"el-centinela/internal/core/ports"

	"github.com/alicebob/miniredis/v2"
)

// Corre el contrato contra miniredis: un Redis en memoria que habla el mismo
// protocolo, así los tests no necesitan Docker.
func TestKVStoreRedis_Contrato(t *testing.T) {
	kvtest.ProbarContrato(t, func(t *testing.T) (ports.KeyValueStore, func(time.Duration)) {
		srv := miniredis.RunT(t)
		store := redis.Nuevo(redis.Config{Addr: srv.Addr()})
		t.Cleanup(func() { _ = store.Close() })
		return store, srv.FastForward
	})
}

// Verifica que el TTL llegue a Redis tal cual (ej. SET auth:pre2fa:<jti> ... EX 300).
func TestKVStoreRedis_TTLExacto(t *testing.T) {
	srv := miniredis.RunT(t)
	store := redis.Nuevo(redis.Config{Addr: srv.Addr()})
	_ = store.Set(context.Background(), "auth:pre2fa:jti", "usuario", 300*time.Second)
	if ttl := srv.TTL("auth:pre2fa:jti"); ttl != 300*time.Second {
		t.Errorf("TTL = %s, se esperaba 300s", ttl)
	}
}

func TestConectar(t *testing.T) {
	ctx := context.Background()

	t.Run("con contraseña y base", func(t *testing.T) {
		srv := miniredis.RunT(t)
		srv.RequireAuth("clave-redis")
		if _, err := redis.Conectar(ctx, redis.Config{Addr: srv.Addr(), Password: "incorrecta"}); err == nil {
			t.Error("Con la contraseña incorrecta debe fallar")
		}
		store, err := redis.Conectar(ctx, redis.Config{Addr: srv.Addr(), Password: "clave-redis", DB: 3})
		if err != nil {
			t.Fatalf("Con la contraseña correcta debe conectar: %v", err)
		}
		defer store.Close()
		_ = store.Set(ctx, "k", "v", time.Minute)
		srv.Select(3)
		if !srv.Exists("k") {
			t.Error("Con DB 3 las claves deben quedar en la base 3")
		}
	})

	t.Run("reintenta y falla si Redis no responde", func(t *testing.T) {
		srv := miniredis.RunT(t)
		addr := srv.Addr()
		srv.Close()
		inicio := time.Now()
		if _, err := redis.Conectar(ctx, redis.Config{Addr: addr}); err == nil {
			t.Fatal("Conectar debe fallar si Redis no responde")
		}
		if time.Since(inicio) < 1500*time.Millisecond {
			t.Error("Debe reintentar antes de rendirse (5 intentos con espera)")
		}
	})

	t.Run("se recupera si Redis aparece durante los reintentos", func(t *testing.T) {
		srv := miniredis.RunT(t)
		addr := srv.Addr()
		srv.Close()
		go func() {
			time.Sleep(300 * time.Millisecond)
			_ = srv.StartAddr(addr)
		}()
		store, err := redis.Conectar(ctx, redis.Config{Addr: addr})
		if err != nil {
			t.Fatalf("Debe conectar cuando Redis aparece en un reintento: %v", err)
		}
		_ = store.Close()
	})
}

func TestConfigDesdeEntorno(t *testing.T) {
	t.Setenv("REDIS_ADDR", "")
	t.Setenv("REDIS_PASSWORD", "clave")
	t.Setenv("REDIS_DB", "")
	cfg, err := redis.ConfigDesdeEntorno()
	if err != nil || cfg.Addr != "localhost:6379" || cfg.Password != "clave" || cfg.DB != 0 {
		t.Errorf("Defaults inesperados: %+v, %v", cfg, err)
	}

	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("REDIS_DB", "2")
	if cfg, _ := redis.ConfigDesdeEntorno(); cfg.Addr != "redis:6379" || cfg.DB != 2 {
		t.Errorf("Debe leer REDIS_ADDR y REDIS_DB: %+v", cfg)
	}

	for _, invalida := range []string{"uno", "-1"} {
		t.Setenv("REDIS_DB", invalida)
		if _, err := redis.ConfigDesdeEntorno(); err == nil {
			t.Errorf("REDIS_DB=%q debe ser rechazada", invalida)
		}
	}
}

// Con Redis caído las operaciones fallan con un error de conexión, que NO debe
// confundirse con "clave no encontrada" (el servicio de sesiones usa esa
// diferencia para caer a PostgreSQL).
func TestKVStoreRedis_CaidoNoEsClaveNoEncontrada(t *testing.T) {
	srv := miniredis.RunT(t)
	store := redis.Nuevo(redis.Config{Addr: srv.Addr()})
	srv.Close()
	ctx := context.Background()

	if _, err := store.Get(ctx, "k"); err == nil || errors.Is(err, ports.ErrClaveNoEncontrada) {
		t.Errorf("Get con Redis caído: se esperaba error de conexión, vino %v", err)
	}
	if _, err := store.GetDel(ctx, "k"); err == nil || errors.Is(err, ports.ErrClaveNoEncontrada) {
		t.Errorf("GetDel con Redis caído: se esperaba error de conexión, vino %v", err)
	}
	for nombre, err := range map[string]error{
		"Set":     store.Set(ctx, "k", "v", time.Minute),
		"Del":     store.Del(ctx, "k"),
		"Publish": store.Publish(ctx, "c", "m"),
		"Ping":    store.Ping(ctx),
	} {
		if err == nil {
			t.Errorf("%s con Redis caído debe fallar", nombre)
		}
	}
	if _, _, err := store.Subscribe(ctx, "c"); err == nil {
		t.Error("Subscribe con Redis caído debe fallar")
	}
	if err := store.Set(ctx, "k", nil, time.Minute); err == nil {
		t.Error("Set con nil debe fallar antes de hablar con Redis")
	}
}
