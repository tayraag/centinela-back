package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/memoria"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
)

type respuestaError struct {
	ErrorCode string `json:"errorCode"`
}

type storeDistribuidoPrueba struct{ *memoria.KVStore }

func (storeDistribuidoPrueba) EsDistribuido() bool { return true }

func nuevoStoreDistribuidoPrueba() ports.KeyValueStore {
	return storeDistribuidoPrueba{KVStore: memoria.Nuevo()}
}

func TestLimitarLogin_RechazaElSextoFallo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := nuevoStoreDistribuidoPrueba()
	router := gin.New()
	router.POST("/login", LimitarLogin(store), func(c *gin.Context) {
		c.JSON(http.StatusUnauthorized, gin.H{"errorCode": "AUTH_FAILED"})
	})

	for intento := 1; intento <= 6; intento++ {
		w := ejecutarJSON(router, "/login", "203.0.113.10:1234", `{"email":"Usuario@Ejemplo.com","password":"incorrecta"}`)
		if intento <= 5 && w.Code != http.StatusUnauthorized {
			t.Fatalf("intento %d: status = %d, se esperaba 401", intento, w.Code)
		}
		if intento == 6 {
			if w.Code != http.StatusTooManyRequests {
				t.Fatalf("sexto intento: status = %d, se esperaba 429", w.Code)
			}
			if got := w.Header().Get("Retry-After"); got != "900" {
				t.Fatalf("Retry-After = %q, se esperaba 900", got)
			}
			if codigoError(w) != "TOO_MANY_ATTEMPTS" {
				t.Fatalf("errorCode = %q", codigoError(w))
			}
		}
	}
}

func TestLimitarLogin_NoCuentaExitosNiComparteTuplas(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := nuevoStoreDistribuidoPrueba()
	router := gin.New()
	fallar := true
	router.POST("/login", LimitarLogin(store), func(c *gin.Context) {
		if fallar {
			c.Status(http.StatusUnauthorized)
			return
		}
		c.Status(http.StatusOK)
	})

	for i := 0; i < 4; i++ {
		if got := ejecutarJSON(router, "/login", "203.0.113.11:1", `{"email":"a@ejemplo.com","password":"x"}`).Code; got != 401 {
			t.Fatalf("status = %d", got)
		}
	}
	fallar = false
	if got := ejecutarJSON(router, "/login", "203.0.113.11:1", `{"email":"a@ejemplo.com","password":"correcta"}`).Code; got != 200 {
		t.Fatalf("login exitoso: status = %d", got)
	}
	fallar = true
	if got := ejecutarJSON(router, "/login", "203.0.113.12:1", `{"email":"a@ejemplo.com","password":"x"}`).Code; got != 401 {
		t.Fatalf("otra IP debe tener su propia ventana, status = %d", got)
	}
	if got := ejecutarJSON(router, "/login", "203.0.113.11:1", `{"email":"b@ejemplo.com","password":"x"}`).Code; got != 401 {
		t.Fatalf("otro email debe tener su propia ventana, status = %d", got)
	}
}

func TestLimitarVerificacion2FA_RevocaEnElTercerFallo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := nuevoStoreDistribuidoPrueba()
	const jti = "preauth-jti-prueba"
	if err := store.Set(context.Background(), "auth:pre2fa:"+jti, "usuario", 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/2fa/verify", func(c *gin.Context) {
		c.Set(ContextKeyJTI, jti)
		c.Next()
	}, LimitarVerificacion2FA(store), func(c *gin.Context) {
		if UltimoIntento2FA(c) {
			c.JSON(http.StatusUnauthorized, gin.H{"errorCode": "TOKEN_REVOKED"})
			return
		}
		c.JSON(http.StatusUnauthorized, gin.H{"errorCode": "TOTP_FAILED"})
	})

	for intento := 1; intento <= 3; intento++ {
		w := ejecutarJSON(router, "/2fa/verify", "203.0.113.20:1", `{"codigo":"000000"}`)
		esperado := "TOTP_FAILED"
		if intento == 3 {
			esperado = "TOKEN_REVOKED"
		}
		if w.Code != http.StatusUnauthorized || codigoError(w) != esperado {
			t.Fatalf("intento %d: status=%d errorCode=%q", intento, w.Code, codigoError(w))
		}
	}
	if _, err := store.Get(context.Background(), "auth:pre2fa:"+jti); err != ports.ErrClaveNoEncontrada {
		t.Fatalf("la clave pre-2FA debe eliminarse en el tercer fallo, err=%v", err)
	}
}

func TestLimitarSolicitudRecuperacion_LimitaPorIPYCorreo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := nuevoStoreDistribuidoPrueba()
	router := gin.New()
	router.POST("/password/forgot", LimitarSolicitudRecuperacion(store), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	for i := 1; i <= 4; i++ {
		w := ejecutarJSON(router, "/password/forgot", "203.0.113.30:1", `{"email":"destino@ejemplo.com"}`)
		if i <= 3 && w.Code != http.StatusOK {
			t.Fatalf("solicitud %d: status = %d", i, w.Code)
		}
		if i == 4 && (w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "3600") {
			t.Fatalf("cuarta solicitud: status=%d Retry-After=%q", w.Code, w.Header().Get("Retry-After"))
		}
	}

	// El mismo correo también queda limitado aunque cambie la IP.
	w := ejecutarJSON(router, "/password/forgot", "203.0.113.31:1", `{"email":"destino@ejemplo.com"}`)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("mismo correo desde otra IP: status = %d", w.Code)
	}
}

func TestRateLimit_FallaEnModoSeguroSinRedisDistribuido(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/login", LimitarLogin(memoria.Nuevo()), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	w := ejecutarJSON(router, "/login", "203.0.113.40:1", `{"email":"a@ejemplo.com","password":"x"}`)
	if w.Code != http.StatusServiceUnavailable || codigoError(w) != "RATE_LIMIT_UNAVAILABLE" {
		t.Fatalf("modo degradado: status=%d errorCode=%q", w.Code, codigoError(w))
	}
}

func ejecutarJSON(handler http.Handler, path, remoteAddr, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func codigoError(w *httptest.ResponseRecorder) string {
	var respuesta respuestaError
	_ = json.Unmarshal(w.Body.Bytes(), &respuesta)
	return respuesta.ErrorCode
}
