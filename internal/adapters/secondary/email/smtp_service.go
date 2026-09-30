package email

import (
	"fmt"
	"os"
	"strconv"

	gomail "github.com/go-mail/mail/v2"
)

// SMTPEmailService implementa ports.EmailService enviando correos reales mediante SMTP.
//
// Cifrado en tránsito:
//   - Puerto 587 → STARTTLS (go-mail lo negocia automáticamente, SSL=false por defecto).
//   - Puerto 465 → TLS directo (se activa poniendo SMTP_PORT=465; go-mail detecta SSL=true).
//
// Las credenciales se leen de variables de entorno al arrancar y NUNCA
// se imprimen en los logs para no exponer secretos.
type SMTPEmailService struct {
	host     string
	port     int
	user     string
	password string
	from     string
}

// NewSMTPEmailService construye el servicio leyendo la configuración del entorno.
// Falla en el arranque si SMTP_PORT no es un número válido.
func NewSMTPEmailService() (*SMTPEmailService, error) {
	portStr := os.Getenv("SMTP_PORT")
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("SMTP_PORT inválido (%q): %w", portStr, err)
	}
	return &SMTPEmailService{
		host:     os.Getenv("SMTP_HOST"),
		port:     port,
		user:     os.Getenv("SMTP_USER"),
		password: os.Getenv("SMTP_PASS"),
		from:     os.Getenv("SMTP_FROM"),
	}, nil
}

// EnviarCredencialesTemporales envía el correo de alta / reset administrativo.
// Incluye el nombre del destinatario para un mensaje institucional personalizado.
//
// Por qué personalizar con nombre: el mensaje institucional debe dirigirse
// a la persona y dejar claro que la clave es de un solo uso, cumpliendo
// con los requisitos de la especificación técnica.
func (s *SMTPEmailService) EnviarCredencialesTemporales(destinatario, nombre, contrasenaTemp string) error {
	subject := "El Centinela – Credenciales de acceso"

	// Cuerpo en texto plano (fallback) y HTML (cliente moderno).
	plain := fmt.Sprintf(
		"Hola %s,\n\nSe ha creado o restablecido tu cuenta en El Centinela.\n"+
			"Tu contraseña temporal es: %s\n\n"+
			"Esta clave es de un solo uso. El sistema te pedirá cambiarla al iniciar sesión.\n"+
			"Si no solicitaste este acceso, comunícate con el administrador del sistema.\n\n"+
			"El Centinela – Sistema de Monitoreo\n",
		nombre, contrasenaTemp,
	)

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="es">
<head><meta charset="UTF-8"><title>Credenciales de acceso – El Centinela</title></head>
<body style="font-family:Arial,sans-serif;color:#1a1a2e;background:#f5f5f5;padding:24px;">
  <div style="max-width:520px;margin:auto;background:#fff;border-radius:8px;padding:32px;box-shadow:0 2px 8px rgba(0,0,0,.08)">
    <h2 style="color:#0f3460;margin-top:0">El Centinela</h2>
    <p>Hola <strong>%s</strong>,</p>
    <p>Se ha creado o restablecido tu cuenta en <strong>El Centinela</strong>.</p>
    <p style="margin:24px 0;text-align:center">
      <span style="display:inline-block;background:#0f3460;color:#fff;font-size:20px;letter-spacing:4px;padding:12px 28px;border-radius:6px">%s</span>
    </p>
    <p>⚠️ Esta clave es <strong>de un solo uso</strong>. Al iniciar sesión se te pedirá cambiarla obligatoriamente.</p>
    <p style="color:#888;font-size:12px;margin-top:32px">Si no solicitaste este acceso, comunícate con el administrador del sistema.</p>
  </div>
</body>
</html>`, nombre, contrasenaTemp)

	return s.sendMail(destinatario, subject, plain, html)
}

// EnviarCodigoRecuperacion envía el código OTP de 6 dígitos para restablecer contraseña.
// La ventana de caducidad es de 10 minutos, tal como establece la especificación.
func (s *SMTPEmailService) EnviarCodigoRecuperacion(emailDestino, codigo string) error {
	subject := "El Centinela – Código de recuperación de contraseña"

	plain := fmt.Sprintf(
		"Hola,\n\nRecibiste este correo porque solicitaste restablecer tu contraseña en El Centinela.\n"+
			"Tu código de seguridad es: %s\n\n"+
			"Este código expira en 10 minutos. Si no lo usaste, podés ignorar este mensaje.\n"+
			"Nunca compartas este código con nadie.\n\n"+
			"El Centinela – Sistema de Monitoreo\n",
		codigo,
	)

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="es">
<head><meta charset="UTF-8"><title>Código de recuperación – El Centinela</title></head>
<body style="font-family:Arial,sans-serif;color:#1a1a2e;background:#f5f5f5;padding:24px;">
  <div style="max-width:520px;margin:auto;background:#fff;border-radius:8px;padding:32px;box-shadow:0 2px 8px rgba(0,0,0,.08)">
    <h2 style="color:#0f3460;margin-top:0">El Centinela</h2>
    <p>Solicitaste restablecer tu contraseña. Usá el siguiente código:</p>
    <p style="margin:24px 0;text-align:center">
      <span style="display:inline-block;background:#0f3460;color:#fff;font-size:32px;letter-spacing:8px;padding:16px 32px;border-radius:6px;font-weight:bold">%s</span>
    </p>
    <p>⏱️ Este código expira en <strong>10 minutos</strong>.</p>
    <p>Si no solicitaste este cambio, podés ignorar este mensaje.</p>
    <p style="color:#c00;font-size:13px"><strong>Nunca compartas este código con nadie.</strong></p>
    <p style="color:#888;font-size:12px;margin-top:32px">El Centinela – Sistema de Monitoreo</p>
  </div>
</body>
</html>`, codigo)

	return s.sendMail(emailDestino, subject, plain, html)
}

// SendMail envía un correo genérico de texto plano. Satisface ports.EmailService.
func (s *SMTPEmailService) SendMail(to, subject, body string) error {
	return s.sendMail(to, subject, body, "")
}

// sendMail es el método interno que construye y despacha el mensaje.
//
// Por qué go-mail sobre net/smtp:
//   - Multipart MIME (text/plain + text/html) de forma nativa.
//   - STARTTLS y SSL/TLS directo configurables con un campo booleano.
//   - API limpia: no hay que construir headers RFC 822 a mano.
//
// Cifrado: go-mail negocia STARTTLS en el puerto 587 por defecto (SSL=false).
// Si el puerto es 465, ponemos d.SSL=true para usar TLS directo desde el inicio
// de la conexión, sin negociación de STARTTLS.
func (s *SMTPEmailService) sendMail(to, subject, plainBody, htmlBody string) error {
	m := gomail.NewMessage()
	m.SetHeader("From", s.from)
	m.SetHeader("To", to)
	m.SetHeader("Subject", subject)

	if htmlBody != "" {
		// Multipart: primero texto plano (fallback), luego HTML (preferido por clientes modernos).
		m.SetBody("text/plain", plainBody)
		m.AddAlternative("text/html", htmlBody)
	} else {
		m.SetBody("text/plain", plainBody)
	}

	d := gomail.NewDialer(s.host, s.port, s.user, s.password)

	// Puerto 465 → TLS directo desde la conexión inicial (SMTPS).
	// Puerto 587 → go-mail usa STARTTLS por defecto (d.SSL permanece false).
	if s.port == 465 {
		d.SSL = true
	}

	return d.DialAndSend(m)
}
