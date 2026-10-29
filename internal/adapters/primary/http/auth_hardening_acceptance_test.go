package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	adaptersHttp "el-centinela/internal/adapters/primary/http"
	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/adapters/secondary/memoria"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
)

// authFalso responde como un login, un 2FA y un refresh exitosos.
type authFalso struct {
	ports.AuthService
	cerradas []string
}

func (a *authFalso) Login(context.Context, string, string) (*ports.LoginResult, error) {
	return &ports.LoginResult{}, nil
}

func (a *authFalso) VerificarTotp(context.Context, string, string) (*ports.TokenResult, error) {
	return &ports.TokenResult{AccessToken: "access-1", RefreshToken: "refresh-secreto", ExpiresIn: 28800,
		RefreshExpiraEn: time.Now().Add(30 * 24 * time.Hour)}, nil
}

func (a *authFalso) RefrescarToken(_ context.Context, refresh string) (*ports.TokenResult, error) {
	if refresh != "refresh-secreto" {
		return nil, ports.ErrSesionRevocada
	}
	return &ports.TokenResult{AccessToken: "access-2", RefreshToken: refresh, ExpiresIn: 28800,
		RefreshExpiraEn: time.Now().Add(10 * 24 * time.Hour)}, nil
}

func (a *authFalso) CerrarSesion(_ context.Context, refresh string) error {
	a.cerradas = append(a.cerradas, refresh)
	return nil
}

func (a *authFalso) ConfirmarRecuperacionContrasena(context.Context, string, string, string) error {
	return nil
}

// routerAuth arma /api/auth con los mismos middlewares de cmd/api/main.go. El
// paso previo (RequireAuth / RequirePreAuth) se reemplaza por un JTI fijo.
func routerAuth(svc ports.AuthService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	kv := memoria.Nuevo()
	h := adaptersHttp.NewAuthHandler(svc, kv)
	r := gin.New()
	r.Use(middleware.RechazarCabecerasConControl())
	auth := r.Group("/api/auth", middleware.ValidarCuerpoAuth())
	conJTI := func(c *gin.Context) { c.Set(middleware.ContextKeyJTI, "jti-preauth") }
	auth.POST("/login", middleware.LimitarRafagas(kv, "login"), h.Login)
	auth.POST("/2fa/verify", conJTI, h.VerificarTotp)
	auth.POST("/refresh", h.RefrescarToken)
	auth.POST("/logout", h.Logout)
	auth.POST("/password/reset", middleware.LimitarRafagas(kv, "reset"), h.ConfirmarRecuperacion)
	return r
}

func pedirAuth(r *gin.Engine, ruta, cuerpo string, cabeceras map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, ruta, strings.NewReader(cuerpo))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.10:5000"
	for k, v := range cabeceras {
		req.Header[k] = []string{v}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func cookieRefresh(t *testing.T, w *httptest.ResponseRecorder) (*http.Cookie, string) {
	t.Helper()
	for _, linea := range w.Header().Values("Set-Cookie") {
		if strings.HasPrefix(linea, "centinela_refresh=") {
			cookie, err := http.ParseSetCookie(linea)
			if err != nil {
				t.Fatal(err)
			}
			return cookie, linea
		}
	}
	t.Fatalf("No se emitió la cookie centinela_refresh: %v", w.Header().Values("Set-Cookie"))
	return nil, ""
}

// DoD: el login completo (2FA) y el refresh responden 200 con accessToken en el
// cuerpo y la cookie HttpOnly; Secure; SameSite=Strict; Path=/api/auth, con la
// vida del refresh token. El refresh token nunca va en el JSON.
func TestAuthHardening_CookieSegura(t *testing.T) {
	r := routerAuth(&authFalso{})
	for _, paso := range []struct {
		ruta, cuerpo, cookie, access string
		dias                         int
	}{
		{"/api/auth/2fa/verify", `{"codigo":"123456"}`, "", "access-1", 30},
		{"/api/auth/refresh", "", "centinela_refresh=refresh-secreto", "access-2", 10},
	} {
		w := pedirAuth(r, paso.ruta, paso.cuerpo, map[string]string{"Cookie": paso.cookie})
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", paso.ruta, w.Code, w.Body.String())
		}
		var cuerpo map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &cuerpo)
		if cuerpo["accessToken"] != paso.access || strings.Contains(strings.ToLower(w.Body.String()), "refresh") {
			t.Errorf("%s: el cuerpo trae accessToken y nunca el refresh token: %s", paso.ruta, w.Body.String())
		}
		cookie, linea := cookieRefresh(t, w)
		// HttpOnly impide que document.cookie la lea o la modifique.
		if cookie.Value != "refresh-secreto" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/api/auth" {
			t.Errorf("%s: cookie insegura: %s", paso.ruta, linea)
		}
		esperado := paso.dias * 24 * 3600
		if cookie.MaxAge < esperado-5 || cookie.MaxAge > esperado {
			t.Errorf("%s: Max-Age %d, se esperaban ~%d (lo que le queda al refresh token)", paso.ruta, cookie.MaxAge, esperado)
		}
	}
}

// COOKIE_SECURE=false (solo para desarrollo local sin HTTPS) quita Secure.
func TestAuthHardening_CookieSinSecureEnLocal(t *testing.T) {
	t.Setenv("COOKIE_SECURE", "false")
	w := pedirAuth(routerAuth(&authFalso{}), "/api/auth/2fa/verify", `{"codigo":"123456"}`, nil)
	if cookie, linea := cookieRefresh(t, w); cookie.Secure || !cookie.HttpOnly {
		t.Errorf("Con COOKIE_SECURE=false: %s", linea)
	}
}

// DoD: el logout revoca la sesión y borra la cookie con expiración negativa;
// la cookie se borra aunque la sesión ya no fuera válida.
func TestAuthHardening_LogoutBorraLaCookie(t *testing.T) {
	svc := &authFalso{}
	r := routerAuth(svc)
	w := pedirAuth(r, "/api/auth/logout", "", map[string]string{"Cookie": "centinela_refresh=refresh-secreto"})
	cookie, linea := cookieRefresh(t, w)
	if w.Code != http.StatusNoContent || len(svc.cerradas) != 1 || cookie.MaxAge >= 0 || cookie.Value != "" || cookie.Path != "/api/auth" {
		t.Errorf("Logout: %d cerradas=%v cookie=%s", w.Code, svc.cerradas, linea)
	}
	w = pedirAuth(r, "/api/auth/logout", "", nil)
	if cookie, linea := cookieRefresh(t, w); w.Code != http.StatusUnauthorized || cookie.MaxAge >= 0 {
		t.Errorf("Sin cookie también se ordena borrarla: %d %s", w.Code, linea)
	}
}

// DoD: 6 pedidos en menos de un minuto desde la misma IP: el sexto es 429
// RATE_LIMIT_EXCEEDED con Retry-After; otra IP sigue pudiendo entrar.
func TestAuthHardening_RafagasPorIP(t *testing.T) {
	for _, ruta := range []string{"/api/auth/login", "/api/auth/password/reset"} {
		r := routerAuth(&authFalso{})
		cuerpo := `{"email":"admin@empresa.com","password":"ClaveSegura2026!"}`
		if strings.HasSuffix(ruta, "reset") {
			cuerpo = `{"email":"admin@empresa.com","codigo":"123456","nuevaContrasena":"ClaveSegura2026!"}`
		}
		for i := 1; i <= 5; i++ {
			if w := pedirAuth(r, ruta, cuerpo, nil); w.Code == http.StatusTooManyRequests {
				t.Fatalf("%s: el pedido %d no debe limitarse: %s", ruta, i, w.Body.String())
			}
		}
		w := pedirAuth(r, ruta, cuerpo, nil)
		var error map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &error)
		reintento, _ := strconv.Atoi(w.Header().Get("Retry-After"))
		if w.Code != http.StatusTooManyRequests || error["errorCode"] != "RATE_LIMIT_EXCEEDED" ||
			error["message"] != "Demasiados intentos. Por favor, intente nuevamente en un minuto." || reintento < 1 || reintento > 60 {
			t.Errorf("%s: sexto pedido: %d %v Retry-After=%q", ruta, w.Code, error, w.Header().Get("Retry-After"))
		}
		otra := httptest.NewRequest(http.MethodPost, ruta, strings.NewReader(cuerpo))
		otra.Header.Set("Content-Type", "application/json")
		otra.RemoteAddr = "198.51.100.7:5000"
		w = httptest.NewRecorder()
		r.ServeHTTP(w, otra)
		if w.Code == http.StatusTooManyRequests {
			t.Errorf("%s: otra IP no comparte el límite", ruta)
		}
	}
}

// Sanitización: caracteres de control en cabeceras o en el JSON, estructuras
// anidadas, datos de más y cuerpos enormes se rechazan con 400 antes del handler.
func TestAuthHardening_Sanitizacion(t *testing.T) {
	r := routerAuth(&authFalso{})
	valido := `{"email":"admin@empresa.com","password":"ClaveSegura2026!"}`
	casos := map[string]struct {
		cuerpo    string
		cabeceras map[string]string
	}{
		"CRLF en una cabecera":         {valido, map[string]string{"User-Agent": "x\r\nSet-Cookie: centinela_refresh=robada"}},
		"NUL en una cabecera":          {valido, map[string]string{"X-Request-Id": "abc\x00def"}},
		"salto de línea en el email":   {`{"email":"admin@empresa.com\r\nBcc: x@y.com","password":"ClaveSegura2026!"}`, nil},
		"NUL en la contraseña":         {`{"email":"admin@empresa.com","password":"Clave\u0000Segura"}`, nil},
		"objeto anidado":               {`{"email":{"$ne":""},"password":"ClaveSegura2026!"}`, nil},
		"campo extra anidado":          {`{"email":"admin@empresa.com","password":"ClaveSegura2026!","extra":{"$where":"1"}}`, nil},
		"lista":                        {`{"email":["a@b.com"],"password":"ClaveSegura2026!"}`, nil},
		"no es un objeto":              {`["admin@empresa.com"]`, nil},
		"datos después del objeto":     {valido + `{"email":"otro@x.com"}`, nil},
		"JSON malformado":              {`{"email":"admin@empresa.com",`, nil},
		"cuerpo de más de 16 KB":       {`{"email":"admin@empresa.com","password":"` + strings.Repeat("a", 17*1024) + `"}`, nil},
		"carácter de control en clave": {`{"email\u0007":"admin@empresa.com","password":"ClaveSegura2026!"}`, nil},
	}
	for nombre, caso := range casos {
		w := pedirAuth(r, "/api/auth/login", caso.cuerpo, caso.cabeceras)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"errorCode":"INVALID_REQUEST"`) {
			t.Errorf("%s: se esperaba 400 INVALID_REQUEST, vino %d %s", nombre, w.Code, w.Body.String())
		}
	}
	if w := pedirAuth(r, "/api/auth/login", valido, map[string]string{"User-Agent": "Mozilla/5.0\t(X11)"}); w.Code != http.StatusOK {
		t.Errorf("Un pedido válido (con tabulador en una cabecera) pasa: %d %s", w.Code, w.Body.String())
	}
}
