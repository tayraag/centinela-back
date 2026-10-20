package middleware

import (
	"net/http"

	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
)

// ContextKeyVmid es la clave donde ResolveInstanceRef guarda el VMID interno.
const ContextKeyVmid = "vmid"

// Respuesta única para "esa instancia no existe": la usan tanto una referencia
// que no se puede descifrar como una instancia que Proxmox no encuentra, así el
// error no sirve de oráculo para probar tokens.
const (
	CodigoRecursoNoEncontrado  = "RESOURCE_NOT_FOUND"
	MensajeRecursoNoEncontrado = "La instancia solicitada no existe."
)

// ResolveInstanceRef traduce la referencia pública de la ruta (:instanceRef,
// "inst_...") al VMID interno y lo deja en el contexto para los middlewares y
// el handler que siguen (ver VmidResuelto). Va primero en la cadena de las
// rutas de una instancia, antes de los permisos.
//
// Un token alterado, inventado, de otro servidor o un VMID numérico responde
// 404 RESOURCE_NOT_FOUND, igual que una instancia que no existe en Proxmox.
func ResolveInstanceRef(refs ports.ReferenciasInstancia, param string) gin.HandlerFunc {
	return func(c *gin.Context) {
		vmid, err := refs.DesofuscarVMID(c.Param(param))
		if err != nil || vmid <= 0 {
			AbortRecursoNoEncontrado(c)
			return
		}
		c.Set(ContextKeyVmid, vmid)
		c.Next()
	}
}

// VmidResuelto devuelve el VMID que dejó ResolveInstanceRef.
func VmidResuelto(c *gin.Context) (int, bool) {
	valor, existe := c.Get(ContextKeyVmid)
	vmid, ok := valor.(int)
	return vmid, existe && ok && vmid > 0
}

// AbortRecursoNoEncontrado corta con 404 RESOURCE_NOT_FOUND.
func AbortRecursoNoEncontrado(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
		"errorCode": CodigoRecursoNoEncontrado,
		"message":   MensajeRecursoNoEncontrado,
	})
}
