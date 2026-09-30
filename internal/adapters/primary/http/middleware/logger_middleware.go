package middleware

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// RutaEventos es la ruta del stream SSE, que el logger trata aparte.
const RutaEventos = "/api/events"

// RequestLogger es un middleware Gin que loguea cada request/response de forma concisa.
// Muestra el método, ruta, status, duración y los campos clave del body (sin datos sensibles).
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		// Capturar y re-inyectar el body para poder leerlo sin consumirlo
		var bodySnippet string
		if c.Request.Body != nil && c.Request.ContentLength > 0 {
			bodyBytes, _ := io.ReadAll(c.Request.Body)
			c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
			bodySnippet = sanitizarBody(bodyBytes)
		}

		// El stream de eventos (SSE) queda abierto horas: no se captura su cuerpo
		// para no acumularlo en memoria. Solo se loguea la apertura y el cierre.
		if c.Request.URL.Path == RutaEventos {
			log.Printf("→ %s %s (stream SSE)", c.Request.Method, c.Request.URL.Path)
			c.Next()
			log.Printf("%s %d | %s | stream cerrado", statusEmoji(c.Writer.Status()), c.Writer.Status(), time.Since(start).Round(time.Second))
			return
		}

		// Capturar la respuesta
		blw := &bodyLogWriter{body: &bytes.Buffer{}, ResponseWriter: c.Writer}
		c.Writer = blw

		c.Next()

		duracion := time.Since(start)
		status := c.Writer.Status()
		emoji := statusEmoji(status)

		// Log del request
		if bodySnippet != "" {
			log.Printf("→ %s %s | body: %s", c.Request.Method, c.Request.URL.Path, bodySnippet)
		} else {
			log.Printf("→ %s %s", c.Request.Method, c.Request.URL.Path)
		}

		// Log de la respuesta
		respSnippet := sanitizarBody(blw.body.Bytes())
		log.Printf("%s %d | %s | resp: %s", emoji, status, duracion.Round(time.Millisecond), respSnippet)
	}
}

// bodyLogWriter captura el response body para poder loguearlo.
type bodyLogWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (w *bodyLogWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

// fragmentosSensibles: si el nombre de un campo contiene alguno de estos
// fragmentos (sin distinguir mayúsculas), su valor se oculta en el log.
// Cubre contrasena, contrasenaActual, contrasenaNueva, nuevaContrasena,
// password, secretoManual, secreto_totp_cifrado, qrBase64 (el QR contiene el
// secreto TOTP), codigo (TOTP / recuperación) y ticket (el de un solo uso para
// abrir el stream de eventos: quien lo lea del log podría usarlo).
var fragmentosSensibles = []string{"contrasena", "password", "secreto", "qrbase64", "codigo", "ticket"}

// camposJWT se truncan en vez de ocultarse, para poder distinguirlos en el log.
var camposJWT = map[string]bool{"jwtTemporal": true, "accessToken": true, "refreshToken": true}

// sanitizarBody parsea el JSON y oculta campos sensibles, retornando una versión compacta.
// Recorre objetos y arrays anidados, no solo el primer nivel.
func sanitizarBody(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return "<non-json>"
	}
	if arr, ok := v.([]interface{}); ok {
		// Los listados pueden ser largos: se loguea solo la cantidad.
		return fmt.Sprintf("<array de %d elementos>", len(arr))
	}

	compact, _ := json.Marshal(sanitizarValor(v))
	return string(compact)
}

func sanitizarValor(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		for campo, sub := range val {
			switch {
			case esSensible(campo):
				val[campo] = "***"
			case camposJWT[campo]:
				if str, ok := sub.(string); ok && len(str) > 30 {
					val[campo] = str[:20] + "…[jwt]"
				}
			default:
				val[campo] = sanitizarValor(sub)
			}
		}
		return val
	case []interface{}:
		for i, sub := range val {
			val[i] = sanitizarValor(sub)
		}
		return val
	default:
		return v
	}
}

func esSensible(campo string) bool {
	campo = strings.ToLower(campo)
	for _, fragmento := range fragmentosSensibles {
		if strings.Contains(campo, fragmento) {
			return true
		}
	}
	return false
}

// statusEmoji retorna un emoji según el código HTTP para lectura rápida en terminal.
func statusEmoji(status int) string {
	switch {
	case status < 300:
		return "✅"
	case status < 400:
		return "↪️ "
	case status == 401:
		return "🔒"
	case status == 403:
		return "🚫"
	case status < 500:
		return "⚠️ "
	default:
		return "❌"
	}
}
