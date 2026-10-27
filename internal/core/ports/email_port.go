package ports

import (
	"context"
	"time"
)

// EmailService define el contrato para el envío de correos electrónicos.
type EmailService interface {
	// EnviarCodigoRecuperacion envía un correo al usuario con su código temporal de 6 dígitos.
	EnviarCodigoRecuperacion(emailDestino, codigo string) error

	// EnviarCredencialesTemporales envía un correo al usuario con su contraseña de un solo uso.
	EnviarCredencialesTemporales(destinatario, nombre, contrasenaTemp string) error

	// SendMail envía un correo al usuario.
	SendMail(to, subject, body string) error

	// EnviarAlertaCritica envía a cada destinatario el correo estructurado de
	// una alerta CRITICAL (asunto, recurso, métrica, valor contra umbral, hora y
	// recomendación). Devuelve los errores de los envíos que fallaron.
	EnviarAlertaCritica(destinatarios []string, alerta AlertaCorreo) error
}

// AlertaCorreo son los datos de una alerta crítica para el correo.
type AlertaCorreo struct {
	AlertaID    string
	Tipo        string // host_saturation | resource_saturation | instance_down
	RecursoTipo string // HOST | VM | LXC
	Recurso     string // nombre del nodo o de la instancia
	Metrica     string // nombre legible ("Memoria RAM"); vacío en instance_down
	Valor       float64
	Umbral      float64
	Mensaje     string
	FechaHora   time.Time
}

// DestinatariosAlertas devuelve a quién mandar el correo de una alerta.
type DestinatariosAlertas interface {
	// EmailsParaAlerta devuelve los correos de los ADMIN activos y, si vmid >
	// 0, también de los OPERATOR activos con FULL_ACCESS sobre esa instancia.
	EmailsParaAlerta(ctx context.Context, vmid int) ([]string, error)
}
