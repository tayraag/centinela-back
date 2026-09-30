package redis_test

import (
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/cachetest"
	"el-centinela/internal/adapters/secondary/redis"
	"el-centinela/internal/core/ports"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

// Corre el contrato contra miniredis: un Redis en memoria que habla el mismo
// protocolo, así los tests no necesitan Docker.
func TestSesionCacheRedis_Contrato(t *testing.T) {
	cachetest.ProbarContrato(t, func(t *testing.T) (ports.SesionCache, func(time.Duration)) {
		srv := miniredis.RunT(t)
		client := goredis.NewClient(&goredis.Options{Addr: srv.Addr()})
		t.Cleanup(func() { _ = client.Close() })
		return redis.NewSesionCache(client), srv.FastForward
	})
}

func TestSesionCacheRedis_ClavesYTTL(t *testing.T) {
	srv := miniredis.RunT(t)
	cache := redis.NewSesionCache(goredis.NewClient(&goredis.Options{Addr: srv.Addr()}))
	cachetest.VerificarClaves(t, cache, func(clave string) (string, time.Duration, bool) {
		if !srv.Exists(clave) {
			return "", 0, false
		}
		valor, _ := srv.Get(clave)
		return valor, srv.TTL(clave), true
	})
}

func TestConectar_RedisCaido(t *testing.T) {
	srv := miniredis.RunT(t)
	addr := srv.Addr()
	srv.Close()
	if _, err := redis.Conectar(addr, ""); err == nil {
		t.Fatal("Conectar debe fallar si Redis no responde")
	}
}

func TestConectar_ConPassword(t *testing.T) {
	srv := miniredis.RunT(t)
	srv.RequireAuth("clave-redis")
	if _, err := redis.Conectar(srv.Addr(), "incorrecta"); err == nil {
		t.Error("Con la contraseña incorrecta debe fallar")
	}
	if _, err := redis.Conectar(srv.Addr(), "clave-redis"); err != nil {
		t.Errorf("Con la contraseña correcta debe conectar: %v", err)
	}
}
