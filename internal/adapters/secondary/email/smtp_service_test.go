package email

import (
	"strings"
	"testing"
)

func TestNewSMTPEmailServiceValidaConfiguracion(t *testing.T) {
	setSMTPValido(t)
	t.Setenv("SMTP_HOST", "")
	t.Setenv("SMTP_PASS", " ")

	_, err := NewSMTPEmailService()
	if err == nil {
		t.Fatal("se esperaba error por variables SMTP ausentes")
	}
	for _, name := range []string{"SMTP_HOST", "SMTP_PASS"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error = %q; falta mencionar %s", err, name)
		}
	}
}

func TestNewSMTPEmailServiceValidaRangoDelPuerto(t *testing.T) {
	setSMTPValido(t)
	t.Setenv("SMTP_PORT", "70000")

	_, err := NewSMTPEmailService()
	if err == nil || !strings.Contains(err.Error(), "entre 1 y 65535") {
		t.Fatalf("error = %v; se esperaba error de rango", err)
	}
}

func TestNewSMTPEmailServiceAceptaConfiguracionCompleta(t *testing.T) {
	setSMTPValido(t)

	svc, err := NewSMTPEmailService()
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if svc.host != "smtp.example.com" || svc.port != 587 || svc.user != "user" || svc.from != "no-reply@example.com" {
		t.Fatalf("configuración inesperada: %+v", svc)
	}
}

func setSMTPValido(t *testing.T) {
	t.Helper()
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_PORT", "587")
	t.Setenv("SMTP_USER", "user")
	t.Setenv("SMTP_PASS", "secret")
	t.Setenv("SMTP_FROM", "no-reply@example.com")
}
