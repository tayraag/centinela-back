package email

import (
	"log"
	"strings"

	"el-centinela/internal/core/ports"
)

// Proveedores de correo que informa la API (GET /api/version y la cabecera
// X-Centinela-Email-Provider).
const (
	ProveedorMock = "mock"
	ProveedorSMTP = "smtp"
)

// Proveedor devuelve "mock" si el servicio de correo es el simulado (los
// correos no salen a la red) y "smtp" en cualquier otro caso.
func Proveedor(svc ports.EmailService) string {
	if _, esMock := svc.(*MockEmailService); esMock {
		return ProveedorMock
	}
	return ProveedorSMTP
}

// MockEmailService implementa ports.EmailService simulando el envío mediante logs en consola.
// Ideal para entornos de desarrollo o tests.
type MockEmailService struct{}

// NewMockEmailService crea una nueva instancia del servicio de email simulado.
func NewMockEmailService() *MockEmailService {
	return &MockEmailService{}
}

// banner deja el correo simulado en el log, bien visible: el correo NO se
// envió y el dato que el usuario necesita (código, clave o token) se destaca.
func banner(destinatario, asunto, destacado string, lineas ...string) {
	log.Println("##################################################################")
	log.Println("📧 EMAIL_PROVIDER=mock · ESTE CORREO NO SE ENVIÓ, solo se muestra en este log")
	log.Printf("📥 Para: %s", destinatario)
	log.Printf("Asunto: %s", asunto)
	if destacado != "" {
		log.Printf("👉👉👉  %s  👈👈👈", destacado)
	}
	for _, linea := range lineas {
		log.Println(linea)
	}
	log.Println("##################################################################")
}

// EnviarCodigoRecuperacion simula el envío imprimiendo el código en la consola.
func (s *MockEmailService) EnviarCodigoRecuperacion(emailDestino, codigo string) error {
	banner(emailDestino, "El Centinela – Código de recuperación", "CÓDIGO DE RECUPERACIÓN: "+codigo,
		"Solicitaste restablecer tu contraseña.", "Este código expira en 10 minutos.")
	return nil
}

// EnviarCredencialesTemporales simula el envío de credenciales temporales por email.
func (s *MockEmailService) EnviarCredencialesTemporales(destinatario, nombre, contrasenaTemp string) error {
	banner(destinatario, "El Centinela – Credenciales de acceso", "CLAVE PROVISORIA: "+contrasenaTemp,
		"Hola "+nombre+",", "Se ha creado o restablecido tu cuenta en El Centinela.",
		"Esta clave es de un solo uso. El sistema exigirá su cambio al iniciar sesión.")
	return nil
}

// SendMail simula el envío de un correo genérico (ej. el token de cambio de email).
func (s *MockEmailService) SendMail(to, subject, body string) error {
	banner(to, subject, "", "Mensaje:", body)
	return nil
}

// EnviarAlertaCritica simula el envío del correo de una alerta crítica: deja en
// el log los destinatarios, el asunto y el cuerpo en texto plano.
func (s *MockEmailService) EnviarAlertaCritica(destinatarios []string, alerta ports.AlertaCorreo) error {
	asunto, texto, _ := ArmarCorreoAlerta(alerta)
	banner(strings.Join(destinatarios, ", "), asunto, "", "Mensaje:", texto)
	return nil
}
