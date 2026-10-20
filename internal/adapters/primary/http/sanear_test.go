package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
)

// fuga detecta lo que nunca puede llegar al cliente.
var fuga = regexp.MustCompile(`/usr/|/var/|/run/|\.pm\b|\.pl\b|PVE::|line \d+|UPID:`)

const trazaQemu = "unable to create VM 115: storage 'local-lvm' does not exist at /usr/share/perl5/PVE/API2/Qemu.pm line 450."

func TestSanearTexto(t *testing.T) {
	casos := map[string]string{
		// El ejemplo de la tarea: se quita la cola técnica y queda la frase útil.
		trazaQemu: "unable to create VM 115: storage 'local-lvm' does not exist",
		"command '/usr/bin/kvm -id 115 -name srv' failed: exit code 1":                   "command '[ruta] -id 115 -name srv' failed: exit code 1",
		"can't lock file '/var/lock/qemu-server/lock-110.conf' - got timeout":            "can't lock file '[ruta]' - got timeout",
		"unable to connect to VM 115 qmp socket - timeout after 31 retries":              "unable to connect to VM 115 qmp socket - timeout after 31 retries",
		"VM 115 qmp command failed: /var/run/qemu-server/115.qmp: No such file":          "VM 115 qmp command failed: [ruta]: No such file",
		"error en la tarea UPID:pve1:0000ABCD:00112233:6543210F:qmstart:115:root@pam: x": "error en la tarea [tarea] x",
		"PVE::Storage::LVMPlugin::alloc_image failed: no space":                          "[módulo] failed: no space",
		// Una traza de varias líneas: solo la primera.
		"storage 'zfs' is not online\n  at /usr/share/perl5/PVE/Storage.pm line 120.\n  PVE::API2::Qemu::create_vm()": "storage 'zfs' is not online",
		// Lo que no tiene nada técnico pasa igual.
		"el /runtime y /devices no son rutas del sistema": "el /runtime y /devices no son rutas del sistema",
		"falló /run":  "falló [ruta]",
		"OK":          "OK",
		"WARNINGS: 2": "WARNINGS: 2",
		"La instancia se encuentra ejecutando otra tarea. Aguarde a que finalice.": "La instancia se encuentra ejecutando otra tarea. Aguarde a que finalice.",
		"El cuerpo debe ser JSON con cores y/o ramMb enteros.":                     "El cuerpo debe ser JSON con cores y/o ramMb enteros.",
		"": "",
		// Si no queda nada útil, el texto canónico.
		"/usr/share/perl5/PVE/API2/Qemu.pm line 450.":                MensajeErrorHipervisor,
		"at /usr/share/perl5/PVE/RESTHandler.pm line 401.":           MensajeErrorHipervisor,
		"UPID:pve1:0000ABCD:00112233:6543210F:qmstart:115:root@pam:": MensajeErrorHipervisor,
	}
	for entrada, esperado := range casos {
		if got := SanearTexto(entrada); got != esperado {
			t.Errorf("SanearTexto(%q)\n  = %q\n  se esperaba %q", entrada, got, esperado)
		}
	}
	largo := SanearTexto(strings.Repeat("a", 1000))
	if n := len([]rune(largo)); n != largoMaximoTextoCliente+1 || !strings.HasSuffix(largo, "…") {
		t.Errorf("Un texto largo se recorta a %d caracteres: %d", largoMaximoTextoCliente, n)
	}
}

func TestSanearDetalles_SinCamposInternosNiTrazas(t *testing.T) {
	original := map[string]any{
		"tareaId": "3f2504e0-4f89-11d3-9a0c-0305e82c3301", "accion": "CREATE", "estado": "FAILED",
		"exitstatus": trazaQemu, "error": trazaQemu, "motivo": ports.MotivoProxmoxError,
		"upid": "UPID:pve1:x", "rawOutput": "traza completa", "StackTrace": "...", "socket": "/var/run/qemu-server/115.qmp",
		"anidado": map[string]any{"error": trazaQemu, "trace": "x"}, "lista": []any{trazaQemu, 3}, "nulo": nil,
	}
	d := sanearDetalles(original)
	for _, clave := range []string{"upid", "rawOutput", "StackTrace", "socket"} {
		if _, existe := d[clave]; existe {
			t.Errorf("%s no debe viajar al cliente", clave)
		}
	}
	if d["tareaId"] != original["tareaId"] || d["motivo"] != ports.MotivoProxmoxError || d["nulo"] != nil {
		t.Errorf("Los campos públicos se conservan: %v", d)
	}
	crudo, _ := json.Marshal(d)
	if fuga.Match(crudo) {
		t.Errorf("Los detalles saneados filtran datos internos: %s", crudo)
	}
	if _, existe := d["anidado"].(map[string]any)["trace"]; existe {
		t.Error("También se limpian los mapas anidados")
	}
	if original["error"] != trazaQemu || original["upid"] == nil {
		t.Error("No debe modificarse el original (lo comparten todos los streams)")
	}
}

// DoD: ninguna respuesta de error lleva rutas, módulos Perl ni trazas.
func TestSendError_SaneaElMensaje(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	SendError(c, http.StatusBadRequest, "INVALID_REQUEST", trazaQemu)
	if fuga.Match(w.Body.Bytes()) {
		t.Errorf("SendError filtró la traza: %s", w.Body.String())
	}
}

func TestMapearErrorProxmox_NuncaDevuelveLaTraza(t *testing.T) {
	gin.SetMode(gin.TestMode)
	traza := errors.New(trazaQemu + "\n  PVE::API2::Qemu::create_vm() called at /usr/share/perl5/PVE/RESTHandler.pm line 401")
	casos := []struct {
		err    error
		status int
		codigo string
	}{
		{traza, http.StatusInternalServerError, "PROXMOX_ERROR"},
		{fmt.Errorf("%w: HTTP 500 en /nodes/pve1/qemu: %v", ports.ErrProxmoxNoDisponible, traza), http.StatusBadGateway, "PROXMOX_UNAVAILABLE"},
		{fmt.Errorf("%w: %w: %v", ports.ErrProxmoxNoDisponible, ports.ErrProxmoxTimeout, traza), http.StatusGatewayTimeout, "PROXMOX_TIMEOUT"},
		{fmt.Errorf("%w: %v", ports.ErrInstanciaOcupada, traza), http.StatusConflict, "INSTANCE_BUSY"},
		{fmt.Errorf("%w: %v", ports.ErrRecursosInsuficientes, traza), http.StatusConflict, "RESOURCE_QUOTA_EXCEEDED"},
	}
	for _, caso := range casos {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/instances", nil)
		mapearErrorProxmox(c, caso.err)
		var cuerpo map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &cuerpo)
		if w.Code != caso.status || cuerpo["errorCode"] != caso.codigo || fuga.Match(w.Body.Bytes()) {
			t.Errorf("%s: se esperaba %d %s sin traza, vino %d %s", caso.codigo, caso.status, caso.codigo, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/instances", nil)
	mapearErrorProxmox(c, traza)
	if !strings.Contains(w.Body.String(), MensajeErrorHipervisor) {
		t.Errorf("Un error no clasificado lleva el texto canónico: %s", w.Body.String())
	}
}

// DoD: los eventos SSE llegan sin trazas, sin UPID ni campos de depuración,
// para cualquier rol y tipo de recurso.
func TestEventoPublico_Saneado(t *testing.T) {
	for _, recurso := range []struct{ tipo, id string }{{ports.RecursoVM, "115"}, {ports.RecursoHost, ""}} {
		ev, _ := ports.NewRealtimeEvent(ports.EventoTareaFinalizada, ports.SeveridadWarning, "La tarea de creación falló")
		ev = ev.ConRecurso(recurso.tipo, recurso.id).ConDetalles(map[string]any{
			"tareaId": "3f2504e0-4f89-11d3-9a0c-0305e82c3301", "exitstatus": trazaQemu, "error": trazaQemu, "upid": "UPID:pve1:x", "rawOutput": "x",
		})
		publico := eventoPublico(RefsPrueba, ev)
		crudo, _ := json.Marshal(publico)
		if fuga.Match(crudo) || strings.Contains(string(crudo), "rawOutput") || strings.Contains(string(crudo), `"upid"`) {
			t.Errorf("%s: el evento filtra datos internos: %s", recurso.tipo, crudo)
		}
		if publico.Detalles["tareaId"] != "3f2504e0-4f89-11d3-9a0c-0305e82c3301" {
			t.Errorf("%s: tareaId se conserva: %v", recurso.tipo, publico.Detalles)
		}
	}
	ev, _ := ports.NewRealtimeEvent(ports.EventoTareaFinalizada, ports.SeveridadWarning, "La tarea de creación falló")
	publico := eventoPublico(RefsPrueba, ev.ConRecurso(ports.RecursoVM, "115").ConDetalles(map[string]any{"error": trazaQemu}))
	if esperado := "unable to create VM " + RefInst(115) + ": storage 'local-lvm' does not exist"; publico.Detalles["error"] != esperado {
		t.Errorf("error = %q, se esperaba %q", publico.Detalles["error"], esperado)
	}
}

// Los mensajes del WebSocket solo tienen campos de telemetría y la referencia
// pública: un campo nuevo (de depuración, un socket, el UPID) rompe este test.
func TestMensajesWS_SoloCamposPublicos(t *testing.T) {
	permitidos := map[string]bool{"vmid": true, "timestamp": true, "status": true, "cpu": true, "cpus": true, "mem": true, "maxmem": true,
		"disk": true, "maxdisk": true, "netin": true, "netout": true, "diskread": true, "diskwrite": true, "uptime": true, "error": true}
	for nombre, mensaje := range map[string]any{
		"métricas": metricasWS{VMID: RefInst(110), MetricasEnVivoDTO: &domain.MetricasEnVivoDTO{VMID: 110, Status: "running"}},
		"error":    errorMetricasWS{VMID: RefInst(110), Status: "unavailable", Error: detalleErrorWS{ErrorCode: "PROXMOX_UNAVAILABLE", Message: "x"}},
	} {
		crudo, _ := json.Marshal(mensaje)
		var campos map[string]any
		_ = json.Unmarshal(crudo, &campos)
		for campo := range campos {
			if !permitidos[campo] {
				t.Errorf("El mensaje de %s manda el campo %q, que no es público", nombre, campo)
			}
		}
		if campos["vmid"] != RefInst(110) {
			t.Errorf("%s: vmid debe ser la referencia pública: %v", nombre, campos["vmid"])
		}
	}
}
