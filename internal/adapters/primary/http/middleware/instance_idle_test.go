package middleware_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
)

// exclusionFalsa responde lo que el test indique y cuenta reservas y liberaciones.
type exclusionFalsa struct {
	err       error
	vmid      int
	liberadas int
}

func (f *exclusionFalsa) Reservar(_ context.Context, vmid int) (func(), error) {
	f.vmid = vmid
	if f.err != nil {
		return nil, f.err
	}
	return func() { f.liberadas++ }, nil
}

func pedirConGuarda(excl *exclusionFalsa, ruta string) (*httptest.ResponseRecorder, bool) {
	gin.SetMode(gin.TestMode)
	llegoAlHandler := false
	r := gin.New()
	r.POST("/api/instances/:instanceRef/start", middleware.ResolveInstanceRef(refsPrueba, "instanceRef"), middleware.RequireInstanceIdle(excl), func(c *gin.Context) {
		llegoAlHandler = true
		c.Status(http.StatusAccepted)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, ruta, nil))
	return w, llegoAlHandler
}

func TestRequireInstanceIdle_OcupadaEs409SinLlegarAlHandler(t *testing.T) {
	excl := &exclusionFalsa{err: ports.ErrInstanciaOcupada}
	w, llego := pedirConGuarda(excl, "/api/instances/"+ref(t, 110)+"/start")
	var cuerpo map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &cuerpo)
	esperado := map[string]string{"errorCode": "INSTANCE_BUSY", "message": "La instancia se encuentra ejecutando otra tarea. Aguarde a que finalice."}
	if w.Code != http.StatusConflict || len(cuerpo) != 2 || cuerpo["errorCode"] != esperado["errorCode"] || cuerpo["message"] != esperado["message"] {
		t.Errorf("Se esperaba 409 %v, vino %d %v", esperado, w.Code, cuerpo)
	}
	if llego {
		t.Error("Con la instancia ocupada no debe llegar al handler (ni a Proxmox)")
	}
	if excl.vmid != 110 {
		t.Errorf("Debe reservar el vmid de la ruta, reservó %d", excl.vmid)
	}
}

func TestRequireInstanceIdle_OciosaPasaYLibera(t *testing.T) {
	excl := &exclusionFalsa{}
	w, llego := pedirConGuarda(excl, "/api/instances/"+ref(t, 110)+"/start")
	if w.Code != http.StatusAccepted || !llego {
		t.Errorf("Una instancia ociosa debe pasar al handler: %d", w.Code)
	}
	if excl.liberadas != 1 {
		t.Errorf("Al terminar el handler debe liberarse la reserva una vez, se liberó %d", excl.liberadas)
	}
}

func TestRequireInstanceIdle_SinPoderVerificarEs503(t *testing.T) {
	excl := &exclusionFalsa{err: errors.Join(ports.ErrExclusionNoDisponible, errors.New("conexión rechazada"))}
	w, llego := pedirConGuarda(excl, "/api/instances/"+ref(t, 110)+"/start")
	if w.Code != http.StatusServiceUnavailable || llego {
		t.Errorf("Sin poder verificar se esperaba 503 sin despachar: %d (handler=%v)", w.Code, llego)
	}
}

func TestRequireInstanceIdle_ReferenciaInvalidaEs404SinReservar(t *testing.T) {
	excl := &exclusionFalsa{}
	if w, llego := pedirConGuarda(excl, "/api/instances/110/start"); w.Code != http.StatusNotFound || llego || excl.vmid != 0 {
		t.Errorf("referencia inválida: se esperaba 404 sin reservar, vino %d (reservó %d)", w.Code, excl.vmid)
	}
}
