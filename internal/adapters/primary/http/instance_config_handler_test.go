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
	resultado        *ports.ResultadoReconfiguracion
	err              error
}

func (f *configuracionFalsa) Reconfigurar(_ context.Context, _, cores, memoriaMB int) (*ports.ResultadoReconfiguracion, error) {
	f.llamadas++
	f.cores, f.memoria = cores, memoriaMB
	resultado := f.resultado
	if resultado == nil {
		nuevosCores, nuevaMemoria := cores, memoriaMB
		if nuevosCores == 0 {
			nuevosCores = 2
		}
		if nuevaMemoria == 0 {
			nuevaMemoria = 2048
		}
		resultado = &ports.ResultadoReconfiguracion{
			RequiresReboot: f.requiereReinicio, RecursoTipo: ports.RecursoVM,
			Anterior:   ports.ConfigInstanciaDTO{Cores: 2, MemoriaMB: 2048},
			Solicitada: ports.ConfigInstanciaDTO{Cores: nuevosCores, MemoriaMB: nuevaMemoria},
		}
	}
	if f.err != nil {
		return resultado, f.err
	}
	return resultado, nil
}

func pedirConfig(f *configuracionFalsa, cuerpo string) (int, map[string]any) {
	status, respuesta, _ := pedirConfigConAuditoria(f, cuerpo)
	return status, respuesta
}

func pedirConfigConAuditoria(f *configuracionFalsa, cuerpo string) (int, map[string]any, *mockAuditService) {
	return pedirConfigConAuditService(f, cuerpo, &mockAuditService{})
}

func pedirConfigConAuditService(f *configuracionFalsa, cuerpo string, audit *mockAuditService) (int, map[string]any, *mockAuditService) {
	gin.SetMode(gin.TestMode)
	h := adaptersHttp.NewInstanceConfigHandler(f, audit)
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
	return w.Code, resp, audit
}

func TestConfig_FalloDeAuditoriaNoInformaExitoNormal(t *testing.T) {
	falloDB := fmt.Errorf("postgres no disponible")
	for _, caso := range []struct {
		nombre       string
		errorProxmox error
		resultado    string
		mensaje      string
	}{
		{"configuración aplicada", nil, ports.ResultadoSuccess, "La configuración fue aplicada"},
		{"rechazo de Proxmox", fmt.Errorf("%w: permission denied", ports.ErrProxmoxNoDisponible), ports.ResultadoFailed, "La reconfiguración falló"},
	} {
		t.Run(caso.nombre, func(t *testing.T) {
			audit := &mockAuditService{errRegistrar: falloDB}
			status, respuesta, audit := pedirConfigConAuditService(&configuracionFalsa{err: caso.errorProxmox}, `{"cores":4,"ramMb":4096}`, audit)
			if status != http.StatusInternalServerError || respuesta["errorCode"] != "AUDIT_PERSISTENCE_FAILED" ||
				!strings.Contains(fmt.Sprint(respuesta["message"]), caso.mensaje) {
				t.Fatalf("se ocultó el fallo de auditoría: status=%d respuesta=%v", status, respuesta)
			}
			registro, ok := audit.ultimo()
			if !ok || registro.Resultado != caso.resultado || registro.Accion != ports.AccionConfigurarInstancia {
				t.Fatalf("intento de auditoría incorrecto: %+v, existe=%v", registro, ok)
			}
		})
	}
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

func TestConfig_AuditaDiferencialEnExitoYRechazo(t *testing.T) {
	resultado := &ports.ResultadoReconfiguracion{
		RequiresReboot: true, RecursoTipo: ports.RecursoVM,
		Anterior:   ports.ConfigInstanciaDTO{Cores: 2, MemoriaMB: 2048},
		Solicitada: ports.ConfigInstanciaDTO{Cores: 4, MemoriaMB: 4096},
	}
	for _, caso := range []struct {
		nombre, esperado string
		err              error
	}{
		{"éxito", ports.ResultadoSuccess, nil},
		{"rechazo Proxmox", ports.ResultadoFailed, fmt.Errorf("%w: permission denied", ports.ErrProxmoxNoDisponible)},
	} {
		t.Run(caso.nombre, func(t *testing.T) {
			_, _, audit := pedirConfigConAuditoria(&configuracionFalsa{resultado: resultado, err: caso.err}, `{"cores":4,"ramMb":4096}`)
			registro, ok := audit.ultimo()
			if !ok {
				t.Fatal("no se registró la auditoría")
			}
			if registro.Accion != ports.AccionConfigurarInstancia || registro.Resultado != caso.esperado || registro.InstanciaID != "110" ||
				registro.RecursoTipo != ports.RecursoVM || registro.RecursoID != "110" {
				t.Fatalf("cabecera de auditoría incorrecta: %+v", registro)
			}
			anteriores := registro.Detalles["valoresAnteriores"].(map[string]any)
			nuevos := registro.Detalles["valoresNuevos"].(map[string]any)
			if anteriores["cores"] != 2 || anteriores["ramMb"] != 2048 || nuevos["cores"] != 4 || nuevos["ramMb"] != 4096 ||
				registro.Detalles["deltaRamMb"] != 2048 || registro.Detalles["requiresReboot"] != true ||
				registro.Detalles["resource_type"] != ports.RecursoVM || registro.Detalles["resource_id"] != "110" {
				t.Fatalf("diferencial incompleto: %+v", registro.Detalles)
			}
			if caso.err != nil && registro.Detalles["error"] == nil {
				t.Fatalf("el rechazo debe conservar el error: %+v", registro.Detalles)
			}
		})
	}
}

func TestConfig_AuditaSuccessCuandoSoloFallaLaVerificacionPosterior(t *testing.T) {
	resultado := &ports.ResultadoReconfiguracion{
		Aplicada: true, VerificacionPendiente: true, RecursoTipo: ports.RecursoVM,
		Anterior:   ports.ConfigInstanciaDTO{Cores: 2, MemoriaMB: 2048},
		Solicitada: ports.ConfigInstanciaDTO{Cores: 4, MemoriaMB: 4096},
	}
	errorVerificacion := fmt.Errorf("%w: timeout leyendo config/current", ports.ErrVerificacionPostConfig)
	status, respuesta, audit := pedirConfigConAuditoria(&configuracionFalsa{resultado: resultado, err: errorVerificacion}, `{"cores":4,"ramMb":4096}`)
	if status != http.StatusBadGateway || respuesta["errorCode"] != "CONFIG_APPLIED_VERIFICATION_FAILED" {
		t.Fatalf("respuesta ambigua: status=%d respuesta=%v", status, respuesta)
	}
	registro, ok := audit.ultimo()
	if !ok || registro.Resultado != ports.ResultadoSuccess {
		t.Fatalf("un PUT aceptado no debe auditarse FAILED: %+v", registro)
	}
	if registro.Detalles["requiresReboot"] != nil || registro.Detalles["verificationStatus"] != "FAILED" {
		t.Fatalf("la incertidumbre de verificación no quedó auditada: %+v", registro.Detalles)
	}
}
