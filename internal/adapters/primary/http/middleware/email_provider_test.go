package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"el-centinela/internal/adapters/primary/http/middleware"

	"github.com/gin-gonic/gin"
)

// DoD: con el correo simulado, las respuestas de los flujos de correo llevan
// X-Centinela-Email-Provider: mock (también en un error); con SMTP, nada.
func TestAvisarProveedorEmail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for proveedor, esperado := range map[string]string{"mock": "mock", "smtp": ""} {
		for _, codigo := range []int{http.StatusOK, http.StatusBadRequest} {
			r := gin.New()
			r.POST("/forgot", middleware.AvisarProveedorEmail(proveedor), func(c *gin.Context) {
				c.JSON(codigo, gin.H{"message": "Si el correo existe, se envió un código."})
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/forgot", nil))
			if got := w.Header().Get(middleware.CabeceraProveedorEmail); got != esperado || w.Code != codigo {
				t.Errorf("%s, %d: cabecera %q, se esperaba %q", proveedor, codigo, got, esperado)
			}
		}
	}
}
