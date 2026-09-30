package ports

// EmailService define el contrato para el envío de correos electrónicos.
type EmailService interface {
	// EnviarCodigoRecuperacion envía un correo al usuario con su código temporal de 6 dígitos.
	EnviarCodigoRecuperacion(emailDestino, codigo string) error

	// EnviarCredencialesTemporales envía un correo al usuario con su contraseña de un solo uso.
	EnviarCredencialesTemporales(destinatario, nombre, contrasenaTemp string) error

	// SendMail envía un correo al usuario.
	SendMail(to, subject, body string) error
}
