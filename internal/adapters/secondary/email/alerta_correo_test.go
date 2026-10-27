package email

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"

	"el-centinela/internal/core/ports"
)

var instante = time.Date(2026, 10, 27, 15, 45, 0, 0, time.UTC)

func TestArmarCorreoAlerta_SaturacionDelHost(t *testing.T) {
	asunto, texto, cuerpo := ArmarCorreoAlerta(ports.AlertaCorreo{
		AlertaID: "0192e4a1-7c9b-7123-b123-000000000001", Tipo: "host_saturation", RecursoTipo: "HOST", Recurso: "proxmox",
		Metrica: "Memoria RAM", Valor: 89.2, Umbral: 85, Mensaje: "Saturación crítica: Uso de Memoria RAM en el host al 89.2% (límite 85%)", FechaHora: instante,
	})
	if asunto != "[ALERTA CRÍTICA] Saturación de Recursos en HOST proxmox" {
		t.Errorf("Asunto: %q", asunto)
	}
	for _, esperado := range []string{
		"Recurso afectado: HOST proxmox", "Métrica comprometida: Memoria RAM", "Valor registrado: 89.2% (umbral 85%)",
		"27/10/2026 15:45:00 UTC", "27/10/2026 12:45:00 (hora de Argentina)", "Recomendación: Revisar los procesos",
		"0192e4a1-7c9b-7123-b123-000000000001",
	} {
		if !strings.Contains(texto, esperado) {
			t.Errorf("Falta %q en el texto plano:\n%s", esperado, texto)
		}
	}
	for _, esperado := range []string{"HOST proxmox", "Memoria RAM", "89.2% (umbral 85%)", "15:45:00 UTC", "Revisar los procesos"} {
		if !strings.Contains(cuerpo, esperado) {
			t.Errorf("Falta %q en el HTML", esperado)
		}
	}
}

func TestArmarCorreoAlerta_CaidaDeInstancia(t *testing.T) {
	asunto, texto, _ := ArmarCorreoAlerta(ports.AlertaCorreo{Tipo: "instance_down", RecursoTipo: "VM", Recurso: "srv-database",
		Mensaje: "La instancia srv-database se detuvo sin una orden de El Centinela.", FechaHora: instante})
	if asunto != "[ALERTA CRÍTICA] Caída de Instancia en VM srv-database" {
		t.Errorf("Asunto: %q", asunto)
	}
	if strings.Contains(texto, "Métrica comprometida") || !strings.Contains(texto, "consola de Proxmox") {
		t.Errorf("Una caída no tiene métrica y trae su recomendación:\n%s", texto)
	}
}

// Un nombre de instancia no puede inyectar cabeceras en el asunto ni HTML en el cuerpo.
func TestArmarCorreoAlerta_NombresSeguros(t *testing.T) {
	asunto, _, cuerpo := ArmarCorreoAlerta(ports.AlertaCorreo{Tipo: "resource_saturation", RecursoTipo: "VM",
		Recurso: "srv\r\nBcc: atacante@x.com <script>alert(1)</script>", Metrica: "CPU", Valor: 97, Umbral: 95, FechaHora: instante})
	if strings.ContainsAny(asunto, "\r\n") {
		t.Errorf("El asunto no puede tener saltos de línea: %q", asunto)
	}
	if strings.Contains(cuerpo, "<script>") || !strings.Contains(cuerpo, "&lt;script&gt;") {
		t.Error("El HTML debe escapar el nombre de la instancia")
	}
}

// El simulado deja en el log los destinatarios, el asunto y el cuerpo.
func TestMockEmail_AlertaCriticaQuedaEnElLog(t *testing.T) {
	var salida bytes.Buffer
	anterior := log.Writer()
	log.SetOutput(&salida)
	defer log.SetOutput(anterior)
	err := NewMockEmailService().EnviarAlertaCritica([]string{"a@x.com", "b@x.com"}, ports.AlertaCorreo{Tipo: "host_saturation",
		RecursoTipo: "HOST", Recurso: "proxmox", Metrica: "CPU", Valor: 93, Umbral: 90, FechaHora: instante})
	if err != nil || !strings.Contains(salida.String(), "a@x.com, b@x.com") || !strings.Contains(salida.String(), "[ALERTA CRÍTICA] Saturación de Recursos en HOST proxmox") {
		t.Errorf("Log del mock: %v\n%s", err, salida.String())
	}
}
