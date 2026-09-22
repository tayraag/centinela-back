package email

import (
	"log"
)

// MockEmailService implementa ports.EmailService simulando el envío mediante logs en consola.
// Ideal para entornos de desarrollo o hasta que se configure un servidor SMTP real.
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
	log.Println("Asunto: Código de recuperación de contraseña")
	log.Println("Mensaje:")
	log.Println("Hola, solicitaste restablecer tu contraseña.")
	log.Printf("Tu código de seguridad temporal es: ** %s **\n", codigo)
	log.Println("Este código expira en 15 minutos.")
	log.Println("==================================================")
	
	// Retornamos nil simulando que el correo se envió con éxito
	return nil
}

// EnviarContrasenaTemporal simula el envío de la clave temporal por email.
func (s *MockEmailService) EnviarContrasenaTemporal(emailDestino, contrasena string) error {
	log.Println("==================================================")
	log.Println("📧 SIMULACIÓN DE ENVÍO DE CORREO (MOCK SMTP)")
	log.Printf("📥 Destinatario: %s\n", emailDestino)
	log.Println("Asunto: Tu nueva cuenta / Restablecimiento de clave")
	log.Println("Mensaje:")
	log.Println("Se ha generado una clave temporal de acceso para tu cuenta.")
	log.Printf("Tu clave provisoria es: ** %s **\n", contrasena)
	log.Println("Por motivos de seguridad, el sistema exigirá su cambio obligatorio en el primer inicio de sesión.")
	log.Println("==================================================")
	
	// Retornamos nil simulando éxito
	return nil
}
