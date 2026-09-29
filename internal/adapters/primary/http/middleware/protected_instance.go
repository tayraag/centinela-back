package middleware

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// VmidsProtegidosPorDefecto son las instancias que conforman la infraestructura
// del propio El Centinela (base de datos, API, front, etc.). Apagarlas deja el
// sistema fuera de servicio, así que ninguna acción destructiva puede tocarlas.
const VmidsProtegidosPorDefecto = "100,101,102,103,104,105"

// ParseVmidsProtegidos convierte una lista de VMIDs separados por coma (ej. el
// valor de PROXMOX_PROTECTED_VMIDS) en un set. Un valor vacío devuelve los
// VmidsProtegidosPorDefecto. Una entrada inválida es un error: preferimos que
// la API no arranque antes que dejar una instancia desprotegida por un typo.
func ParseVmidsProtegidos(raw string) (map[int]bool, error) {
	if strings.TrimSpace(raw) == "" {
		raw = VmidsProtegidosPorDefecto
	}

	protegidos := make(map[int]bool)
	for _, parte := range strings.Split(raw, ",") {
		parte = strings.TrimSpace(parte)
		if parte == "" {
			continue
		}
		vmid, err := strconv.Atoi(parte)
		if err != nil {
			return nil, fmt.Errorf("VMID protegido inválido %q: debe ser un número entero", parte)
		}
		protegidos[vmid] = true
	}
	return protegidos, nil
}

// RejectProtectedInstance bloquea acciones destructivas (stop, y a futuro
// shutdown/reboot/delete) sobre las instancias de infraestructura de El
// Centinela. Aplica a todos los roles, ADMIN incluido.
//
// Debe colocarse DESPUÉS de RequireInstanceAccess, para que un OPERATOR sin
// permiso sobre el vmid reciba INSTANCE_ACCESS_DENIED y no se entere de que la
// instancia está protegida.
//
// Comportamiento:
//   - VMID protegido → 403 INSTANCE_PROTECTED.
//   - VMID no parseable como entero → 400 INVALID_VMID.
//   - Cualquier otro VMID → pasa.
func RejectProtectedInstance(protegidos map[int]bool, paramName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		vmid, err := strconv.Atoi(c.Param(paramName))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"errorCode": "INVALID_VMID",
				"message":   "El identificador de instancia debe ser un número entero válido.",
			})
			return
		}

		if protegidos[vmid] {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"errorCode": "INSTANCE_PROTECTED",
				"message":   "Esta instancia forma parte de la infraestructura de El Centinela y no puede apagarse desde la API.",
			})
			return
		}

		c.Next()
	}
}
