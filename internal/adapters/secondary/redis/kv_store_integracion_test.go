package redis_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/redis"
	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

// Integración contra el Redis del docker-compose local (docker compose up -d redis).
// Usa REDIS_ADDR/REDIS_PASSWORD/REDIS_DB del .env y se saltea si Redis no responde.
func TestIntegracion_RedisDelCompose(t *testing.T) {
	_ = godotenv.Load("../../../../.env")
	cfg, err := redis.ConfigDesdeEntorno()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store := redis.Nuevo(cfg)
	if err := store.Ping(ctx); err != nil {
		t.Skipf("Saltando integración: Redis no responde en %s (%v)", cfg.Addr, err)
	}
	defer store.Close()

	prefijo := "test:" + uuid.NewString()[:8] + ":"
	t.Cleanup(func() { _ = store.Del(context.Background(), prefijo+"k", prefijo+"unico") })

	if err := store.Set(ctx, prefijo+"k", map[string]int{"n": 1}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if v, err := store.Get(ctx, prefijo+"k"); err != nil || v != `{"n":1}` {
		t.Fatalf("Get = %q, %v", v, err)
	}

	_ = store.Set(ctx, prefijo+"unico", "token", time.Minute)
	if v, err := store.GetDel(ctx, prefijo+"unico"); err != nil || v != "token" {
		t.Fatalf("GetDel = %q, %v", v, err)
	}
	if _, err := store.GetDel(ctx, prefijo+"unico"); !errors.Is(err, ports.ErrClaveNoEncontrada) {
		t.Fatalf("El segundo GetDel debe dar ErrClaveNoEncontrada, vino %v", err)
	}

	if err := store.Del(ctx, prefijo+"k"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, prefijo+"k"); !errors.Is(err, ports.ErrClaveNoEncontrada) {
		t.Fatal("Después de Del la clave no debe existir")
	}

	mensajes, cancelar, err := store.Subscribe(ctx, prefijo+"canal")
	if err != nil {
		t.Fatal(err)
	}
	defer cancelar()
	if err := store.Publish(ctx, prefijo+"canal", "hola desde el compose"); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-mensajes:
		if m.Contenido != "hola desde el compose" {
			t.Errorf("Mensaje inesperado: %+v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("No llegó el mensaje publicado")
	}
}
