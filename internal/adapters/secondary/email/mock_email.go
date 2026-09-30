package email

import (
	"log"
)

// MockEmailService implementa ports.EmailService simulando el envío mediante logs en consola.
// Ideal para entornos de desarrollo o tests.
type MockEmailService struct{}

// NewMockEmailService crea una nueva instancia del servicio de email simulado.
func NewMockEmailService() *MockEmailService {
	return &MockEmailService{}
}

// EnviarCodigoRecuperacion simula el envío imprimiendo el código en la consola.
func (s *MockEmailService) EnviarCodigoRecuperacion(emailDestino, codigo string) error {
	log.Println("==================================================")
	log.Println("📧 SIMULACIÓN DE ENVÍO DE CORREO (MOCK SMTP)")
	log.Printf("📥 Destinatario: %s\n", emailDestino)
	log.Println("Asunto: El Centinela – Código de recuperación")
	log.Println("Mensaje:")
	log.Println("Solicitaste restablecer tu contraseña.")
	log.Printf("Tu código de seguridad es: ** %s **\n", codigo)
	log.Println("Este código expira en 10 minutos.") 
	log.Println("==================================================")
	return nil
}

// EnviarCredencialesTemporales simula el envío de credenciales temporales por email.
func (s *MockEmailService) EnviarCredencialesTemporales(destinatario, nombre, contrasenaTemp string) error {
	log.Println("==================================================")
	log.Println("📧 SIMULACIÓN DE ENVÍO DE CORREO (MOCK SMTP)")
	log.Printf("📥 Destinatario: %s\n", destinatario)
	log.Println("Asunto: El Centinela – Credenciales de acceso")
	log.Println("Mensaje:")
	log.Printf("Hola %s,\n", nombre)
	log.Println("Se ha creado o restablecido tu cuenta en El Centinela.")
	log.Printf("Tu clave provisoria es: ** %s **\n", contrasenaTemp)
	log.Println("Esta clave es de un solo uso. El sistema exigirá su cambio al iniciar sesión.")
	log.Println("==================================================")
	return nil
}

// SendMail simula el envío de un correo genérico.
func (s *MockEmailService) SendMail(to, subject, body string) error {
	log.Println("==================================================")
	log.Println("📧 SIMULACIÓN DE ENVÍO DE CORREO GENÉRICO (MOCK SMTP)")
	log.Printf("📥 Destinatario: %s\n", to)
	log.Printf("Asunto: %s\n", subject)
	log.Printf("Mensaje:\n%s\n", body)
	log.Println("==================================================")
	return nil
}
