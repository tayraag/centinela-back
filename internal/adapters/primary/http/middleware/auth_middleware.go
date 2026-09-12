package middleware

import (
	"net/http"
	"os"
	"strings"

	"el-centinela/internal/infrastructure/crypto"

	"github.com/gin-gonic/gin"
)

// ContextKey es el tipo para las claves del contexto de Gin.
type ContextKey string

const (
	// ContextKeyJTI es la clave donde se guarda el JTI del token en el contexto de Gin.
	ContextKeyJTI = "jti"
	// ContextKeyUserID es la clave donde se guarda el UserID en el contexto de Gin.
	ContextKeyUserID = "userID"
	// ContextKeyRol es la clave donde se guarda el rol del usuario en el contexto de Gin.
	ContextKeyRol = "rol"
)

// RequirePreAuth valida que la petición tenga un JWT temporal válido (tipo "pre-auth").
// Se usa en las rutas del flujo 2FA: GET /2fa/qr y POST /2fa/verify.
// Inyecta el JTI en el contexto de Gin para uso posterior.
func RequirePreAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr := extraerBearer(c)
		if tokenStr == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"errorCode": "MISSING_TOKEN",
				"message":   "Se requiere un token de autenticación.",
			})
			return
		}

		jwtSecret := os.Getenv("JWT_SECRET")
		claims, err := crypto.VerificarToken(tokenStr, jwtSecret)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"errorCode": "INVALID_TOKEN",
				"message":   "Token inválido o expirado.",
			})
			return
		}

		if claims.Tipo != crypto.TipoPreAuth {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"errorCode": "WRONG_TOKEN_TYPE",
				"message":   "Se requiere un token pre-autenticación para esta ruta.",
			})
			return
		}

		// Inyectar datos en el contexto para los handlers
		c.Set(ContextKeyJTI, claims.ID)
		c.Set(ContextKeyUserID, claims.Subject)
		c.Set(ContextKeyRol, claims.Rol)
		c.Next()
	}
}

// RequireAuth valida que la petición tenga un JWT de acceso válido (tipo "access")
// con 2FA verificado. Es el middleware principal para rutas protegidas.
func RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr := extraerBearer(c)
		if tokenStr == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"errorCode": "MISSING_TOKEN",
				"message":   "Se requiere un token de autenticación.",
			})
			return
		}

		jwtSecret := os.Getenv("JWT_SECRET")
		claims, err := crypto.VerificarToken(tokenStr, jwtSecret)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"errorCode": "INVALID_TOKEN",
				"message":   "Token inválido o expirado.",
			})
			return
		}

		if claims.Tipo != crypto.TipoAccess {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"errorCode": "WRONG_TOKEN_TYPE",
				"message":   "Se requiere un access token para esta ruta.",
			})
			return
		}

		if !claims.Verificado2FA {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"errorCode": "2FA_REQUIRED",
				"message":   "Se requiere completar la verificación en dos pasos.",
			})
			return
		}

		c.Set(ContextKeyJTI, claims.ID)
		c.Set(ContextKeyUserID, claims.Subject)
		c.Set(ContextKeyRol, claims.Rol)
		c.Next()
	}
}

// RequireRole verifica que el usuario autenticado tenga uno de los roles especificados.
// Debe usarse DESPUÉS de RequireAuth en la cadena de middlewares.
func RequireRole(roles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		rolActual, exists := c.Get(ContextKeyRol)
		if !exists {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"errorCode": "NO_ROLE",
				"message":   "No se pudo determinar el rol del usuario.",
			})
			return
		}

		rolStr, ok := rolActual.(string)
		if !ok {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"errorCode": "INVALID_ROLE",
				"message":   "El rol del usuario no es válido.",
			})
			return
		}

		for _, r := range roles {
			if strings.EqualFold(rolStr, r) {
				c.Next()
				return
			}
		}

		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"errorCode": "INSUFFICIENT_PERMISSIONS",
			"message":   "No tiene permisos para realizar esta acción.",
		})
	}
}

// extraerBearer extrae el token Bearer del header Authorization.
func extraerBearer(c *gin.Context) string {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		return ""
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
