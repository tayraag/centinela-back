package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"el-centinela/internal/adapters/secondary/email"
	"el-centinela/internal/adapters/secondary/memoria"
	redisstore "el-centinela/internal/adapters/secondary/redis"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
)

func TestAprovisionadorSeguroRechazaFallbackLocal(t *testing.T) {
	provider := aprovisionadorSeguro(memoria.Nuevo(), nil)
	if result, err := provider.AprovisionarInstancia(context.Background(), ports.CrearInstanciaProxmoxInput{}); result != nil || !errors.Is(err, ports.ErrCoordinacionAprovisionamiento) {
		t.Fatalf("fallback result=%v err=%v; expected coordination refusal", result, err)
	}
	// Selection must retain the real provider with Redis, not silently disable it.
	store := redisstore.Nuevo(redisstore.Config{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = store.Close() })
	want := aprovisionamientoNoDisponible{}
	if got := aprovisionadorSeguro(store, want); got != want {
		t.Fatalf("shared backend changed provider: %T", got)
	}
}

func TestInicializarServicioEmail(t *testing.T) {
	tests := []struct {
		name          string
		provider      string
		appEnv        string
		wantMock      bool
		wantErr       string
		wantLog       string
		configureSMTP bool
	}{
		{
			name:     "mock explícito advierte",
			provider: "mock",
			appEnv:   "test",
			wantMock: true,
			wantLog:  "[WARN] EMAIL_PROVIDER configurado en modo MOCK. Los correos se imprimirán por stdout",
		},
		{
			name:     "ausente en desarrollo advierte y usa mock",
			appEnv:   "development",
			wantMock: true,
			wantLog:  "[WARN] EMAIL_PROVIDER ausente. Usando MOCK por defecto",
		},
		{
			name:     "desconocido en desarrollo advierte y usa mock",
			provider: "otro",
			appEnv:   "development",
			wantMock: true,
			wantLog:  `[WARN] EMAIL_PROVIDER desconocido ("otro"). Usando MOCK por defecto`,
		},
		{
			name:    "ausente en producción falla",
			appEnv:  "production",
			wantErr: "EMAIL_PROVIDER no configurado. Debe ser 'smtp' en entornos productivos",
		},
		{
			name:     "desconocido en producción falla",
			provider: "otro",
			appEnv:   "production",
			wantErr:  `EMAIL_PROVIDER inválido ("otro"). Debe ser 'smtp' en entornos productivos`,
		},
		{
			name:          "smtp válido crea servicio real",
			provider:      "smtp",
			appEnv:        "production",
			configureSMTP: true,
			wantLog:       "[INFO] EMAIL_PROVIDER configurado en modo SMTP",
		},
		{
			name:     "smtp incompleto falla",
			provider: "smtp",
			appEnv:   "production",
			wantErr:  "configuración SMTP inválida: faltan variables obligatorias:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("EMAIL_PROVIDER", tt.provider)
			t.Setenv("APP_ENV", tt.appEnv)
			for _, name := range []string{"SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASS", "SMTP_FROM"} {
				t.Setenv(name, "")
			}
			if tt.configureSMTP {
				t.Setenv("SMTP_HOST", "smtp.example.com")
				t.Setenv("SMTP_PORT", "587")
				t.Setenv("SMTP_USER", "user")
				t.Setenv("SMTP_PASS", "secret")
				t.Setenv("SMTP_FROM", "no-reply@example.com")
			}

			var logs bytes.Buffer
			originalWriter := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(originalWriter) })

			svc, err := inicializarServicioEmail()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v; se esperaba que contuviera %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}
			if tt.wantMock {
				if _, ok := svc.(*email.MockEmailService); !ok {
					t.Fatalf("servicio = %T; se esperaba *email.MockEmailService", svc)
				}
			} else if _, ok := svc.(*email.SMTPEmailService); !ok {
				t.Fatalf("servicio = %T; se esperaba *email.SMTPEmailService", svc)
			}
			if !strings.Contains(logs.String(), tt.wantLog) {
				t.Fatalf("log = %q; se esperaba que contuviera %q", logs.String(), tt.wantLog)
			}
		})
	}
}

// DoD: GET /api/version informa el proveedor de correo que quedó activo.
func TestVersionInformaProveedorEmail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, caso := range []struct {
		provider, esperado string
		smtp               bool
	}{
		{"mock", "mock", false},
		{"", "mock", false},
		{"otro", "mock", false},
		{"smtp", "smtp", true},
	} {
		t.Setenv("EMAIL_PROVIDER", caso.provider)
		t.Setenv("APP_ENV", "")
		for nombre, valor := range map[string]string{"SMTP_HOST": "smtp.example.com", "SMTP_PORT": "587", "SMTP_USER": "user", "SMTP_PASS": "secret", "SMTP_FROM": "no-reply@example.com"} {
			if !caso.smtp {
				valor = ""
			}
			t.Setenv(nombre, valor)
		}
		svc, err := inicializarServicioEmail()
		if err != nil {
			t.Fatal(err)
		}
		r := gin.New()
		r.GET("/api/version", versionHandler(email.Proveedor(svc)))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/version", nil))
		var cuerpo map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &cuerpo)
		if w.Code != http.StatusOK || cuerpo["emailProvider"] != caso.esperado || cuerpo["commit"] == "" {
			t.Errorf("EMAIL_PROVIDER=%q: %d %s, se esperaba emailProvider=%q", caso.provider, w.Code, w.Body.String(), caso.esperado)
		}
	}
}
