package middleware

import "github.com/gin-gonic/gin"

// CabeceraProveedorEmail avisa en las respuestas de los flujos que mandan
// correo que el correo no salió a la red (EMAIL_PROVIDER=mock).
const CabeceraProveedorEmail = "X-Centinela-Email-Provider"

// AvisarProveedorEmail estampa X-Centinela-Email-Provider: mock en la respuesta
// cuando el correo es simulado, para que un 200 "correo enviado" no se tome por
// una entrega real. Con SMTP no agrega nada. Va en las rutas que mandan correo.
func AvisarProveedorEmail(proveedor string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if proveedor == "mock" {
			c.Header(CabeceraProveedorEmail, proveedor)
		}
		c.Next()
	}
}
