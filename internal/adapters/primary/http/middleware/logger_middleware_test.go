package middleware

import (
	"strings"
	"testing"
)

func TestSanitizarBody_OcultaCamposSensibles(t *testing.T) {
	casos := map[string]string{
		"login":             `{"email":"a@b.com","password":"Secreta1!"}`,
		"cambio contraseña": `{"contrasenaActual":"Vieja123!","contrasenaNueva":"Nueva123!"}`,
		"recuperación":      `{"email":"a@b.com","codigo":"123456","nuevaContrasena":"Nueva123!"}`,
		"verificar 2FA":     `{"codigo":"654321"}`,
		"QR de 2FA":         `{"qrBase64":"iVBORw0KGgo","secretoManual":"JBSWY3DPEHPK3PXP"}`,
		"anidado":           `{"usuario":{"datos":[{"password":"Secreta1!"}]}}`,
	}
	secretos := []string{"Secreta1!", "Vieja123!", "Nueva123!", "123456", "654321", "iVBORw0KGgo", "JBSWY3DPEHPK3PXP"}

	for nombre, body := range casos {
		resultado := sanitizarBody([]byte(body))
		for _, secreto := range secretos {
			if strings.Contains(resultado, secreto) {
				t.Errorf("%s: el log expone %q: %s", nombre, secreto, resultado)
			}
		}
		if !strings.Contains(resultado, "***") {
			t.Errorf("%s: se esperaba al menos un campo oculto: %s", nombre, resultado)
		}
	}
}

func TestSanitizarBody_ConservaCamposNormales(t *testing.T) {
	resultado := sanitizarBody([]byte(`{"email":"a@b.com","rol":"ADMIN","permisos":[{"vmid":110}]}`))
	for _, esperado := range []string{`"email":"a@b.com"`, `"rol":"ADMIN"`, `"vmid":110`} {
		if !strings.Contains(resultado, esperado) {
			t.Errorf("Se esperaba %s en el log: %s", esperado, resultado)
		}
	}
}

func TestSanitizarBody_TruncaJWT(t *testing.T) {
	jwt := strings.Repeat("x", 200)
	resultado := sanitizarBody([]byte(`{"accessToken":"` + jwt + `"}`))
	if strings.Contains(resultado, jwt) || !strings.Contains(resultado, "…[jwt]") {
		t.Errorf("El JWT debe truncarse: %s", resultado)
	}
}

func TestSanitizarBody_ArrayYNoJSON(t *testing.T) {
	if r := sanitizarBody([]byte(`[{"id":1},{"id":2}]`)); r != "<array de 2 elementos>" {
		t.Errorf("Array: %s", r)
	}
	if r := sanitizarBody([]byte(`no es json`)); r != "<non-json>" {
		t.Errorf("No JSON: %s", r)
	}
}
