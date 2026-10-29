package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// tamanoMaximoCuerpoAuth acota los cuerpos de /api/auth: ninguno necesita más
// que un correo, una contraseña (hasta 128 caracteres) y un código.
const tamanoMaximoCuerpoAuth = 16 * 1024

// tieneControl detecta CR, LF, NUL y el resto de los caracteres de control
// (los que permiten inyectar cabeceras o contaminar los logs). El tabulador se
// acepta solo en cabeceras, donde es un separador válido.
func tieneControl(texto string, permitirTab bool) bool {
	for _, r := range texto {
		if (r < 0x20 && !(permitirTab && r == '\t')) || r == 0x7F {
			return true
		}
	}
	return false
}

func rechazarEntrada(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
		"errorCode": "INVALID_REQUEST",
		"message":   "La petición contiene datos con un formato no permitido.",
	})
}

// RechazarCabecerasConControl responde 400 INVALID_REQUEST si alguna cabecera
// trae CR, LF, NUL u otro carácter de control (HTTP Header Injection, logs
// contaminados). El servidor de Go ya rechaza la mayoría al leer la petición;
// esta es la barrera explícita para todo lo que llega a la API.
func RechazarCabecerasConControl() gin.HandlerFunc {
	return func(c *gin.Context) {
		for nombre, valores := range c.Request.Header {
			if tieneControl(nombre, false) {
				rechazarEntrada(c)
				return
			}
			for _, valor := range valores {
				if tieneControl(valor, true) {
					rechazarEntrada(c)
					return
				}
			}
		}
		c.Next()
	}
}

// ValidarCuerpoAuth valida los cuerpos JSON de /api/auth antes de los límites
// y de los handlers: un único objeto plano (sin objetos ni listas anidadas),
// con textos sin caracteres de control y de hasta 16 KB. Un cuerpo vacío pasa
// (refresh y logout no llevan cuerpo). Si no cumple, 400 INVALID_REQUEST.
func ValidarCuerpoAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body == nil || c.Request.Body == http.NoBody {
			c.Next()
			return
		}
		contenido, err := io.ReadAll(io.LimitReader(c.Request.Body, tamanoMaximoCuerpoAuth+1))
		if err != nil || len(contenido) > tamanoMaximoCuerpoAuth {
			rechazarEntrada(c)
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(contenido))
		if len(bytes.TrimSpace(contenido)) == 0 {
			c.Next()
			return
		}
		if !cuerpoPlanoValido(contenido) {
			rechazarEntrada(c)
			return
		}
		c.Next()
	}
}

// cuerpoPlanoValido: un solo objeto JSON, sin nada después, cuyos valores son
// textos sin caracteres de control, números, booleanos o null.
func cuerpoPlanoValido(contenido []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(contenido))
	dec.UseNumber()
	var objeto map[string]any
	if err := dec.Decode(&objeto); err != nil || objeto == nil {
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return false // datos después del objeto
	}
	for clave, valor := range objeto {
		if tieneControl(clave, false) {
			return false
		}
		switch v := valor.(type) {
		case string:
			if tieneControl(v, false) {
				return false
			}
		case json.Number, bool, nil:
		default:
			return false // objeto o lista anidada
		}
	}
	return true
}
