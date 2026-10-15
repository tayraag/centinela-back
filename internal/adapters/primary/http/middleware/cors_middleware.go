// Package middleware contiene los middlewares HTTP del servidor Gin.
package middleware

import (
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// OrigenesPermitidos lee ALLOWED_ORIGINS (lista separada por comas, por
// ejemplo https://centinela.tail6bb3f3.ts.net,http://localhost:5173). Si no
// está definida, solo se permite el dominio de producción de Tailscale. La usan
// el CORS y el control de origen del WebSocket de métricas.
func OrigenesPermitidos() map[string]struct{} {
	allowed := make(map[string]struct{})
	for _, origin := range strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",") {
		if trimmed := strings.TrimSpace(origin); trimmed != "" {
			allowed[trimmed] = struct{}{}
		}
	}
	// Valor por defecto si la variable no está definida: solo producción.
	if len(allowed) == 0 {
		allowed["https://centinela.tail6bb3f3.ts.net"] = struct{}{}
	}
	return allowed
}

// CORS aplica la política de Cross-Origin Resource Sharing.
//
// Orígenes permitidos: se leen de la variable de entorno ALLOWED_ORIGINS
// como lista separada por comas. Si la variable no está definida, se usa
// el dominio de producción de Tailscale como valor por defecto seguro.
//
// Cabeceras que emite:
//   - Access-Control-Allow-Origin:      origen exacto (nunca comodín *)
//   - Access-Control-Allow-Credentials: true  (necesario para cookies HttpOnly)
//   - Access-Control-Allow-Methods:     GET, POST, PUT, DELETE, OPTIONS
//   - Access-Control-Allow-Headers:     Authorization, Content-Type, Accept, Origin
//   - Vary: Origin  (indica a cachés que la respuesta varía por origen)
//
// Las peticiones OPTIONS (preflight) se responden con 204 No Content
// y se corta la cadena de handlers para no llegar al handler de negocio.
func CORS() gin.HandlerFunc {
	allowed := OrigenesPermitidos()

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")

		// Si el request no trae Origin (ej: curl directo, Swagger local)
		// no se agregan cabeceras CORS y se continúa normalmente.
		if origin == "" {
			c.Next()
			return
		}

		// Verificar si el origen está en la lista blanca.
		_, permitido := allowed[origin]

		if permitido {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, Origin")
			// Vary: Origin es obligatorio cuando el Allow-Origin no es comodín,
			// para que los proxies/CDN no sirvan la respuesta de un origen a otro.
			c.Header("Vary", "Origin")
		}

		// Preflight OPTIONS: responder inmediatamente sin pasar al handler.
		if c.Request.Method == http.MethodOptions {
			if permitido {
				c.AbortWithStatus(http.StatusNoContent) // 204
			} else {
				// Origen no permitido: rechazar el preflight explícitamente.
				c.AbortWithStatus(http.StatusForbidden) // 403
			}
			return
		}

		c.Next()
	}
}
