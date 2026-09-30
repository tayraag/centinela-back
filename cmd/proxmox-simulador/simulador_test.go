package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/proxmox"
	"el-centinela/internal/core/ports"
)

const (
	tokenID     = "centinela-api@pve!backend-token"
	tokenSecret = "secreto-de-prueba"
)

// entorno levanta el simulador con un reloj controlado por el test.
type entorno struct {
	t      *testing.T
	sim    *Simulador
	server *httptest.Server
	reloj  time.Time
}

func nuevoEntorno(t *testing.T, falla string) *entorno {
	t.Helper()
	e := &entorno{t: t, reloj: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)}
	cfg := Config{Nodo: "proxmox", TokenID: tokenID, TokenSecret: tokenSecret, DuracionTarea: 3 * time.Second, Falla: falla}
	e.sim = NuevoSimulador(cfg)
	e.sim.ahora = func() time.Time { return e.reloj }
	e.sim.inicio = e.reloj
	e.sim.instancias = map[int]*instancia{}
	for _, inst := range inventarioInicial(e.reloj) {
		e.sim.instancias[inst.Vmid] = inst
	}
	e.server = httptest.NewServer(e.sim)
	t.Cleanup(e.server.Close)
	return e
}

func (e *entorno) avanzar(d time.Duration) { e.reloj = e.reloj.Add(d) }

// pedir hace un request autenticado y devuelve status y "data" (o el body entero si hay error).
func (e *entorno) pedir(metodo, ruta string, form url.Values) (int, any, map[string]any) {
	e.t.Helper()
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req, _ := http.NewRequest(metodo, e.server.URL+"/api2/json"+ruta, body)
	req.Header.Set("Authorization", "PVEAPIToken="+tokenID+"="+tokenSecret)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", metodo, ruta, err)
	}
	defer resp.Body.Close()
	var completo map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&completo)
	return resp.StatusCode, completo["data"], completo
}

func (e *entorno) cliente() *proxmox.Client {
	return proxmox.NewClient(e.server.URL+"/api2/json", "proxmox", tokenID, tokenSecret, nil)
}

// buscar devuelve el elemento de una lista con el vmid dado.
func buscar(lista any, vmid int) map[string]any {
	for _, x := range lista.([]any) {
		if m := x.(map[string]any); m["vmid"] == float64(vmid) {
			return m
		}
	}
	return nil
}

// ==========================================
// Autenticación, errores y modos de falla
// ==========================================

func TestExigeElTokenComoProxmox(t *testing.T) {
	e := nuevoEntorno(t, "")
	for _, auth := range []string{"", "PVEAPIToken=" + tokenID + "=otro-secreto", "Bearer algo"} {
		req, _ := http.NewRequest(http.MethodGet, e.server.URL+"/api2/json/cluster/resources", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, _ := http.DefaultClient.Do(req)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Authorization %q: se esperaba 401, vino %d", auth, resp.StatusCode)
		}
	}
}

func TestRutasYNodosInexistentes(t *testing.T) {
	e := nuevoEntorno(t, "")
	if s, _, _ := e.pedir("GET", "/nodes/proxmox/algo/raro/que/no/existe", nil); s != http.StatusNotImplemented {
		t.Errorf("Ruta inexistente: se esperaba 501, vino %d", s)
	}
	if s, _, cuerpo := e.pedir("GET", "/nodes/pve/status", nil); s != 500 || !strings.Contains(cuerpo["message"].(string), "hostname lookup 'pve'") {
		t.Errorf("Nodo inexistente: se esperaba 500 hostname lookup, vino %d %v", s, cuerpo)
	}
	if s, _, cuerpo := e.pedir("GET", "/nodes/proxmox/qemu/999/status/current", nil); s != 500 || !strings.Contains(cuerpo["message"].(string), "999.conf' does not exist") {
		t.Errorf("VMID inexistente: se esperaba 500 does not exist, vino %d %v", s, cuerpo)
	}
	if s, _, _ := e.pedir("GET", "/nodes/proxmox/lxc/100/status/current", nil); s != 500 {
		t.Errorf("La 100 es qemu, pedirla como lxc debe dar 500, vino %d", s)
	}
}

func TestModosDeFalla(t *testing.T) {
	for falla, esperado := range map[string]int{"caido": 500, "token": 401} {
		e := nuevoEntorno(t, falla)
		if s, _, _ := e.pedir("GET", "/cluster/resources", nil); s != esperado {
			t.Errorf("Modo %s: se esperaba %d, vino %d", falla, esperado, s)
		}
	}

	// Modo lento: el cliente del backend tiene que terminar en ErrProxmoxTimeout (→ 504).
	anterior := demoraModoLento
	demoraModoLento = 300 * time.Millisecond
	defer func() { demoraModoLento = anterior }()
	e := nuevoEntorno(t, "lento")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := e.cliente().ListarInstancias(ctx); err == nil || !strings.Contains(err.Error(), ports.ErrProxmoxTimeout.Error()) {
		t.Errorf("Modo lento: se esperaba ErrProxmoxTimeout, vino %v", err)
	}
}

// ==========================================
// Inventario (RF-02, RF-03)
// ==========================================

func TestClusterResourcesComoLaCaptura(t *testing.T) {
	e := nuevoEntorno(t, "")
	s, data, _ := e.pedir("GET", "/cluster/resources", nil)
	if s != 200 {
		t.Fatalf("status %d", s)
	}
	lista := data.([]any)
	tipos := map[string]int{}
	for _, x := range lista {
		tipos[x.(map[string]any)["type"].(string)]++
	}
	if tipos["qemu"] != 5 || tipos["lxc"] != 3 || tipos["node"] != 1 || tipos["storage"] != 2 || tipos["network"] != 1 {
		t.Errorf("Composición inesperada: %v", tipos)
	}

	vm100 := buscar(data, 100)
	for _, campo := range []string{"id", "node", "status", "diskwrite", "netout", "uptime", "template", "maxcpu", "name", "hastate", "maxmem", "diskread", "type", "memhost", "maxdisk", "vmid", "mem", "netin", "disk", "cpu"} {
		if _, ok := vm100[campo]; !ok {
			t.Errorf("A la 100 le falta el campo %q de la captura", campo)
		}
	}
	if vm100["name"] != "PruebaLucas" || vm100["hastate"] != "started" || vm100["uptime"] != float64(2484) || vm100["node"] != "proxmox" {
		t.Errorf("La 100 no coincide con la captura: %v", vm100)
	}
	if vm110 := buscar(data, 110); vm110["status"] != "stopped" || vm110["cpu"] != float64(0) || vm110["mem"] != float64(0) {
		t.Errorf("La 110 apagada debe tener cpu y mem en 0: %v", vm110)
	}

	if _, soloVMs, _ := e.pedir("GET", "/cluster/resources?type=vm", nil); len(soloVMs.([]any)) != 8 {
		t.Errorf("?type=vm debe devolver solo las 8 instancias, vinieron %d", len(soloVMs.([]any)))
	}
}

func TestContrato_ClienteDelBackend_ListarInstancias(t *testing.T) {
	e := nuevoEntorno(t, "")
	instancias, err := e.cliente().ListarInstancias(context.Background())
	if err != nil {
		t.Fatalf("El cliente del backend no pudo listar contra el simulador: %v", err)
	}
	if len(instancias) != 8 {
		t.Fatalf("Se esperaban 8 instancias, vinieron %d", len(instancias))
	}
	if instancias[0].Vmid != 100 || instancias[0].Tipo != "qemu" || instancias[0].Estado != "running" {
		t.Errorf("Primera instancia inesperada: %+v", instancias[0])
	}
}

// ==========================================
// Acciones y tareas (RF-04, RF-11)
// ==========================================

func TestContrato_ClienteDelBackend_IniciarYEsperarTarea(t *testing.T) {
	e := nuevoEntorno(t, "")
	upid, err := e.cliente().IniciarInstancia(context.Background(), 110)
	if err != nil {
		t.Fatalf("IniciarInstancia: %v", err)
	}
	if !strings.HasPrefix(upid, "UPID:proxmox:") || !strings.HasSuffix(upid, ":qmstart:110:"+tokenID+":") {
		t.Errorf("UPID con formato inesperado: %s", upid)
	}

	// Mientras corre la tarea, la VM sigue apagada y la tarea en "running".
	_, tarea, _ := e.pedir("GET", "/nodes/proxmox/tasks/"+upid+"/status", nil)
	if tarea.(map[string]any)["status"] != "running" {
		t.Errorf("La tarea debería estar corriendo: %v", tarea)
	}
	if inst, _ := e.cliente().ObtenerInstancia(context.Background(), 110); inst.Estado != "stopped" {
		t.Errorf("Antes de terminar la tarea la 110 debe seguir apagada, está %s", inst.Estado)
	}

	e.avanzar(3 * time.Second)
	_, tarea, _ = e.pedir("GET", "/nodes/proxmox/tasks/"+upid+"/status", nil)
	tm := tarea.(map[string]any)
	if tm["status"] != "stopped" || tm["exitstatus"] != "OK" || tm["user"] != "centinela-api@pve" || tm["tokenid"] != "backend-token" || tm["type"] != "qmstart" {
		t.Errorf("Estado final de la tarea inesperado: %v", tm)
	}
	if inst, _ := e.cliente().ObtenerInstancia(context.Background(), 110); inst.Estado != "running" {
		t.Errorf("Al terminar la tarea la 110 debe estar encendida, está %s", inst.Estado)
	}
}

func TestAcciones_ErroresYLocks(t *testing.T) {
	e := nuevoEntorno(t, "")

	// Encender una que ya está encendida: Proxmox acepta la tarea, pero termina con error.
	_, upid, _ := e.pedir("POST", "/nodes/proxmox/qemu/9003/status/start", url.Values{})
	e.avanzar(3 * time.Second)
	_, tarea, _ := e.pedir("GET", "/nodes/proxmox/tasks/"+upid.(string)+"/status", nil)
	if tarea.(map[string]any)["exitstatus"] != "VM 9003 already running" {
		t.Errorf("exitstatus inesperado: %v", tarea)
	}

	// Dos acciones seguidas sobre la misma instancia: la segunda choca con el lock.
	e.pedir("POST", "/nodes/proxmox/lxc/9002/status/shutdown", url.Values{})
	s, _, cuerpo := e.pedir("POST", "/nodes/proxmox/lxc/9002/status/start", url.Values{})
	if s != 500 || !strings.Contains(cuerpo["message"].(string), "can't lock file '/var/lock/lxc/lock-9002.conf'") {
		t.Errorf("Se esperaba el error de lock, vino %d %v", s, cuerpo)
	}

	// reset no existe para contenedores.
	if s, _, _ := e.pedir("POST", "/nodes/proxmox/lxc/101/status/reset", url.Values{}); s != 501 {
		t.Errorf("reset en lxc debe dar 501, vino %d", s)
	}

	// La 100 está administrada por HA: sus tareas se llaman hastop/hastart, como en la captura.
	_, upid, _ = e.pedir("POST", "/nodes/proxmox/qemu/100/status/stop", url.Values{})
	if !strings.Contains(upid.(string), ":hastop:100:") {
		t.Errorf("La 100 debería generar una tarea hastop: %v", upid)
	}

	// UPID sin el ":" final: Proxmox lo rechaza (nota de la captura RF-04).
	if s, _, _ := e.pedir("GET", "/nodes/proxmox/tasks/"+strings.TrimSuffix(upid.(string), ":")+"/status", nil); s != 400 {
		t.Errorf("UPID sin ':' final debe dar 400, vino %d", s)
	}
}

// ==========================================
// Métricas (RF-05)
// ==========================================

func TestRRDData(t *testing.T) {
	e := nuevoEntorno(t, "")

	_, data, _ := e.pedir("GET", "/nodes/proxmox/lxc/101/rrddata?timeframe=hour", nil)
	puntos := data.([]any)
	if len(puntos) != 60 {
		t.Fatalf("Se esperaban 60 puntos como en la captura, vinieron %d", len(puntos))
	}
	p0, p1 := puntos[0].(map[string]any), puntos[1].(map[string]any)
	if p1["time"].(float64)-p0["time"].(float64) != 60 {
		t.Errorf("Paso esperado de 60 s")
	}
	// La 101 lleva 2234 s encendida: los puntos de los últimos ~37 min traen todos los campos de la captura lxc.
	ultimo := puntos[59].(map[string]any)
	for _, campo := range []string{"cpu", "disk", "diskread", "diskwrite", "maxcpu", "maxdisk", "maxmem", "mem", "netin", "netout", "pressurecpufull", "pressurecpusome", "pressureiofull", "pressureiosome", "pressurememoryfull", "pressurememorysome", "time"} {
		if _, ok := ultimo[campo]; !ok {
			t.Errorf("Falta el campo %q en un punto lxc encendido", campo)
		}
	}
	if _, ok := ultimo["memhost"]; ok {
		t.Error("Los puntos lxc no traen memhost (ver captura)")
	}
	if _, ok := p0["cpu"]; ok {
		t.Error("Hace una hora la 101 estaba apagada: el punto no debe traer cpu")
	}

	// Una VM apagada: solo time, maxcpu, maxmem, maxdisk y disk (como el final de la captura vm).
	_, data, _ = e.pedir("GET", "/nodes/proxmox/qemu/110/rrddata?timeframe=hour", nil)
	if n := len(data.([]any)[59].(map[string]any)); n != 5 {
		t.Errorf("Un punto de VM apagada debe tener 5 campos, tiene %d", n)
	}

	if s, _, _ := e.pedir("GET", "/nodes/proxmox/qemu/100/rrddata", nil); s != 400 {
		t.Errorf("Sin timeframe debe dar 400, vino %d", s)
	}
}

// ==========================================
// Snapshots (RF-06)
// ==========================================

func TestSnapshots(t *testing.T) {
	e := nuevoEntorno(t, "")

	_, data, _ := e.pedir("GET", "/nodes/proxmox/qemu/100/snapshot", nil)
	lista := data.([]any)
	actual := lista[len(lista)-1].(map[string]any)
	if len(lista) != 3 || actual["name"] != "current" || actual["description"] != "You are here!" || actual["parent"] != "mi_segundo_snapshot" {
		t.Errorf("Snapshots iniciales de la 100 inesperados: %v", lista)
	}

	s, upid, _ := e.pedir("POST", "/nodes/proxmox/lxc/101/snapshot", url.Values{"snapname": {"antes_del_update"}, "description": {"prueba"}})
	if s != 200 || !strings.Contains(upid.(string), ":vzsnapshot:101:") {
		t.Fatalf("Crear snapshot: %d %v", s, upid)
	}
	e.avanzar(3 * time.Second)
	_, data, _ = e.pedir("GET", "/nodes/proxmox/lxc/101/snapshot", nil)
	if len(data.([]any)) != 2 {
		t.Errorf("Después de la tarea el snapshot debe aparecer: %v", data)
	}

	if s, _, cuerpo := e.pedir("POST", "/nodes/proxmox/lxc/101/snapshot", url.Values{"snapname": {"antes_del_update"}}); s != 500 || !strings.Contains(cuerpo["message"].(string), "already used") {
		t.Errorf("Nombre repetido: %d %v", s, cuerpo)
	}
	if s, _, _ := e.pedir("POST", "/nodes/proxmox/lxc/101/snapshot", url.Values{"snapname": {"con espacios"}}); s != 400 {
		t.Errorf("Nombre inválido debe dar 400, vino %d", s)
	}
	if s, _, _ := e.pedir("POST", "/nodes/proxmox/qemu/100/snapshot/no_existe/rollback", url.Values{}); s != 500 {
		t.Errorf("Rollback a un snapshot inexistente debe dar 500, vino %d", s)
	}

	// Rollback: la VM queda apagada y en el snapshot elegido.
	e.pedir("POST", "/nodes/proxmox/qemu/100/snapshot/mi_primer_snapshot/rollback", url.Values{})
	e.avanzar(3 * time.Second)
	_, estado, _ := e.pedir("GET", "/nodes/proxmox/qemu/100/status/current", nil)
	_, config, _ := e.pedir("GET", "/nodes/proxmox/qemu/100/config", nil)
	if estado.(map[string]any)["status"] != "stopped" || config.(map[string]any)["parent"] != "mi_primer_snapshot" {
		t.Errorf("Después del rollback: estado %v, parent %v", estado.(map[string]any)["status"], config.(map[string]any)["parent"])
	}
}

// ==========================================
// Creación y configuración (RF-07, RF-10)
// ==========================================

func TestCrearInstancias(t *testing.T) {
	e := nuevoEntorno(t, "")

	// Los mismos parámetros que la captura de RF-07.
	s, upid, _ := e.pedir("POST", "/nodes/proxmox/qemu", url.Values{
		"vmid": {"115"}, "name": {"VM-Postman"}, "memory": {"2048"}, "cores": {"2"},
		"net0": {"virtio,bridge=vmbr0"}, "scsihw": {"virtio-scsi-pci"}, "scsi0": {"local-lvm:20"},
		"ide2": {"local:iso/debian-13.6.0-amd64-netinst.iso,media=cdrom"},
	})
	if s != 200 || !strings.Contains(upid.(string), ":qmcreate:115:") {
		t.Fatalf("Crear VM: %d %v", s, upid)
	}
	_, data, _ := e.pedir("GET", "/cluster/resources?type=vm", nil)
	if vm := buscar(data, 115); vm == nil || vm["lock"] != "create" || vm["maxmem"] != float64(2048*mib) {
		t.Errorf("La 115 debe aparecer con lock create mientras se crea: %v", vm)
	}
	e.avanzar(3 * time.Second)
	_, config, _ := e.pedir("GET", "/nodes/proxmox/qemu/115/config", nil)
	if c := config.(map[string]any); c["scsi0"] != "local-lvm:vm-115-disk-0,size=20G" || c["lock"] != nil {
		t.Errorf("Config de la 115 inesperada: %v", c)
	}

	if s, _, cuerpo := e.pedir("POST", "/nodes/proxmox/lxc", url.Values{"vmid": {"115"}, "ostemplate": {"local:vztmpl/debian.tar.zst"}}); s != 500 || !strings.Contains(cuerpo["message"].(string), "already exists") {
		t.Errorf("VMID repetido (qemu y lxc comparten IDs): %d %v", s, cuerpo)
	}
	if s, _, cuerpo := e.pedir("POST", "/nodes/proxmox/lxc", url.Values{"vmid": {"202"}, "hostname": {"sin-template"}}); s != 400 || cuerpo["errors"].(map[string]any)["ostemplate"] == nil {
		t.Errorf("LXC sin ostemplate debe dar 400: %d %v", s, cuerpo)
	}
}

func TestEditarConfig_CambiosPendientes(t *testing.T) {
	e := nuevoEntorno(t, "")

	// GET config de la 100 como en la captura: memory viene como string en qemu.
	_, config, _ := e.pedir("GET", "/nodes/proxmox/qemu/100/config", nil)
	if c := config.(map[string]any); c["memory"] != "2048" || c["cores"] != float64(1) || c["name"] != "PruebaLucas" || c["digest"] == nil {
		t.Errorf("Config inicial de la 100: %v", c)
	}

	// La 100 está encendida: el cambio de memoria queda pendiente.
	s, data, _ := e.pedir("PUT", "/nodes/proxmox/qemu/100/config", url.Values{"memory": {"4096"}, "cores": {"2"}})
	if s != 200 || data != nil {
		t.Fatalf("PUT config debe responder 200 con data null: %d %v", s, data)
	}
	_, conPendientes, _ := e.pedir("GET", "/nodes/proxmox/qemu/100/config", nil)
	_, actual, _ := e.pedir("GET", "/nodes/proxmox/qemu/100/config?current=1", nil)
	if conPendientes.(map[string]any)["memory"] != "4096" || actual.(map[string]any)["memory"] != "2048" {
		t.Errorf("Pendiente: con pendientes %v, actual %v", conPendientes.(map[string]any)["memory"], actual.(map[string]any)["memory"])
	}

	// Al reiniciar se aplica.
	e.pedir("POST", "/nodes/proxmox/qemu/100/status/reboot", url.Values{})
	e.avanzar(3 * time.Second)
	_, estado, _ := e.pedir("GET", "/nodes/proxmox/qemu/100/status/current", nil)
	if estado.(map[string]any)["maxmem"] != float64(4096*mib) || estado.(map[string]any)["cpus"] != float64(2) {
		t.Errorf("Después del reboot deben aplicarse los cambios: %v", estado)
	}

	// En un lxc se aplica en caliente.
	e.pedir("PUT", "/nodes/proxmox/lxc/101/config", url.Values{"memory": {"2048"}})
	_, estado, _ = e.pedir("GET", "/nodes/proxmox/lxc/101/status/current", nil)
	if estado.(map[string]any)["maxmem"] != float64(2048*mib) {
		t.Errorf("En lxc el cambio de memoria debe aplicarse en caliente: %v", estado.(map[string]any)["maxmem"])
	}

	if s, _, _ := e.pedir("PUT", "/nodes/proxmox/qemu/110/config", url.Values{"memory": {"mucha"}}); s != 400 {
		t.Errorf("memory no numérica debe dar 400, vino %d", s)
	}
}

func TestContrato_ClienteDelBackend_EstadoTarea(t *testing.T) {
	e := nuevoEntorno(t, "")
	cliente := e.cliente()
	upid, err := cliente.DetenerInstancia(context.Background(), 9003)
	if err != nil {
		t.Fatal(err)
	}
	estado, err := cliente.EstadoTarea(context.Background(), upid)
	if err != nil || estado.Terminada {
		t.Fatalf("Recién creada la tarea debe estar corriendo: %+v, %v", estado, err)
	}
	e.avanzar(3 * time.Second)
	if estado, err = cliente.EstadoTarea(context.Background(), upid); err != nil || !estado.Terminada || estado.ExitStatus != "OK" {
		t.Fatalf("Al terminar debe dar Terminada con OK: %+v, %v", estado, err)
	}

	// Una tarea que termina con error trae el mensaje de Proxmox en ExitStatus.
	upid, _ = cliente.IniciarInstancia(context.Background(), 9002) // ya está encendida
	e.avanzar(3 * time.Second)
	if estado, _ = cliente.EstadoTarea(context.Background(), upid); estado.ExitStatus != "CT 9002 already running" {
		t.Errorf("ExitStatus de una tarea fallida: %+v", estado)
	}
	if _, err := cliente.EstadoTarea(context.Background(), "no-es-un-upid"); err == nil {
		t.Error("Un UPID inválido debe dar error")
	}
}

// ==========================================
// Fidelidad con la API real (Proxmox VE 9.2.2)
// ==========================================

// crudo hace un GET autenticado y devuelve la línea de estado y el body tal cual.
func (e *entorno) crudo(ruta string, auth string) (string, string) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.server.URL+"/api2/json"+ruta, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.Status, string(body)
}

const authValida = "PVEAPIToken=" + tokenID + "=" + tokenSecret

func TestFidelidad_ConfigConTiposNumericos(t *testing.T) {
	e := nuevoEntorno(t, "")

	// LXC: unprivileged, memory, swap y cores son números (captura RF-10 del contenedor 201).
	_, lxc := e.crudo("/nodes/proxmox/lxc/201/config", authValida)
	for _, campo := range []string{`"unprivileged":1`, `"memory":512`, `"swap":512`, `"cores":1`} {
		if !strings.Contains(lxc, campo) {
			t.Errorf("La config LXC debe tener %s sin comillas: %s", campo, lxc)
		}
	}

	// QEMU: cores, sockets y numa son números; memory es string, igual que en la API real
	// (captura RF-10 de la VM 100: "memory": "2048").
	_, qemu := e.crudo("/nodes/proxmox/qemu/100/config", authValida)
	for _, campo := range []string{`"cores":1`, `"sockets":1`, `"numa":0`, `"memory":"2048"`} {
		if !strings.Contains(qemu, campo) {
			t.Errorf("La config QEMU debe tener %s: %s", campo, qemu)
		}
	}

	// Los cambios pendientes también salen tipados.
	e.pedir("PUT", "/nodes/proxmox/qemu/100/config", url.Values{"cores": {"4"}})
	if _, qemu = e.crudo("/nodes/proxmox/qemu/100/config", authValida); !strings.Contains(qemu, `"cores":4`) {
		t.Errorf("El cambio pendiente de cores debe salir como número: %s", qemu)
	}

	// Deserializar en un struct estricto de Go no debe fallar.
	var estricto struct {
		Data struct {
			Unprivileged int `json:"unprivileged"`
			Memory       int `json:"memory"`
			Swap         int `json:"swap"`
			Cores        int `json:"cores"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(lxc), &estricto); err != nil || estricto.Data.Unprivileged != 1 || estricto.Data.Memory != 512 {
		t.Errorf("Un struct estricto debe poder leer la config LXC: %+v, %v", estricto.Data, err)
	}
}

func TestFidelidad_StatusCurrentConHA(t *testing.T) {
	e := nuevoEntorno(t, "")
	casos := map[string]map[string]any{
		"/nodes/proxmox/lxc/101/status/current":  {"managed": float64(0)},
		"/nodes/proxmox/qemu/110/status/current": {"managed": float64(0)},
		"/nodes/proxmox/qemu/100/status/current": {"managed": float64(1), "state": "started"},
	}
	for ruta, esperado := range casos {
		_, data, _ := e.pedir("GET", ruta, nil)
		ha, _ := data.(map[string]any)["ha"].(map[string]any)
		if len(ha) != len(esperado) {
			t.Errorf("%s: ha = %v, se esperaba %v", ruta, ha, esperado)
			continue
		}
		for k, v := range esperado {
			if ha[k] != v {
				t.Errorf("%s: ha[%s] = %v, se esperaba %v", ruta, k, ha[k], v)
			}
		}
	}
	// En el listado por tipo Proxmox no manda "ha" (ver captura).
	_, lista, _ := e.pedir("GET", "/nodes/proxmox/lxc", nil)
	if _, tiene := buscar(lista, 101)["ha"]; tiene {
		t.Error("El listado GET /nodes/{node}/lxc no debe traer ha")
	}
}

func TestFidelidad_NextID(t *testing.T) {
	e := nuevoEntorno(t, "")
	if s, data, _ := e.pedir("GET", "/cluster/nextid", nil); s != 200 || data != "102" {
		t.Fatalf("nextid = %d %v, se esperaba 200 \"102\" (100 y 101 están ocupados)", s, data)
	}
	e.pedir("POST", "/nodes/proxmox/qemu", url.Values{"vmid": {"102"}})
	if _, data, _ := e.pedir("GET", "/cluster/nextid", nil); data != "103" {
		t.Errorf("Después de crear la 102, nextid debe ser \"103\": %v", data)
	}
	if _, crudo := e.crudo("/cluster/nextid", authValida); !strings.Contains(crudo, `"data":"103"`) {
		t.Errorf("El VMID viaja como string, como en Proxmox: %s", crudo)
	}

	if s, data, _ := e.pedir("GET", "/cluster/nextid?vmid=150", nil); s != 200 || data != "150" {
		t.Errorf("?vmid libre debe devolverlo: %d %v", s, data)
	}
	if s, _, cuerpo := e.pedir("GET", "/cluster/nextid?vmid=110", nil); s != 400 || !strings.Contains(cuerpo["message"].(string), "VM 110 already exists") {
		t.Errorf("?vmid ocupado debe dar 400: %d %v", s, cuerpo)
	}
	if s, _, _ := e.pedir("GET", "/cluster/nextid?vmid=50", nil); s != 400 {
		t.Errorf("?vmid menor a 100 debe dar 400, vino %d", s)
	}
}

func TestFidelidad_ListarTareas(t *testing.T) {
	e := nuevoEntorno(t, "")
	listar := func(query string) (int, []any, float64) {
		t.Helper()
		s, data, cuerpo := e.pedir("GET", "/nodes/proxmox/tasks"+query, nil)
		lista, _ := data.([]any)
		total, _ := cuerpo["total"].(float64)
		return s, lista, total
	}

	if s, lista, total := listar(""); s != 200 || len(lista) != 0 || total != 0 {
		t.Fatalf("Sin tareas: %d %v total=%v", s, lista, total)
	}

	_, upid, _ := e.pedir("POST", "/nodes/proxmox/qemu/110/status/start", url.Values{})
	// Por defecto (source=archive) solo aparecen las terminadas.
	if _, lista, _ := listar(""); len(lista) != 0 {
		t.Errorf("Con source=archive (default) una tarea en curso no aparece: %v", lista)
	}
	_, activas, _ := listar("?source=active")
	if len(activas) != 1 {
		t.Fatalf("source=active debe traer la tarea en curso: %v", activas)
	}
	enCurso := activas[0].(map[string]any)
	if enCurso["upid"] != upid || enCurso["status"] != nil || enCurso["endtime"] != nil {
		t.Errorf("Una tarea en curso no tiene status ni endtime: %v", enCurso)
	}

	e.avanzar(3 * time.Second)
	e.pedir("POST", "/nodes/proxmox/qemu/9003/status/start", url.Values{}) // ya encendida → falla
	e.avanzar(3 * time.Second)

	s, lista, total := listar("")
	if s != 200 || len(lista) != 2 || total != 2 {
		t.Fatalf("Terminadas: %d %v total=%v", s, lista, total)
	}
	primera, segunda := lista[0].(map[string]any), lista[1].(map[string]any)
	if primera["id"] != "9003" || segunda["id"] != "110" {
		t.Errorf("Deben venir de la más nueva a la más vieja: %v, %v", primera["id"], segunda["id"])
	}
	for _, campo := range []string{"upid", "node", "pid", "pstart", "starttime", "endtime", "type", "id", "user", "tokenid", "status"} {
		if _, ok := segunda[campo]; !ok {
			t.Errorf("A la tarea terminada le falta %q: %v", campo, segunda)
		}
	}
	if segunda["status"] != "OK" || segunda["user"] != "centinela-api@pve" || segunda["tokenid"] != "backend-token" || segunda["type"] != "qmstart" {
		t.Errorf("Tarea terminada: %v", segunda)
	}
	if segunda["endtime"].(float64) <= segunda["starttime"].(float64) {
		t.Errorf("endtime debe ser posterior a starttime: %v", segunda)
	}

	if _, lista, _ := listar("?errors=1"); len(lista) != 1 || lista[0].(map[string]any)["status"] != "VM 9003 already running" {
		t.Errorf("errors=1 debe traer solo la fallida: %v", lista)
	}
	if _, lista, _ := listar("?vmid=110"); len(lista) != 1 {
		t.Errorf("vmid=110 debe traer solo esa: %v", lista)
	}
	if _, lista, total := listar("?limit=1&start=1"); len(lista) != 1 || total != 2 || lista[0].(map[string]any)["id"] != "110" {
		t.Errorf("Paginación: %v total=%v", lista, total)
	}
	if s, _, _ := e.pedir("GET", "/nodes/proxmox/tasks?source=otra", nil); s != 400 {
		t.Errorf("source inválido debe dar 400, vino %d", s)
	}
}

func TestFidelidad_401Canonico(t *testing.T) {
	e := nuevoEntorno(t, "")
	for _, auth := range []string{"", "PVEAPIToken=" + tokenID + "=secreto-incorrecto"} {
		status, body := e.crudo("/cluster/resources", auth)
		if status != "401 Authentication failed!" || body != "" {
			t.Errorf("Authorization %q: se esperaba \"401 Authentication failed!\" sin body, vino %q %q", auth, status, body)
		}
	}
	// El modo falla "token" responde igual.
	if status, body := nuevoEntorno(t, "token").crudo("/cluster/resources", authValida); status != "401 Authentication failed!" || body != "" {
		t.Errorf("Modo falla token: %q %q", status, body)
	}
	// Y el cliente del backend lo sigue reconociendo como token rechazado.
	malo := proxmox.NewClient(e.server.URL+"/api2/json", "proxmox", tokenID, "incorrecto", nil)
	if _, err := malo.ListarInstancias(context.Background()); !errors.Is(err, ports.ErrProxmoxCredenciales) {
		t.Errorf("El backend debe reconocer el 401 canónico como credenciales rechazadas: %v", err)
	}
}

// Dos acciones de energía seguidas sobre la misma instancia: el cliente del
// backend traduce el "can't lock file" del simulador a ErrInstanciaOcupada.
func TestContrato_ClienteDelBackend_InstanciaOcupada(t *testing.T) {
	e := nuevoEntorno(t, "")
	cliente := e.cliente()
	if _, err := cliente.IniciarInstancia(context.Background(), 110); err != nil {
		t.Fatal(err)
	}
	_, err := cliente.DetenerInstancia(context.Background(), 110)
	if !errors.Is(err, ports.ErrInstanciaOcupada) || errors.Is(err, ports.ErrProxmoxNoDisponible) {
		t.Fatalf("La segunda acción inmediata debe dar ErrInstanciaOcupada (no Proxmox caído): %v", err)
	}
	e.avanzar(3 * time.Second)
	if _, err := cliente.DetenerInstancia(context.Background(), 110); err != nil {
		t.Errorf("Cuando termina la primera tarea, la instancia se libera: %v", err)
	}
}
