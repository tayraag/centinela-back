package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	"el-centinela/internal/adapters/secondary/email"
	"el-centinela/internal/adapters/secondary/memoria"
	redisstore "el-centinela/internal/adapters/secondary/redis"
	"el-centinela/internal/core/ports"
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
