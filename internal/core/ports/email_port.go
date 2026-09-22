package ports

// EmailService define el contrato para el envío de correos electrónicos.
type EmailService interface {
	// EnviarCodigoRecuperacion envía un correo al usuario con su código temporal de 6 dígitos.
	EnviarCodigoRecuperacion(emailDestino, codigo string) error

	// EnviarContrasenaTemporal envía un correo al usuario con su contraseña de un solo uso.
	EnviarContrasenaTemporal(emailDestino, contrasena string) error
}
