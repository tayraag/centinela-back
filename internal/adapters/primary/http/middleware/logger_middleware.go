package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"time"

	"github.com/gin-gonic/gin"
)

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

// sanitizarBody parsea el JSON y oculta campos sensibles, retornando una versión compacta.
func sanitizarBody(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return "<non-json>"
	}

	// Ocultar campos sensibles
	camposSensibles := []string{"contrasena", "password", "secreto_totp_cifrado", "contrasenaHash"}
	for _, campo := range camposSensibles {
		if _, ok := m[campo]; ok {
			m[campo] = "***"
		}
	}

	// Truncar tokens JWT largos
	camposJWT := []string{"jwtTemporal", "accessToken", "refreshToken", "refreshToken"}
	for _, campo := range camposJWT {
		if val, ok := m[campo]; ok {
			if str, ok := val.(string); ok && len(str) > 30 {
				m[campo] = str[:20] + "…[jwt]"
			}
		}
	}

	compact, _ := json.Marshal(m)
	return string(compact)
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
