package middleware

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// ParseVmidsProtegidos convierte PROXMOX_PROTECTED_VMIDS (VMIDs separados por
// coma, ej. "200,201,202") en el set de instancias de infraestructura que
// ninguna acción destructiva puede tocar. Los VMIDs varían según el servidor,
// así que no hay ninguno fijo: un valor vacío devuelve un set vacío y no se
// bloquea ninguna instancia. Una entrada inválida es un error: preferimos que
// la API no arranque antes que dejar una instancia desprotegida por un typo.
func ParseVmidsProtegidos(raw string) (map[int]bool, error) {
	protegidos := make(map[int]bool)
	for _, parte := range strings.Split(raw, ",") {
		parte = strings.TrimSpace(parte)
		if parte == "" {
			continue
		}
		vmid, err := strconv.Atoi(parte)
		if err != nil || vmid <= 0 {
			return nil, fmt.Errorf("VMID protegido inválido %q: debe ser un número entero positivo", parte)
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
//   - Sin VMID resuelto por ResolveInstanceRef → 404 RESOURCE_NOT_FOUND.
//   - Cualquier otro VMID → pasa.
func RejectProtectedInstance(protegidos map[int]bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		vmid, ok := VmidResuelto(c)
		if !ok {
			AbortRecursoNoEncontrado(c)
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
