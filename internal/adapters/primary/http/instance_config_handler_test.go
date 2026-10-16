package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adaptersHttp "el-centinela/internal/adapters/primary/http"
	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type configuracionFalsa struct {
	llamadas         int
	cores, memoria   int
	requiereReinicio bool
	err              error
}

func (f *configuracionFalsa) Reconfigurar(_ context.Context, _, cores, memoriaMB int) (*ports.ResultadoReconfiguracion, error) {
	f.llamadas++
	f.cores, f.memoria = cores, memoriaMB
	if f.err != nil {
		return nil, f.err
	}
	return &ports.ResultadoReconfiguracion{RequiresReboot: f.requiereReinicio}, nil
}

func pedirConfig(f *configuracionFalsa, cuerpo string) (int, map[string]any) {
	gin.SetMode(gin.TestMode)
	h := adaptersHttp.NewInstanceConfigHandler(f, &mockAuditService{})
	r := gin.New()
	r.PUT("/api/instances/:vmid/config", func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUserID), uuid.New().String())
	}, h.ActualizarConfig)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/instances/110/config", strings.NewReader(cuerpo))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp
}

func TestConfig_PayloadInvalidoEs400SinTocarElServicio(t *testing.T) {
	for _, cuerpo := range []string{`{}`, `{"cores":0}`, `{"cores":-2}`, `{"ramMb":8}`, `{"cores":2.5}`, `{"ramMb":"4096"}`, `no es json`} {
		f := &configuracionFalsa{}
		if s, resp := pedirConfig(f, cuerpo); s != http.StatusBadRequest || resp["errorCode"] != "INVALID_REQUEST" || f.llamadas != 0 {
			t.Errorf("%s: se esperaba 400 INVALID_REQUEST sin llamar al servicio, vino %d %v (llamadas %d)", cuerpo, s, resp, f.llamadas)
		}
	}
}

func TestConfig_Respuestas(t *testing.T) {
	f := &configuracionFalsa{requiereReinicio: true}
	s, resp := pedirConfig(f, `{"cores":4,"ramMb":4096}`)
	if s != http.StatusOK || resp["requiresReboot"] != true || resp["message"] != "Recursos modificados; los cambios se aplicarán tras reiniciar la instancia." {
		t.Errorf("Pendiente de reinicio: %d %v", s, resp)
	}
	if f.cores != 4 || f.memoria != 4096 {
		t.Errorf("Debe pasar cores y ramMb al servicio: %d %d", f.cores, f.memoria)
	}

	f = &configuracionFalsa{}
	s, resp = pedirConfig(f, `{"ramMb":2048}`)
	if _, tieneMensaje := resp["message"]; s != http.StatusOK || resp["requiresReboot"] != false || tieneMensaje || f.cores != 0 {
		t.Errorf("Aplicado (solo RAM): %d %v cores=%d", s, resp, f.cores)
	}
}

func TestConfig_ErroresDelServicio(t *testing.T) {
	casos := []struct {
		err    error
		status int
		codigo string
	}{
		{fmt.Errorf("%w: RAM disponible insuficiente", ports.ErrRecursosInsuficientes), http.StatusConflict, "RESOURCE_QUOTA_EXCEEDED"},
		{fmt.Errorf("%w: detected modified configuration", ports.ErrInstanciaOcupada), http.StatusConflict, "INSTANCE_BUSY"},
		{ports.ErrInstanciaNoEncontrada, http.StatusNotFound, "INSTANCE_NOT_FOUND"},
		{fmt.Errorf("%w: lock", ports.ErrCoordinacionAprovisionamiento), http.StatusServiceUnavailable, "PROVISIONING_COORDINATION_UNAVAILABLE"},
	}
	for _, c := range casos {
		if s, resp := pedirConfig(&configuracionFalsa{err: c.err}, `{"ramMb":4096}`); s != c.status || resp["errorCode"] != c.codigo {
			t.Errorf("%v: se esperaba %d %s, vino %d %v", c.err, c.status, c.codigo, s, resp)
		}
	}
}
