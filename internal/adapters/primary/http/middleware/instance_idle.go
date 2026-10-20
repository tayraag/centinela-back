package middleware

import (
	"errors"
	"log"
	"net/http"

	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
)

// RequireInstanceIdle es la guarda de exclusión mutua de las operaciones que
// modifican una instancia (energía, borrado y, cuando existan, configuración y
// snapshots): reserva la instancia antes del handler y la libera al terminar.
// Si otra operación la tiene tomada (lock Redis lock:instance:{vmid}:operation)
// o en curso (tarea RUNNING en tareas_asincronas), responde 409 INSTANCE_BUSY
// sin llegar al handler, así que no se contacta a Proxmox.
//
// Va después de ResolveInstanceRef, RequireInstanceAccess y RejectProtectedInstance: un pedido sin
// permiso nunca llega a tomar el lock.
func RequireInstanceIdle(exclusion ports.ExclusionInstancias) gin.HandlerFunc {
	return func(c *gin.Context) {
		vmid, ok := VmidResuelto(c)
		if !ok {
			AbortRecursoNoEncontrado(c)
			return
		}

		liberar, err := exclusion.Reservar(c.Request.Context(), vmid)
		switch {
		case errors.Is(err, ports.ErrInstanciaOcupada):
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{
				"errorCode": "INSTANCE_BUSY",
				"message":   "La instancia se encuentra ejecutando otra tarea. Aguarde a que finalice.",
			})
			return
		case err != nil:
			log.Printf("⚠️  [EXCLUSION] no se pudo reservar la instancia %d: %v", vmid, err)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"errorCode": "INSTANCE_LOCK_UNAVAILABLE",
				"message":   "No se pudo verificar si la instancia está ocupada. Intente nuevamente.",
			})
			return
		}
		defer liberar()
		c.Next()
	}
}
