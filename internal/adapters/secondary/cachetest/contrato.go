// Package cachetest contiene el test de contrato de ports.SesionCache: la misma
// batería de pruebas corre contra el adaptador de Redis y contra el de memoria,
// para garantizar que se comporten igual (en especial los TTL).
package cachetest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
)

// Fabrica crea un almacén vacío y devuelve también una función para adelantar
// el reloj (miniredis.FastForward o el reloj inyectado del adaptador de memoria).
type Fabrica func(t *testing.T) (cache ports.SesionCache, avanzar func(time.Duration))

// ProbarContrato corre todas las pruebas del contrato sobre el adaptador.
func ProbarContrato(t *testing.T, nueva Fabrica) {
	ctx := context.Background()

	t.Run("pre2fa: guardar, leer y vencer a los 5 minutos", func(t *testing.T) {
		cache, avanzar := nueva(t)
		usuario := uuid.New()
		if err := cache.GuardarPre2FA(ctx, "jti-1", usuario, 5*time.Minute); err != nil {
			t.Fatal(err)
		}
		if got, err := cache.ObtenerPre2FA(ctx, "jti-1"); err != nil || got != usuario {
			t.Fatalf("ObtenerPre2FA = %v, %v", got, err)
		}
		avanzar(4*time.Minute + 59*time.Second)
		if _, err := cache.ObtenerPre2FA(ctx, "jti-1"); err != nil {
			t.Fatalf("A los 4:59 todavía debe existir: %v", err)
		}
		avanzar(2 * time.Second)
		if _, err := cache.ObtenerPre2FA(ctx, "jti-1"); !errors.Is(err, ports.ErrSesionNoEncontrada) {
			t.Fatalf("A los 5:01 debe haber vencido, vino %v", err)
		}
	})

	t.Run("pre2fa: eliminar devuelve true solo la primera vez", func(t *testing.T) {
		cache, _ := nueva(t)
		_ = cache.GuardarPre2FA(ctx, "jti-2", uuid.New(), time.Minute)
		if borrada, err := cache.EliminarPre2FA(ctx, "jti-2"); err != nil || !borrada {
			t.Fatalf("Primera eliminación = %v, %v", borrada, err)
		}
		if borrada, _ := cache.EliminarPre2FA(ctx, "jti-2"); borrada {
			t.Fatal("La segunda eliminación debe devolver false (evita doble verificación)")
		}
		if _, err := cache.ObtenerPre2FA(ctx, "jti-2"); !errors.Is(err, ports.ErrSesionNoEncontrada) {
			t.Fatalf("Después de eliminarla no debe existir: %v", err)
		}
	})

	t.Run("sesión: guardar, reemplazar, vencer y eliminar", func(t *testing.T) {
		cache, avanzar := nueva(t)
		sesion := ports.SesionCacheada{
			SesionID: uuid.New(), UsuarioID: uuid.New(), JtiAccess: "access-1", JtiRefresh: "refresh-1",
			FechaExpiracion: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
		}
		if err := cache.GuardarSesion(ctx, sesion, time.Hour); err != nil {
			t.Fatal(err)
		}
		got, err := cache.ObtenerSesion(ctx, sesion.SesionID)
		if err != nil || got.JtiAccess != "access-1" || got.UsuarioID != sesion.UsuarioID || !got.FechaExpiracion.Equal(sesion.FechaExpiracion) {
			t.Fatalf("ObtenerSesion = %+v, %v", got, err)
		}

		// Renovación: mismo session_id, nuevo jti_access y TTL.
		sesion.JtiAccess = "access-2"
		_ = cache.GuardarSesion(ctx, sesion, 2*time.Hour)
		avanzar(90 * time.Minute)
		if got, err := cache.ObtenerSesion(ctx, sesion.SesionID); err != nil || got.JtiAccess != "access-2" {
			t.Fatalf("Después de renovar debe tener el jti nuevo y el TTL nuevo: %+v, %v", got, err)
		}
		avanzar(31 * time.Minute)
		if _, err := cache.ObtenerSesion(ctx, sesion.SesionID); !errors.Is(err, ports.ErrSesionNoEncontrada) {
			t.Fatalf("Debe vencer con el TTL: %v", err)
		}

		otra := ports.SesionCacheada{SesionID: uuid.New(), UsuarioID: uuid.New()}
		tercera := ports.SesionCacheada{SesionID: uuid.New(), UsuarioID: uuid.New()}
		_ = cache.GuardarSesion(ctx, otra, time.Hour)
		_ = cache.GuardarSesion(ctx, tercera, time.Hour)
		if err := cache.EliminarSesiones(ctx, otra.SesionID, tercera.SesionID, uuid.New()); err != nil {
			t.Fatalf("EliminarSesiones (con una inexistente) no debe fallar: %v", err)
		}
		for _, id := range []uuid.UUID{otra.SesionID, tercera.SesionID} {
			if _, err := cache.ObtenerSesion(ctx, id); !errors.Is(err, ports.ErrSesionNoEncontrada) {
				t.Errorf("La sesión %s debería haberse eliminado: %v", id, err)
			}
		}
		if err := cache.EliminarSesiones(ctx); err != nil {
			t.Errorf("EliminarSesiones sin IDs no debe fallar: %v", err)
		}
	})

	t.Run("claves pre2fa y de sesión no se pisan", func(t *testing.T) {
		cache, _ := nueva(t)
		id := uuid.New()
		_ = cache.GuardarPre2FA(ctx, id.String(), uuid.New(), time.Minute)
		if _, err := cache.ObtenerSesion(ctx, id); !errors.Is(err, ports.ErrSesionNoEncontrada) {
			t.Fatalf("Un pre2fa con el mismo texto no debe aparecer como sesión: %v", err)
		}
	})
}

// Inspector lee una clave cruda del almacén: valor, TTL restante y si existe.
type Inspector func(clave string) (valor string, ttl time.Duration, existe bool)

// VerificarClaves comprueba que se usen exactamente las claves y TTL de la
// especificación: SET auth:pre2fa:<jti> <usuario_id> EX 300 y
// SET auth:session:<session_id> <payload> EX <ttl_refresh>.
func VerificarClaves(t *testing.T, cache ports.SesionCache, inspeccionar Inspector) {
	ctx := context.Background()
	usuario := uuid.New()

	_ = cache.GuardarPre2FA(ctx, "jti-abc", usuario, 300*time.Second)
	valor, ttl, existe := inspeccionar("auth:pre2fa:jti-abc")
	if !existe || valor != usuario.String() || ttl != 300*time.Second {
		t.Errorf("auth:pre2fa:jti-abc = %q (TTL %s, existe %v), se esperaba %s con TTL 300s", valor, ttl, existe, usuario)
	}

	sesion := ports.SesionCacheada{SesionID: uuid.New(), UsuarioID: usuario, JtiAccess: "a", JtiRefresh: "r"}
	_ = cache.GuardarSesion(ctx, sesion, 30*24*time.Hour)
	valor, ttl, existe = inspeccionar("auth:session:" + sesion.SesionID.String())
	if !existe || ttl != 30*24*time.Hour {
		t.Errorf("auth:session:<id>: existe %v, TTL %s", existe, ttl)
	}
	for _, campo := range []string{`"jti_access":"a"`, `"jti_refresh":"r"`, `"usuario_id":"` + usuario.String() + `"`} {
		if !strings.Contains(valor, campo) {
			t.Errorf("El payload de la sesión no contiene %s: %s", campo, valor)
		}
	}
}
