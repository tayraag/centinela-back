package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/infrastructure/crypto"

	"github.com/gin-gonic/gin"
)

var refsPrueba, _ = crypto.NuevoOfuscadorVMID("salt-de-prueba-de-al-menos-32-caracteres!")

// ref devuelve la referencia pública de un VMID para armar rutas de prueba.
func ref(t *testing.T, vmid int) string {
	t.Helper()
	r, err := refsPrueba.OfuscarVMID(vmid)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestResolveInstanceRef_TraduceAlVmidInterno(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	vmid := 0
	r.GET("/instances/:instanceRef", middleware.ResolveInstanceRef(refsPrueba, "instanceRef"), func(c *gin.Context) {
		vmid, _ = middleware.VmidResuelto(c)
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/instances/"+ref(t, 110), nil))
	if w.Code != http.StatusOK || vmid != 110 {
		t.Errorf("Se esperaba 200 con vmid 110, vino %d con %d", w.Code, vmid)
	}
}

// Cualquier token que no se pueda descifrar responde exactamente lo mismo, sin
// pistas de por qué falló.
func TestResolveInstanceRef_TokenInvalidoEs404Uniforme(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	llego := false
	r.GET("/instances/:instanceRef", middleware.ResolveInstanceRef(refsPrueba, "instanceRef"), func(c *gin.Context) {
		llego = true
	})
	valido := ref(t, 110)
	alterado := []byte(valido)
	if alterado[8] == 'x' {
		alterado[8] = 'y'
	} else {
		alterado[8] = 'x'
	}
	otroServidor, _ := crypto.NuevoOfuscadorVMID("otro-salt-de-al-menos-32-caracteres-xx")
	deOtro, _ := otroServidor.OfuscarVMID(110)

	for _, token := range []string{"110", "abc", "inst_k8Pq2v", string(alterado), valido[:len(valido)-1], deOtro} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/instances/"+token, nil))
		var cuerpo map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &cuerpo)
		if w.Code != http.StatusNotFound || len(cuerpo) != 2 || cuerpo["errorCode"] != "RESOURCE_NOT_FOUND" || cuerpo["message"] != middleware.MensajeRecursoNoEncontrado {
			t.Errorf("%q: se esperaba 404 RESOURCE_NOT_FOUND, vino %d %v", token, w.Code, cuerpo)
		}
	}
	if llego {
		t.Error("Un token inválido no debe llegar al handler")
	}
}
