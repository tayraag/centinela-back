package middleware

import (
	"net/http"
	"strconv"

	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// RequireInstanceAccess es una factory que retorna un gin.HandlerFunc que valida
// si el usuario autenticado tiene permiso para operar sobre una instancia Proxmox.
//
// Debe colocarse DESPUÉS de RequireAuth() en la cadena de middlewares, ya que
// depende de los valores "userID" y "rol" inyectados por ese middleware.
//
// Parámetros:
//   - repo: implementación de InstanceRepository para consultar permisos_instancia.
//   - paramName: nombre del parámetro de ruta que contiene el VMID (ej: "vmid").
//   - nivelRequerido: ports.NivelAccesoFullAccess o ports.NivelAccesoReadOnly —
//     el nivel mínimo que necesita el OPERATOR sobre ese vmid para pasar.
//
// Comportamiento:
//   - ADMIN → pasa siempre, sin consultar la base de datos.
//   - OPERATOR con nivel suficiente sobre el vmid → pasa.
//   - OPERATOR sin permiso, o con un nivel insuficiente → 403 INSTANCE_ACCESS_DENIED.
//   - VMID no parseable como entero → 400 INVALID_VMID.
//   - Error de BD → 500 INTERNAL_ERROR.
func RequireInstanceAccess(repo ports.InstanceRepository, paramName string, nivelRequerido string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1. Extraer rol del contexto (inyectado por RequireAuth)
		rolVal, exists := c.Get(ContextKeyRol)
		if !exists {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"errorCode": "NO_ROLE",
				"message":   "No se pudo determinar el rol del usuario.",
			})
			return
		}
		rol, _ := rolVal.(string)

		// 2. Los administradores tienen acceso implícito a todas las instancias.
		// No se realiza ninguna consulta a la base de datos.
		if rol == "ADMIN" {
			c.Next()
			return
		}

		// 3. Parsear el VMID desde el parámetro de ruta
		vmidStr := c.Param(paramName)
		vmid, err := strconv.Atoi(vmidStr)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"errorCode": "INVALID_VMID",
				"message":   "El identificador de instancia debe ser un número entero válido.",
			})
			return
		}

		// 4. Extraer userID del contexto (inyectado por RequireAuth)
		userIDVal, exists := c.Get(ContextKeyUserID)
		if !exists {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"errorCode": "NO_USER",
				"message":   "No se pudo determinar el usuario autenticado.",
			})
			return
		}
		userIDStr, _ := userIDVal.(string)
		userID, err := uuid.Parse(userIDStr)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"errorCode": "INVALID_USER_ID",
				"message":   "El identificador de usuario no es válido.",
			})
			return
		}

		// 5. Consultar permisos_instancia
		tieneAcceso, err := repo.VerificarAcceso(c.Request.Context(), userID, vmid, nivelRequerido)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"errorCode": "INTERNAL_ERROR",
				"message":   "Error al verificar permisos de acceso.",
			})
			return
		}

		// 6. Denegar si no tiene permiso
		if !tieneAcceso {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"errorCode": "INSTANCE_ACCESS_DENIED",
				"message":   "No tiene permiso para acceder a esta instancia.",
			})
			return
		}

		c.Next()
	}
}
