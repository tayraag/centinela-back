package middleware

import "github.com/gin-gonic/gin"

// SecurityHeaders agrega los headers de seguridad básicos a las respuestas de la API.
// HSTS, nosniff, anti-clickjacking y política de referrer: son los mismos que debería
// mandar el proxy, pero acá quedan garantizados aunque el backend se exponga directo.
// Ojo: el front (SPA) los recibe desde nginx, no desde acá.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		headers := c.Writer.Header()
		headers.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		headers.Set("X-Content-Type-Options", "nosniff")
		headers.Set("X-Frame-Options", "DENY")
		headers.Set("Referrer-Policy", "no-referrer")
		c.Next()
	}
}
