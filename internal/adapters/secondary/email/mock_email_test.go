package email

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"el-centinela/internal/core/ports"
)

func TestProveedor(t *testing.T) {
	if p := Proveedor(NewMockEmailService()); p != ProveedorMock {
		t.Errorf("mock: %q", p)
	}
	if p := Proveedor(&SMTPEmailService{}); p != ProveedorSMTP {
		t.Errorf("smtp: %q", p)
	}
}

// DoD: en modo mock el código OTP, la clave provisoria y el token quedan
// destacados en el log, con el aviso de que el correo no se envió.
func TestMock_DestacaCodigosYAvisaQueNoSeEnvio(t *testing.T) {
	var logs bytes.Buffer
	original := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(original) })

	mock := NewMockEmailService()
	casos := []struct {
		enviar   func() error
		esperado []string
	}{
		{func() error { return mock.EnviarCodigoRecuperacion("ana@empresa.com", "482913") },
			[]string{"Para: ana@empresa.com", "👉👉👉  CÓDIGO DE RECUPERACIÓN: 482913  👈👈👈"}},
		{func() error { return mock.EnviarCredencialesTemporales("juan@empresa.com", "Juan", "Tmp#9xQ2wLp4") },
			[]string{"Para: juan@empresa.com", "👉👉👉  CLAVE PROVISORIA: Tmp#9xQ2wLp4  👈👈👈", "Hola Juan,"}},
		{func() error { return mock.SendMail("nuevo@empresa.com", "Confirme su nuevo correo", "token: abc123") },
			[]string{"Para: nuevo@empresa.com", "Asunto: Confirme su nuevo correo", "token: abc123"}},
		{func() error {
			return mock.EnviarAlertaCritica([]string{"a@x.com", "b@x.com"}, ports.AlertaCorreo{Tipo: "host_saturation", RecursoTipo: "HOST", Recurso: "nodo-principal", Metrica: "CPU", Valor: 95, Umbral: 90})
		}, []string{"Para: a@x.com, b@x.com"}},
	}
	for _, caso := range casos {
		logs.Reset()
		if err := caso.enviar(); err != nil {
			t.Fatal(err)
		}
		salida := logs.String()
		for _, texto := range append(caso.esperado, "EMAIL_PROVIDER=mock · ESTE CORREO NO SE ENVIÓ") {
			if !strings.Contains(salida, texto) {
				t.Errorf("falta %q en:\n%s", texto, salida)
			}
		}
	}
}
