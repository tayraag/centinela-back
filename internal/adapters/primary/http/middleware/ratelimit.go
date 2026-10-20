package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	ventanaLogin        = 15 * time.Minute
	limiteLogin   int64 = 5
	ventana2FA          = 5 * time.Minute
	limite2FA     int64 = 3
	ventanaForgot       = time.Hour
	limiteForgot  int64 = 3

	contextKeyUltimoIntento2FA = "rateLimit2FAUltimoIntento"
)

// LimitarLogin reserva un lugar en la ventana antes de ejecutar el handler y
// lo revierte salvo que la respuesta sea 401. Así solo persisten los intentos
// fallidos y el sexto request se rechaza incluso bajo concurrencia.
func LimitarLogin(store ports.KeyValueStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !store.EsDistribuido() {
			responderRateLimitNoDisponible(c)
			return
		}
		email := emailNormalizadoDelBody(c)
		if email == "" {
			c.Next()
			return
		}
		key := "ratelimit:login:" + huella(c.ClientIP()+"\x00"+email)
		member := uuid.NewString()
		registro, err := store.RegistrarEnVentana(c.Request.Context(), key, member, time.Now(), ventanaLogin, limiteLogin)
		if err != nil {
			responderRateLimitNoDisponible(c)
			return
		}
		if !registro.Permitido {
			responderDemasiadosIntentos(c, int(ventanaLogin.Seconds()), "Demasiados intentos fallidos. Intente nuevamente en 15 minutos.")
			return
		}

		c.Next()
		if c.Writer.Status() != http.StatusUnauthorized {
			_ = store.EliminarDeVentana(c.Request.Context(), key, member)
		}
	}
}

// LimitarVerificacion2FA mantiene una ventana por JTI pre-auth. El tercer
// intento queda marcado en el contexto para que el handler devuelva
// TOKEN_REVOKED si la verificación falla.
func LimitarVerificacion2FA(store ports.KeyValueStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !store.EsDistribuido() {
			responderRateLimitNoDisponible(c)
			return
		}
		jti, _ := c.Get(ContextKeyJTI)
		jtiStr, _ := jti.(string)
		if jtiStr == "" {
			c.Next()
			return
		}
		key := "ratelimit:2fa:" + jtiStr
		member := uuid.NewString()
		registro, err := store.RegistrarEnVentana(c.Request.Context(), key, member, time.Now(), ventana2FA, limite2FA)
		if err != nil {
			responderRateLimitNoDisponible(c)
			return
		}
		if !registro.Permitido {
			_ = store.Del(c.Request.Context(), "auth:pre2fa:"+jtiStr, key)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"errorCode": "TOKEN_REVOKED",
				"message":   "La sesión temporal fue revocada por demasiados intentos fallidos.",
			})
			return
		}
		c.Set(contextKeyUltimoIntento2FA, registro.Cantidad >= limite2FA)

		c.Next()
		switch c.Writer.Status() {
		case http.StatusUnauthorized:
			if registro.Cantidad >= limite2FA {
				_ = store.Del(c.Request.Context(), "auth:pre2fa:"+jtiStr, key)
			}
		case http.StatusOK:
			_ = store.Del(c.Request.Context(), key)
		default:
			_ = store.EliminarDeVentana(c.Request.Context(), key, member)
		}
	}
}

// UltimoIntento2FA informa al handler que un error de validación debe responder
// TOKEN_REVOKED porque es el tercer fallo de la sesión temporal.
func UltimoIntento2FA(c *gin.Context) bool {
	valor, _ := c.Get(contextKeyUltimoIntento2FA)
	agotado, _ := valor.(bool)
	return agotado
}

// LimitarSolicitudRecuperacion aplica simultáneamente el máximo por IP y por
// correo destino. Si la segunda reserva falla, revierte la primera.
func LimitarSolicitudRecuperacion(store ports.KeyValueStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !store.EsDistribuido() {
			responderRateLimitNoDisponible(c)
			return
		}
		email := emailNormalizadoDelBody(c)
		if email == "" {
			c.Next()
			return
		}
		member := uuid.NewString()
		keyIP := "ratelimit:forgot:ip:" + huella(c.ClientIP())
		keyEmail := "ratelimit:forgot:email:" + huella(email)

		porIP, err := store.RegistrarEnVentana(c.Request.Context(), keyIP, member, time.Now(), ventanaForgot, limiteForgot)
		if err != nil {
			responderRateLimitNoDisponible(c)
			return
		}
		if !porIP.Permitido {
			responderDemasiadosIntentos(c, int(ventanaForgot.Seconds()), "Demasiadas solicitudes de recuperación. Intente nuevamente en 60 minutos.")
			return
		}
		porEmail, err := store.RegistrarEnVentana(c.Request.Context(), keyEmail, member, time.Now(), ventanaForgot, limiteForgot)
		if err != nil {
			_ = store.EliminarDeVentana(c.Request.Context(), keyIP, member)
			responderRateLimitNoDisponible(c)
			return
		}
		if !porEmail.Permitido {
			_ = store.EliminarDeVentana(c.Request.Context(), keyIP, member)
			responderDemasiadosIntentos(c, int(ventanaForgot.Seconds()), "Demasiadas solicitudes de recuperación. Intente nuevamente en 60 minutos.")
			return
		}
		c.Next()
	}
}

func emailNormalizadoDelBody(c *gin.Context) string {
	if c.Request.Body == nil {
		return ""
	}
	contenido, err := io.ReadAll(io.LimitReader(c.Request.Body, 64*1024))
	if err != nil {
		return ""
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(contenido))
	var body struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(contenido, &body) != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(body.Email))
}

func huella(valor string) string {
	suma := sha256.Sum256([]byte(valor))
	return hex.EncodeToString(suma[:])
}

func responderDemasiadosIntentos(c *gin.Context, retryAfter int, mensaje string) {
	c.Header("Retry-After", strconv.Itoa(retryAfter))
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
		"errorCode": "TOO_MANY_ATTEMPTS",
		"message":   mensaje,
	})
}

func responderRateLimitNoDisponible(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
		"errorCode": "RATE_LIMIT_UNAVAILABLE",
		"message":   "No se pudo validar el límite de intentos. Intente nuevamente.",
	})
}
