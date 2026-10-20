package http

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Política de divulgación cero: lo que sale hacia el cliente (errores HTTP,
// eventos SSE) nunca lleva trazas del host ni detalles internos de Proxmox
// (rutas del sistema de archivos, módulos Perl, números de línea, UPID). El
// texto original queda solo en el log del servidor y en la auditoría.

// MensajeErrorHipervisor es el texto canónico cuando de un error de Proxmox no
// queda nada que se pueda mostrar.
const MensajeErrorHipervisor = "No se pudo completar la operación en el hipervisor. Contacte al administrador."

// largoMaximoTextoCliente acota un texto externo que llega al cliente.
const largoMaximoTextoCliente = 300

var (
	// " at /usr/share/perl5/PVE/API2/Qemu.pm line 450." (cola de un die de Perl)
	trazaPerl = regexp.MustCompile(`\s*\bat\s+\S+\s+line\s+\d+\.?`)
	// Rutas absolutas bajo los directorios del sistema, y cualquier archivo Perl.
	rutaSistema = regexp.MustCompile(`/(?:usr|var|etc|run|tmp|opt|home|root|proc|sys|dev|lib|lib64|bin|sbin|srv|mnt|boot)(?:/[^\s'",;:)\]]*|\b)|\S*\.p[ml]\b`)
	// "[ruta] line 450." cuando la traza no trae el "at" adelante.
	lineaDeRuta = regexp.MustCompile(`\[ruta\]\s+line\s+\d+\.?`)
	// Módulos Perl: PVE::API2::Qemu, PVE::Storage::LVMPlugin.
	moduloPerl = regexp.MustCompile(`\b[A-Za-z_]\w*(?:::\w+)+\b`)
	// UPID:pve1:0000ABCD:...: identificador técnico de la tarea en Proxmox.
	upidProxmox = regexp.MustCompile(`(?i)\bUPID:[^\s'",]*`)
	espacios    = regexp.MustCompile(`[ \t]+`)
	// Lo que queda vacío después de limpiar: comillas, separadores, puntuación.
	sinContenido = regexp.MustCompile(`^[\s'"\-:;,.()\[\]]*$`)
)

// SanearTexto devuelve un texto externo (el error de Proxmox) apto para el
// cliente: primera línea útil, sin trazas, rutas, módulos ni UPID. Si no queda
// nada que mostrar devuelve MensajeErrorHipervisor.
func SanearTexto(texto string) string {
	if texto == "" {
		return texto
	}
	limpio := texto
	// Una traza de varias líneas: solo la primera línea con contenido.
	for _, linea := range strings.Split(limpio, "\n") {
		if strings.TrimSpace(linea) != "" {
			limpio = linea
			break
		}
	}
	limpio = trazaPerl.ReplaceAllString(limpio, "")
	limpio = upidProxmox.ReplaceAllString(limpio, "[tarea]")
	limpio = rutaSistema.ReplaceAllString(limpio, "[ruta]")
	limpio = lineaDeRuta.ReplaceAllString(limpio, "[ruta]")
	limpio = moduloPerl.ReplaceAllString(limpio, "[módulo]")
	limpio = strings.TrimSpace(espacios.ReplaceAllString(limpio, " "))
	limpio = strings.TrimRight(limpio, " :;,")
	if sinContenido.MatchString(limpio) || soloMarcadores(limpio) {
		return MensajeErrorHipervisor
	}
	if utf8.RuneCountInString(limpio) > largoMaximoTextoCliente {
		limpio = string([]rune(limpio)[:largoMaximoTextoCliente]) + "…"
	}
	return limpio
}

// soloMarcadores: después de limpiar solo quedaron reemplazos ("[ruta]").
func soloMarcadores(texto string) bool {
	for _, m := range []string{"[tarea]", "[ruta]", "[módulo]"} {
		texto = strings.ReplaceAll(texto, m, "")
	}
	return sinContenido.MatchString(texto)
}

// clavesInternas son campos de depuración que nunca viajan al cliente, aunque
// alguien los agregue a los detalles de un evento.
var clavesInternas = map[string]bool{
	"upid": true, "rawoutput": true, "raw": true, "stack": true, "stacktrace": true,
	"trace": true, "traza": true, "socket": true, "debug": true,
}

// sanearDetalles devuelve una copia de los detalles de un evento sin campos
// internos y con todos los textos saneados (también dentro de mapas y listas).
func sanearDetalles(detalles map[string]any) map[string]any {
	if detalles == nil {
		return nil
	}
	copia := make(map[string]any, len(detalles))
	for k, v := range detalles {
		if clavesInternas[strings.ToLower(k)] {
			continue
		}
		copia[k] = sanearValor(v)
	}
	return copia
}

func sanearValor(valor any) any {
	switch v := valor.(type) {
	case string:
		return SanearTexto(v)
	case map[string]any:
		return sanearDetalles(v)
	case []any:
		copia := make([]any, len(v))
		for i, elem := range v {
			copia[i] = sanearValor(elem)
		}
		return copia
	}
	return valor
}
