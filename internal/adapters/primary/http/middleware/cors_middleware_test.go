package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"el-centinela/internal/adapters/primary/http/middleware"

	"github.com/gin-gonic/gin"
)

// setupRouter arma un router Gin mínimo con el middleware CORS y una ruta de prueba.
// allowedOrigins se inyecta como variable de entorno antes de llamar a CORS().
func setupRouter(t *testing.T, allowedOrigins string) *gin.Engine {
	t.Helper()
	if allowedOrigins != "" {
		t.Setenv("ALLOWED_ORIGINS", allowedOrigins)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.CORS())
	r.GET("/ping", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	r.OPTIONS("/ping", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	return r
}

// TestCORS_OrigenPermitido verifica que un origen en la lista blanca
// recibe las cabeceras Access-Control-* correctas.
func TestCORS_OrigenPermitido(t *testing.T) {
	r := setupRouter(t, "https://centinela.tail6bb3f3.ts.net")

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("Origin", "https://centinela.tail6bb3f3.ts.net")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://centinela.tail6bb3f3.ts.net" {
		t.Errorf("Access-Control-Allow-Origin = %q; quería %q", got, "https://centinela.tail6bb3f3.ts.net")
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Access-Control-Allow-Credentials = %q; quería \"true\"", got)
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d; quería %d", w.Code, http.StatusOK)
	}
}

// TestCORS_OrigenNoPermitido verifica que un origen fuera de la lista
// NO recibe cabeceras Access-Control-* (el browser bloqueará la respuesta).
func TestCORS_OrigenNoPermitido(t *testing.T) {
	r := setupRouter(t, "https://centinela.tail6bb3f3.ts.net")

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("Origin", "https://atacante.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q; quería vacío (origen bloqueado)", got)
	}
	// El request llega igual al handler (el bloqueo lo hace el browser al leer las cabeceras).
	if w.Code != http.StatusOK {
		t.Errorf("status = %d; quería %d", w.Code, http.StatusOK)
	}
}

// TestCORS_PreflightOrigenPermitido verifica que un preflight OPTIONS
// de un origen permitido recibe 204 No Content.
func TestCORS_PreflightOrigenPermitido(t *testing.T) {
	r := setupRouter(t, "https://centinela.tail6bb3f3.ts.net")

	req := httptest.NewRequest(http.MethodOptions, "/ping", nil)
	req.Header.Set("Origin", "https://centinela.tail6bb3f3.ts.net")
	req.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d; quería %d (204 No Content)", w.Code, http.StatusNoContent)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://centinela.tail6bb3f3.ts.net" {
		t.Errorf("Access-Control-Allow-Origin = %q; quería el origen exacto", got)
	}
}

// TestCORS_PreflightOrigenNoPermitido verifica que un preflight OPTIONS
// de un origen no listado recibe 403 Forbidden.
func TestCORS_PreflightOrigenNoPermitido(t *testing.T) {
	r := setupRouter(t, "https://centinela.tail6bb3f3.ts.net")

	req := httptest.NewRequest(http.MethodOptions, "/ping", nil)
	req.Header.Set("Origin", "https://atacante.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("preflight status = %d; quería %d (403 Forbidden)", w.Code, http.StatusForbidden)
	}
}

// TestCORS_SinCabeceraOrigin verifica que un request sin header Origin
// (curl, Swagger, llamadas server-to-server) pasa sin cabeceras CORS.
func TestCORS_SinCabeceraOrigin(t *testing.T) {
	r := setupRouter(t, "https://centinela.tail6bb3f3.ts.net")

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	// No se setea Origin
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q; quería vacío (sin Origin en el request)", got)
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d; quería %d", w.Code, http.StatusOK)
	}
}

// TestCORS_VariosOrigenesPermitidos verifica que múltiples orígenes en
// ALLOWED_ORIGINS funcionan correctamente, cada uno recibe su propio valor.
func TestCORS_VariosOrigenesPermitidos(t *testing.T) {
	origenes := []string{
		"https://centinela.tail6bb3f3.ts.net",
		"http://localhost:5173",
	}

	r := setupRouter(t, "https://centinela.tail6bb3f3.ts.net,http://localhost:5173")

	for _, origen := range origenes {
		t.Run(origen, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ping", nil)
			req.Header.Set("Origin", origen)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if got := w.Header().Get("Access-Control-Allow-Origin"); got != origen {
				t.Errorf("Access-Control-Allow-Origin = %q; quería %q", got, origen)
			}
		})
	}
}

// TestCORS_ValorPorDefecto verifica que si ALLOWED_ORIGINS no está definida,
// el middleware usa el dominio de producción como valor por defecto.
func TestCORS_ValorPorDefecto(t *testing.T) {
	// setupRouter sin orígenes → ALLOWED_ORIGINS vacía → usa el default del código
	r := setupRouter(t, "")

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("Origin", "https://centinela.tail6bb3f3.ts.net")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://centinela.tail6bb3f3.ts.net" {
		t.Errorf("Access-Control-Allow-Origin = %q; quería el dominio por defecto", got)
	}
}
