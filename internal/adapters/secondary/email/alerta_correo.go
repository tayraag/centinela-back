package email

import (
	"fmt"
	"html"
	"strings"
	"time"

	"el-centinela/internal/core/ports"
)

// zonaHorariaLocal es la hora local que se muestra en el correo, junto a la UTC.
var zonaHorariaLocal = func() *time.Location {
	if zona, err := time.LoadLocation("America/Argentina/Buenos_Aires"); err == nil {
		return zona
	}
	return time.FixedZone("ART", -3*3600)
}()

// recomendaciones por tipo de alerta.
var recomendaciones = map[string]string{
	"host_saturation":     "Revisar los procesos en ejecución del host y apagar o migrar las instancias no prioritarias.",
	"resource_saturation": "Revisar los procesos de la instancia y, si la carga es esperable, ampliar su CPU o memoria.",
	"instance_down":       "Verificar en la consola de Proxmox por qué se detuvo la instancia (apagado manual, falta de recursos o error) y volver a encenderla si corresponde.",
}

// sinSaltos evita que un nombre con saltos de línea agregue cabeceras al correo.
func sinSaltos(texto string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(texto)
}

// ArmarCorreoAlerta devuelve el asunto y el cuerpo (texto plano y HTML) del
// correo de una alerta crítica. Lo comparten el envío SMTP y el simulado.
func ArmarCorreoAlerta(a ports.AlertaCorreo) (asunto, texto, cuerpoHTML string) {
	recurso := sinSaltos(strings.TrimSpace(a.RecursoTipo + " " + a.Recurso))
	motivo := "Saturación de Recursos"
	if a.Tipo == "instance_down" {
		motivo = "Caída de Instancia"
	}
	asunto = fmt.Sprintf("[ALERTA CRÍTICA] %s en %s", motivo, recurso)

	utc := a.FechaHora.UTC().Format("02/01/2006 15:04:05") + " UTC"
	local := a.FechaHora.In(zonaHorariaLocal).Format("02/01/2006 15:04:05") + " (hora de Argentina)"
	recomendacion := recomendaciones[a.Tipo]

	filas := [][2]string{{"Recurso afectado", recurso}}
	if a.Metrica != "" {
		filas = append(filas, [2]string{"Métrica comprometida", a.Metrica},
			[2]string{"Valor registrado", fmt.Sprintf("%.1f%% (umbral %.0f%%)", a.Valor, a.Umbral)})
	}
	filas = append(filas, [2]string{"Fecha y hora", utc + " / " + local}, [2]string{"Identificador de la alerta", a.AlertaID})

	var plano, tabla strings.Builder
	fmt.Fprintf(&plano, "%s\n\n%s\n\n", asunto, a.Mensaje)
	for _, f := range filas {
		fmt.Fprintf(&plano, "%s: %s\n", f[0], f[1])
		fmt.Fprintf(&tabla, `<tr><td style="padding:6px 12px;color:#555">%s</td><td style="padding:6px 12px"><strong>%s</strong></td></tr>`,
			html.EscapeString(f[0]), html.EscapeString(f[1]))
	}
	fmt.Fprintf(&plano, "\nRecomendación: %s\n\nEl Centinela – Sistema de Monitoreo\n", recomendacion)

	cuerpoHTML = fmt.Sprintf(`<!DOCTYPE html>
<html lang="es">
<head><meta charset="UTF-8"><title>%s</title></head>
<body style="font-family:Arial,sans-serif;color:#1a1a2e;background:#f5f5f5;padding:24px;">
  <div style="max-width:560px;margin:auto;background:#fff;border-radius:8px;padding:32px;box-shadow:0 2px 8px rgba(0,0,0,.08)">
    <h2 style="color:#b00020;margin-top:0">%s</h2>
    <p>%s</p>
    <table style="border-collapse:collapse;margin:16px 0">%s</table>
    <p><strong>Recomendación:</strong> %s</p>
    <p style="color:#888;font-size:12px;margin-top:32px">El Centinela – Sistema de Monitoreo</p>
  </div>
</body>
</html>`, html.EscapeString(asunto), html.EscapeString(asunto), html.EscapeString(a.Mensaje), tabla.String(), html.EscapeString(recomendacion))
	return asunto, plano.String(), cuerpoHTML
}
