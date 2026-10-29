package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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
	return proxmox.NewClient(e.server.URL+"/api2/json", tokenID, tokenSecret, nil)
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

// Al arrancar sin PROXMOX_NODE la API descubre el nodo en /nodes, y siempre
// contrasta /version con PROXMOX_TARGET_PVE_VERSION.
func TestContrato_ClienteDelBackend_NodosYVersion(t *testing.T) {
	e := nuevoEntorno(t, "")
	nodos, err := e.cliente().ListarNodos(context.Background())
	if err != nil || len(nodos) != 1 || nodos[0] != "proxmox" {
		t.Errorf("nodos=%v err=%v", nodos, err)
	}
	version, err := e.cliente().VersionPVE(context.Background())
	if err != nil || version != VersionPVE {
		t.Errorf("version=%q err=%v", version, err)
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

// snapshotsDe devuelve los snapshots de una instancia por nombre, más la
// entrada "current", y los nombres en el orden en que los lista el simulador.
func snapshotsDe(e *entorno, tipo, vmid string) (porNombre map[string]map[string]any, orden []string) {
	e.t.Helper()
	s, data, cuerpo := e.pedir("GET", "/nodes/proxmox/"+tipo+"/"+vmid+"/snapshot", nil)
	if s != 200 {
		e.t.Fatalf("Listar snapshots de %s/%s: %d %v", tipo, vmid, s, cuerpo)
	}
	porNombre = map[string]map[string]any{}
	for _, x := range data.([]any) {
		snap := x.(map[string]any)
		nombre := snap["name"].(string)
		porNombre[nombre] = snap
		orden = append(orden, nombre)
	}
	return porNombre, orden
}

// Ciclo completo de snapshots (crear, listar, revertir y borrar) para QEMU y
// LXC, con el formato de las capturas del Proxmox real (RF-06).
func TestSnapshots_CicloCompleto(t *testing.T) {
	casos := []struct{ tipo, vmid, prefijo string }{
		{"qemu", "110", "qm"},
		{"lxc", "201", "vz"},
	}
	for _, c := range casos {
		t.Run(c.tipo, func(t *testing.T) {
			e := nuevoEntorno(t, "")
			base := "/nodes/proxmox/" + c.tipo + "/" + c.vmid + "/snapshot"
			esperarUPID := func(accion string, s int, upid any) {
				t.Helper()
				// Formato real: UPID:<nodo>:<pid>:<pstart>:<inicio>:<tipo de tarea>:<vmid>:<usuario>:
				if s != 200 || !strings.HasPrefix(fmt.Sprint(upid), "UPID:proxmox:") || !strings.Contains(fmt.Sprint(upid), ":"+c.prefijo+accion+":"+c.vmid+":") {
					t.Fatalf("%s: se esperaba 200 con UPID %s%s, vino %d %v", accion, c.prefijo, accion, s, upid)
				}
			}

			// 1. Crear: devuelve el UPID y el snapshot aparece recién cuando termina la tarea.
			s, upid, _ := e.pedir("POST", base, url.Values{"snapname": {"antes_del_cambio"}, "description": {"Antes de actualizar"}})
			esperarUPID("snapshot", s, upid)
			if porNombre, _ := snapshotsDe(e, c.tipo, c.vmid); porNombre["antes_del_cambio"] != nil {
				t.Error("El snapshot no debe aparecer mientras la tarea sigue en curso")
			}
			if s, _, cuerpo := e.pedir("POST", base, url.Values{"snapname": {"otro"}}); s != 500 || !strings.Contains(fmt.Sprint(cuerpo["message"]), "locked (snapshot)") {
				t.Errorf("Otra orden durante la tarea debe dar 500 locked (snapshot): %d %v", s, cuerpo)
			}
			e.avanzar(3 * time.Second)

			// 2. Listar: los campos de la captura real y la entrada "current" al final.
			porNombre, orden := snapshotsDe(e, c.tipo, c.vmid)
			primero := porNombre["antes_del_cambio"]
			if primero == nil || primero["description"] != "Antes de actualizar" || primero["snaptime"] != float64(e.reloj.Add(-3*time.Second).Unix()) {
				t.Fatalf("Snapshot creado: %v", primero)
			}
			if _, tieneVMState := primero["vmstate"]; tieneVMState != (c.tipo == "qemu") {
				t.Errorf("vmstate solo aparece en QEMU, como en las capturas: %v", primero)
			}
			if orden[len(orden)-1] != "current" || porNombre["current"]["parent"] != "antes_del_cambio" || porNombre["current"]["description"] != "You are here!" {
				t.Errorf("La entrada current debe ir al final y apuntar al último snapshot: %v %v", orden, porNombre["current"])
			}

			// Un segundo snapshot: orden cronológico y cadena de padres.
			e.avanzar(time.Minute)
			s, upid, _ = e.pedir("POST", base, url.Values{"snapname": {"despues_del_cambio"}})
			esperarUPID("snapshot", s, upid)
			e.avanzar(3 * time.Second)
			porNombre, orden = snapshotsDe(e, c.tipo, c.vmid)
			if strings.Join(orden, ",") != "antes_del_cambio,despues_del_cambio,current" || porNombre["despues_del_cambio"]["parent"] != "antes_del_cambio" {
				t.Errorf("Orden cronológico y padre: %v %v", orden, porNombre["despues_del_cambio"])
			}

			// 3. Revertir al primero.
			s, upid, _ = e.pedir("POST", base+"/antes_del_cambio/rollback", url.Values{})
			esperarUPID("rollback", s, upid)
			e.avanzar(3 * time.Second)
			if porNombre, _ = snapshotsDe(e, c.tipo, c.vmid); porNombre["current"]["parent"] != "antes_del_cambio" {
				t.Errorf("Después del rollback, current debe colgar del snapshot elegido: %v", porNombre["current"])
			}

			// 4. Borrar el segundo: sigue listado hasta que termina la tarea.
			s, upid, _ = e.pedir("DELETE", base+"/despues_del_cambio", nil)
			esperarUPID("delsnapshot", s, upid)
			if porNombre, _ = snapshotsDe(e, c.tipo, c.vmid); porNombre["despues_del_cambio"] == nil {
				t.Error("El snapshot se quita recién cuando termina la tarea de borrado")
			}
			e.avanzar(3 * time.Second)
			if _, orden = snapshotsDe(e, c.tipo, c.vmid); strings.Join(orden, ",") != "antes_del_cambio,current" {
				t.Errorf("Después del borrado deben quedar el primero y current: %v", orden)
			}

			// Errores, como en Proxmox: snapshot inexistente y nombre inválido.
			for _, pedido := range []struct{ metodo, ruta string }{
				{"POST", base + "/no_existe/rollback"},
				{"DELETE", base + "/no_existe"},
			} {
				if s, _, cuerpo := e.pedir(pedido.metodo, pedido.ruta, url.Values{}); s != 500 || !strings.Contains(fmt.Sprint(cuerpo["message"]), "does not exist") {
					t.Errorf("%s %s: se esperaba 500 does not exist, vino %d %v", pedido.metodo, pedido.ruta, s, cuerpo)
				}
			}
			if s, _, cuerpo := e.pedir("POST", base, url.Values{"description": {"sin nombre"}}); s != 400 || cuerpo["errors"] == nil {
				t.Errorf("Sin snapname debe dar 400 con errors: %d %v", s, cuerpo)
			}
		})
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

	// El digest se calcula como Proxmox (SHA-1 del archivo de config) y coincide con la captura real.
	if !strings.Contains(lxc, `"digest":"4202c1092a6b4e06374400f8f332c3b6fadb450b"`) {
		t.Errorf("El digest del 201 debe coincidir con la captura real: %s", lxc)
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
	malo := proxmox.NewClient(e.server.URL+"/api2/json", tokenID, "incorrecto", nil)
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

// ==========================================
// Borrado de instancias (BAC-24B)
// ==========================================

func TestBorrar_InstanciaApagada(t *testing.T) {
	e := nuevoEntorno(t, "")
	for _, caso := range []struct {
		tipo, vmid, tarea string
	}{{"qemu", "110", ":qmdestroy:110:"}, {"lxc", "201", ":vzdestroy:201:"}} {
		s, upid, _ := e.pedir("DELETE", "/nodes/proxmox/"+caso.tipo+"/"+caso.vmid, nil)
		if s != 200 || !strings.Contains(upid.(string), caso.tarea) {
			t.Fatalf("DELETE %s/%s: se esperaba 200 con UPID %s, vino %d %v", caso.tipo, caso.vmid, caso.tarea, s, upid)
		}

		// Mientras corre la tarea, sigue en el inventario con lock y bloqueada para otras acciones.
		_, data, _ := e.pedir("GET", "/cluster/resources?type=vm", nil)
		vmid, _ := strconv.Atoi(caso.vmid)
		if rec := buscar(data, vmid); rec == nil || rec["lock"] != "destroyed" {
			t.Errorf("Durante el borrado debe seguir con lock destroyed: %v", rec)
		}
		if s, _, _ := e.pedir("POST", "/nodes/proxmox/"+caso.tipo+"/"+caso.vmid+"/status/start", url.Values{}); s != 500 {
			t.Errorf("Durante el borrado no se puede encender, vino %d", s)
		}

		e.avanzar(3 * time.Second)
		_, tarea, _ := e.pedir("GET", "/nodes/proxmox/tasks/"+upid.(string)+"/status", nil)
		if tarea.(map[string]any)["exitstatus"] != "OK" {
			t.Errorf("La tarea de borrado debe terminar OK: %v", tarea)
		}
		_, data, _ = e.pedir("GET", "/cluster/resources?type=vm", nil)
		if buscar(data, vmid) != nil {
			t.Errorf("DoD: al terminar la tarea, %s debe desaparecer del inventario", caso.vmid)
		}
		if s, _, cuerpo := e.pedir("GET", "/nodes/proxmox/"+caso.tipo+"/"+caso.vmid+"/status/current", nil); s != 500 || !strings.Contains(cuerpo["message"].(string), "does not exist") {
			t.Errorf("Después de borrarla no debe existir: %d %v", s, cuerpo)
		}
	}
	if _, data, _ := e.pedir("GET", "/cluster/nextid", nil); data != "102" {
		t.Errorf("nextid sigue calculándose bien: %v", data)
	}
}

func TestBorrar_Rechazos(t *testing.T) {
	e := nuevoEntorno(t, "")
	casos := map[string]string{
		"/nodes/proxmox/qemu/9003": "VM 9003 is running - destroy failed", // encendida
		"/nodes/proxmox/lxc/101":   "CT 101 is running - destroy failed",  // encendida
		"/nodes/proxmox/qemu/999":  "does not exist",
	}
	for ruta, mensaje := range casos {
		if s, _, cuerpo := e.pedir("DELETE", ruta, nil); s != 500 || !strings.Contains(cuerpo["message"].(string), mensaje) {
			t.Errorf("DELETE %s: se esperaba 500 %q, vino %d %v", ruta, mensaje, s, cuerpo)
		}
	}

	// La 100 está en HA: apagada tampoco se puede borrar sin purge=1.
	e.pedir("POST", "/nodes/proxmox/qemu/100/status/stop", url.Values{})
	e.avanzar(3 * time.Second)
	if s, _, cuerpo := e.pedir("DELETE", "/nodes/proxmox/qemu/100", nil); s != 500 || !strings.Contains(cuerpo["message"].(string), "used in HA resources and purge parameter not set") {
		t.Errorf("Borrar una instancia HA sin purge: %d %v", s, cuerpo)
	}
	if s, upid, _ := e.pedir("DELETE", "/nodes/proxmox/qemu/100?purge=1", nil); s != 200 || !strings.Contains(upid.(string), ":qmdestroy:100:") {
		t.Errorf("Con purge=1 se puede borrar: %d %v", s, upid)
	}

	// Con otra tarea en curso responde el error de lock.
	e.pedir("POST", "/nodes/proxmox/qemu/110/status/start", url.Values{})
	if s, _, cuerpo := e.pedir("DELETE", "/nodes/proxmox/qemu/110", nil); s != 500 || !strings.Contains(cuerpo["message"].(string), "can't lock file") {
		t.Errorf("Borrar con una tarea en curso: %d %v", s, cuerpo)
	}
}

// ==========================================
// IPs asignadas (inventario unificado)
// ==========================================

func TestRed_EUI64(t *testing.T) {
	if got := ipv6EnlaceLocal("bc:24:11:d1:c0:04"); got != "fe80::be24:11ff:fed1:c004" {
		t.Errorf("IPv6 de enlace local = %s", got)
	}
}

func TestRed_InterfacesLXC(t *testing.T) {
	e := nuevoEntorno(t, "")
	s, data, _ := e.pedir("GET", "/nodes/proxmox/lxc/101/interfaces", nil)
	lista, _ := data.([]any)
	if s != 200 || len(lista) != 2 {
		t.Fatalf("Interfaces de la 101: %d %v", s, data)
	}
	lo, eth0 := lista[0].(map[string]any), lista[1].(map[string]any)
	if lo["name"] != "lo" || lo["inet"] != "127.0.0.1/8" {
		t.Errorf("lo: %v", lo)
	}
	if eth0["name"] != "eth0" || eth0["inet"] != "192.168.1.101/24" || eth0["hwaddr"] != "bc:24:11:3a:10:01" || eth0["inet6"] != "fe80::be24:11ff:fe3a:1001/64" {
		t.Errorf("eth0: %v", eth0)
	}
	ips := eth0["ip-addresses"].([]any)
	if v4 := ips[0].(map[string]any); v4["ip-address"] != "192.168.1.101" || v4["ip-address-type"] != "inet" || v4["prefix"] != "24" {
		t.Errorf("ip-addresses: %v", ips)
	}
	if v6 := ips[1].(map[string]any); v6["ip-address-type"] != "inet6" || v6["prefix"] != "64" {
		t.Errorf("ip-addresses IPv6: %v", ips)
	}
	if s, _, _ := e.pedir("GET", "/nodes/proxmox/qemu/110/interfaces", nil); s != 501 {
		t.Errorf("/interfaces no existe para qemu (501), vino %d", s)
	}
}

// FIX (contrastado con Proxmox VE 9.2.2): en /lxc/{vmid}/interfaces "prefix" es un
// string, y un struct estricto de Go con Prefix string tiene que poder leerlo.
func TestRed_InterfacesLXC_PrefixComoString(t *testing.T) {
	e := nuevoEntorno(t, "")
	_, crudo := e.crudo("/nodes/proxmox/lxc/101/interfaces", authValida)
	for _, esperado := range []string{`"prefix":"24"`, `"prefix":"8"`, `"prefix":"64"`, `"prefix":"128"`} {
		if !strings.Contains(crudo, esperado) {
			t.Errorf("Falta %s en el JSON crudo: %s", esperado, crudo)
		}
	}
	if strings.Contains(crudo, `"prefix":24`) {
		t.Errorf("prefix no puede venir como número en LXC: %s", crudo)
	}

	var estricto struct {
		Data []struct {
			Name        string `json:"name"`
			IPAddresses []struct {
				IPAddress string `json:"ip-address"`
				Tipo      string `json:"ip-address-type"`
				Prefix    string `json:"prefix"`
			} `json:"ip-addresses"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(crudo), &estricto); err != nil {
		t.Fatalf("Un struct estricto con Prefix string debe poder leer la respuesta: %v", err)
	}
	if eth0 := estricto.Data[1]; eth0.Name != "eth0" || eth0.IPAddresses[0].Prefix != "24" || eth0.IPAddresses[0].IPAddress != "192.168.1.101" {
		t.Errorf("eth0 parseada: %+v", eth0)
	}
}

// FIX (contrastado con Proxmox VE 9.2.2, CT 104): un contenedor apagado no es un
// error: 200 con {"data": null}.
func TestRed_InterfacesLXC_ApagadoDevuelveDataNull(t *testing.T) {
	e := nuevoEntorno(t, "")
	status, crudo := e.crudo("/nodes/proxmox/lxc/201/interfaces", authValida) // la 201 arranca apagada
	if status != "200 OK" || strings.TrimSpace(crudo) != `{"data":null}` {
		t.Errorf("Contenedor apagado: se esperaba 200 {\"data\":null}, vino %q %q", status, crudo)
	}

	// Lo mismo si se apaga uno que estaba encendido, y vuelve a tener IP al encenderlo.
	e.pedir("POST", "/nodes/proxmox/lxc/9002/status/stop", url.Values{})
	e.avanzar(3 * time.Second)
	if status, crudo = e.crudo("/nodes/proxmox/lxc/9002/interfaces", authValida); status != "200 OK" || strings.TrimSpace(crudo) != `{"data":null}` {
		t.Errorf("Al apagar la 9002: %q %q", status, crudo)
	}
	e.pedir("POST", "/nodes/proxmox/lxc/9002/status/start", url.Values{})
	e.avanzar(3 * time.Second)
	if _, crudo = e.crudo("/nodes/proxmox/lxc/9002/interfaces", authValida); !strings.Contains(crudo, `"inet":"192.168.1.92/24"`) {
		t.Errorf("Al volver a encenderla debe reportar su IP: %s", crudo)
	}
}

// El guest agent de qemu NO cambia: según la especificación de QEMU (QAPI
// GuestIpAddress) "prefix" es un entero, y Proxmox lo reenvía tal cual.
func TestRed_AgenteQemu_PrefixComoNumero(t *testing.T) {
	e := nuevoEntorno(t, "")
	e.pedir("POST", "/nodes/proxmox/qemu/110/status/start", url.Values{})
	e.avanzar(3 * time.Second)
	_, crudo := e.crudo("/nodes/proxmox/qemu/110/agent/network-get-interfaces", authValida)
	if !strings.Contains(crudo, `"prefix":24`) || strings.Contains(crudo, `"prefix":"24"`) {
		t.Errorf("En el guest agent prefix es un número: %s", crudo)
	}
}

func TestRed_AgenteQemu(t *testing.T) {
	e := nuevoEntorno(t, "")
	ruta := func(vmid string) string { return "/nodes/proxmox/qemu/" + vmid + "/agent/network-get-interfaces" }

	// Errores de Proxmox, sin romper nada (500 con mensaje, como el resto).
	errores := map[string]string{
		"110":  "VM 110 is not running",           // apagada
		"100":  "No QEMU guest agent configured",  // sin agent: 1 en la config
		"9003": "QEMU guest agent is not running", // agente configurado, no instalado
	}
	for vmid, mensaje := range errores {
		if s, _, cuerpo := e.pedir("GET", ruta(vmid), nil); s != 500 || !strings.Contains(cuerpo["message"].(string), mensaje) {
			t.Errorf("VM %s: se esperaba 500 %q, vino %d %v", vmid, mensaje, s, cuerpo)
		}
	}

	e.pedir("POST", "/nodes/proxmox/qemu/110/status/start", url.Values{})
	e.avanzar(3 * time.Second)
	s, data, _ := e.pedir("GET", ruta("110"), nil)
	result, _ := data.(map[string]any)["result"].([]any)
	if s != 200 || len(result) != 2 {
		t.Fatalf("Guest agent de la 110: %d %v", s, data)
	}
	eth0 := result[1].(map[string]any)
	ips := eth0["ip-addresses"].([]any)
	v4, v6 := ips[0].(map[string]any), ips[1].(map[string]any)
	if eth0["name"] != "eth0" || eth0["hardware-address"] != "bc:24:11:5e:22:10" ||
		v4["ip-address"] != "192.168.1.110" || v4["ip-address-type"] != "ipv4" || v4["prefix"] != float64(24) ||
		v6["ip-address-type"] != "ipv6" || !strings.HasPrefix(v6["ip-address"].(string), "fe80::") {
		t.Errorf("eth0 del guest agent: %v", eth0)
	}
	if _, ok := eth0["statistics"].(map[string]any)["rx-bytes"]; !ok {
		t.Errorf("El guest agent trae statistics: %v", eth0)
	}
	if s, _, _ := e.pedir("GET", "/nodes/proxmox/lxc/101/agent/network-get-interfaces", nil); s != 501 {
		t.Errorf("El guest agent no existe para lxc (501), vino %d", s)
	}
}

// DoD: resolver la IP de VMs y contenedores contra el simulador, como lo haría
// el inventario unificado: qemu por el guest agent y lxc por /interfaces.
func TestRed_ResolverIPsDelInventario(t *testing.T) {
	e := nuevoEntorno(t, "")
	e.pedir("POST", "/nodes/proxmox/qemu/110/status/start", url.Values{})
	e.avanzar(3 * time.Second)

	resolver := func(tipo string, vmid int) string {
		if tipo == "lxc" {
			s, data, _ := e.pedir("GET", fmt.Sprintf("/nodes/proxmox/lxc/%d/interfaces", vmid), nil)
			lista, _ := data.([]any) // apagado: 200 con {"data": null} → sin IP, no es un error
			if s != 200 {
				return ""
			}
			for _, x := range lista {
				if itf := x.(map[string]any); itf["name"] != "lo" {
					return strings.Split(itf["inet"].(string), "/")[0]
				}
			}
			return ""
		}
		s, data, _ := e.pedir("GET", fmt.Sprintf("/nodes/proxmox/qemu/%d/agent/network-get-interfaces", vmid), nil)
		if s != 200 {
			return "" // sin agente: el inventario muestra la instancia sin IP
		}
		for _, x := range data.(map[string]any)["result"].([]any) {
			itf := x.(map[string]any)
			if itf["name"] == "lo" {
				continue
			}
			for _, ip := range itf["ip-addresses"].([]any) {
				if d := ip.(map[string]any); d["ip-address-type"] == "ipv4" {
					return d["ip-address"].(string)
				}
			}
		}
		return ""
	}

	// Se consultan TODAS las instancias, también las apagadas: ninguna debe romper.
	_, data, _ := e.pedir("GET", "/cluster/resources?type=vm", nil)
	obtenidas := map[float64]string{}
	for _, x := range data.([]any) {
		rec := x.(map[string]any)
		obtenidas[rec["vmid"].(float64)] = resolver(rec["type"].(string), int(rec["vmid"].(float64)))
	}
	esperadas := map[float64]string{
		100:  "",              // qemu sin agente configurado
		101:  "192.168.1.101", // lxc encendido
		110:  "192.168.1.110", // qemu con agente
		201:  "",              // lxc apagado → {"data": null}
		9000: "",              // qemu apagada
		9001: "",              // qemu apagada
		9002: "192.168.1.92",  // lxc encendido
		9003: "",              // agente no instalado
	}
	for vmid, ip := range esperadas {
		if obtenidas[vmid] != ip {
			t.Errorf("IP de %v = %q, se esperaba %q", vmid, obtenidas[vmid], ip)
		}
	}
}

// El cliente del backend lee GET /nodes/{node}/status del simulador (valores de la captura).
func TestContrato_ClienteDelBackend_EstadoNodo(t *testing.T) {
	e := nuevoEntorno(t, "")
	estado, err := e.cliente().ObtenerEstadoNodo(context.Background(), "proxmox")
	if err != nil {
		t.Fatal(err)
	}
	if estado.CPUs != 12 || estado.MemTotal != 16423432192 || estado.DiscoTotal != 72722055168 || estado.UptimeSegs < 199252 {
		t.Errorf("Estado del nodo inesperado: %+v", estado)
	}
	if estado.MemUsada <= 0 || estado.DiscoUsado <= 0 || estado.CPU <= 0 || estado.CPU >= 1 {
		t.Errorf("Uso de memoria, disco y CPU debe ser coherente: %+v", estado)
	}
	if _, err := e.cliente().ObtenerEstadoNodo(context.Background(), "pve"); err == nil {
		t.Error("Un nodo inexistente debe dar error")
	}
}

// El cliente del backend lee y normaliza las interfaces de VMs (guest agent,
// prefix numérico) y contenedores (prefix string, data null si está apagado).
func TestContrato_ClienteDelBackend_Interfaces(t *testing.T) {
	e := nuevoEntorno(t, "")
	cliente := e.cliente()
	ctx := context.Background()

	// LXC encendido: prefix "24" (string) normalizado a 24.
	lxc, err := cliente.ObtenerInterfaces(ctx, "proxmox", "lxc", 101)
	if err != nil || len(lxc) != 2 {
		t.Fatalf("Interfaces de la 101: %+v, %v", lxc, err)
	}
	if eth0 := lxc[1]; eth0.Nombre != "eth0" || eth0.MAC != "bc:24:11:3a:10:01" || eth0.Direcciones[0] != (ports.DireccionIP{IP: "192.168.1.101", Prefijo: 24, Version: 4}) || eth0.Direcciones[1].Version != 6 {
		t.Errorf("eth0 de la 101: %+v", eth0)
	}

	// LXC apagado: {"data": null} → sin interfaces y sin error.
	if apagado, err := cliente.ObtenerInterfaces(ctx, "proxmox", "lxc", 201); err != nil || len(apagado) != 0 {
		t.Errorf("Un contenedor apagado devuelve lista vacía sin error: %+v, %v", apagado, err)
	}

	// QEMU con agente: prefix 24 (número).
	e.pedir("POST", "/nodes/proxmox/qemu/110/status/start", url.Values{})
	e.avanzar(3 * time.Second)
	vm, err := cliente.ObtenerInterfaces(ctx, "proxmox", "qemu", 110)
	if err != nil || len(vm) != 2 || vm[1].Direcciones[0] != (ports.DireccionIP{IP: "192.168.1.110", Prefijo: 24, Version: 4}) {
		t.Errorf("Interfaces de la 110: %+v, %v", vm, err)
	}

	// QEMU sin agente o apagada: error de Proxmox (el servicio lo convierte en ip = nil).
	for _, vmid := range []int{9003, 100, 9000} {
		if _, err := cliente.ObtenerInterfaces(ctx, "proxmox", "qemu", vmid); err == nil {
			t.Errorf("La VM %d (sin agente o apagada) debe dar error", vmid)
		}
	}
}

// ==========================================
// Storages y Contenido (RF-05, RF-07)
// ==========================================

func TestStorage_ListarYContenido(t *testing.T) {
	e := nuevoEntorno(t, "")

	// 1. GET /nodes/{node}/storage devuelve local, local-lvm y shared-nfs con campos canónicos
	s, data, _ := e.pedir("GET", "/nodes/proxmox/storage", nil)
	if s != 200 {
		t.Fatalf("GET /nodes/proxmox/storage devolvió %d", s)
	}
	storages, ok := data.([]any)
	if !ok || len(storages) < 2 {
		t.Fatalf("Se esperaban al menos 2 storages simulados, vinieron %d: %v", len(storages), data)
	}

	mapa := map[string]map[string]any{}
	for _, item := range storages {
		st := item.(map[string]any)
		nombre := st["storage"].(string)
		mapa[nombre] = st
		if st["active"].(float64) != 1 {
			t.Errorf("Storage %s active debe ser 1", nombre)
		}
		if st["total"].(float64) <= 0 || st["avail"].(float64) < 0 {
			t.Errorf("Storage %s total/avail inválidos: %+v", nombre, st)
		}
	}

	localSt, okLocal := mapa["local"]
	if !okLocal {
		t.Fatal("Falta storage 'local'")
	}
	if localSt["type"] != "dir" || !strings.Contains(localSt["content"].(string), "iso") || !strings.Contains(localSt["content"].(string), "vztmpl") {
		t.Errorf("Storage local tiene configuración inesperada: %+v", localSt)
	}
	if int64(localSt["total"].(float64)) != 107374182400 || int64(localSt["avail"].(float64)) != 64424509440 {
		t.Errorf("Capacidades canónicas de local no coinciden: total=%v avail=%v", localSt["total"], localSt["avail"])
	}

	lvSt, okLvm := mapa["local-lvm"]
	if !okLvm {
		t.Fatal("Falta storage 'local-lvm'")
	}
	if lvSt["type"] != "lvmthin" || !strings.Contains(lvSt["content"].(string), "images") || !strings.Contains(lvSt["content"].(string), "rootdir") {
		t.Errorf("Storage local-lvm tiene configuración inesperada: %+v", lvSt)
	}
	if int64(lvSt["total"].(float64)) != 536870912000 {
		t.Errorf("Capacidades canónicas de local-lvm no coinciden: total=%v", lvSt["total"])
	}

	// 2. Filtro por ?content=iso
	_, dataIso, _ := e.pedir("GET", "/nodes/proxmox/storage?content=iso", nil)
	for _, item := range dataIso.([]any) {
		st := item.(map[string]any)
		if !strings.Contains(st["content"].(string), "iso") {
			t.Errorf("Filtro content=iso devolvió storage sin iso: %+v", st)
		}
	}

	// 3. GET /nodes/{node}/storage/{storage}/content
	s, dataLocal, _ := e.pedir("GET", "/nodes/proxmox/storage/local/content", nil)
	if s != 200 {
		t.Fatalf("GET content local devolvió %d", s)
	}
	itemsLocal := dataLocal.([]any)
	if len(itemsLocal) == 0 {
		t.Fatal("local debe tener contenidos (ISOs y plantillas)")
	}

	tieneISO := false
	tieneVztmpl := false
	tieneIsoCanonico := false
	tieneVztmplCanonico := false

	for _, it := range itemsLocal {
		m := it.(map[string]any)
		if m["content"] == "iso" {
			tieneISO = true
		}
		if m["content"] == "vztmpl" {
			tieneVztmpl = true
		}
		if m["volid"] == "local:iso/ubuntu-24.04-live-server.iso" {
			tieneIsoCanonico = true
			if m["format"] != "iso" || int64(m["size"].(float64)) != 2147483648 {
				t.Errorf("ISO canónico inválido: %+v", m)
			}
		}
		if m["volid"] == "local:vztmpl/debian-12-standard_12.2-1_amd64.tar.zst" {
			tieneVztmplCanonico = true
			if m["format"] != "tar.zst" || int64(m["size"].(float64)) != 134217728 {
				t.Errorf("vztmpl canónico inválido: %+v", m)
			}
		}
		if m["volid"] == nil || m["format"] == nil || m["size"].(float64) <= 0 {
			t.Errorf("Item de contenido incompleto: %+v", m)
		}
	}
	if !tieneISO || !tieneVztmpl {
		t.Errorf("local debe contener tanto iso como vztmpl: %+v", itemsLocal)
	}
	if !tieneIsoCanonico {
		t.Error("Falta el ISO canónico local:iso/ubuntu-24.04-live-server.iso")
	}
	if !tieneVztmplCanonico {
		t.Error("Falta la plantilla canónica local:vztmpl/debian-12-standard_12.2-1_amd64.tar.zst")
	}

	// 4. Filtro por ?content=iso en contenido
	_, dataSoloIso, _ := e.pedir("GET", "/nodes/proxmox/storage/local/content?content=iso", nil)
	for _, it := range dataSoloIso.([]any) {
		m := it.(map[string]any)
		if m["content"] != "iso" {
			t.Errorf("Filtro ?content=iso trajo item de tipo %v", m["content"])
		}
	}

	// 5. Storage inexistente da error 500 como Proxmox
	sInex, _, errCuerpo := e.pedir("GET", "/nodes/proxmox/storage/inexistente/content", nil)
	if sInex != 500 || !strings.Contains(errCuerpo["message"].(string), "does not exist") {
		t.Errorf("Storage inexistente debe responder 500 con 'does not exist', vino %d: %v", sInex, errCuerpo)
	}

	// 6. Nodo inexistente responde error de lookup
	sNodo, _, _ := e.pedir("GET", "/nodes/nodo-invalido/storage", nil)
	if sNodo != 500 {
		t.Errorf("Nodo inválido debe responder 500, vino %d", sNodo)
	}
}

func TestAprovisionamientoYEdicionRecursos(t *testing.T) {
	e := nuevoEntorno(t, "")

	// 1. Aprovisionamiento POST /nodes/{node}/qemu responde 200 con UPID qmcreate
	s, upidQemu, _ := e.pedir("POST", "/nodes/proxmox/qemu", url.Values{
		"vmid":   {"501"},
		"name":   {"vm-etapa2-test"},
		"memory": {"4096"},
		"cores":  {"4"},
		"scsi0":  {"local-lvm:30"},
		"net0":   {"virtio,bridge=vmbr0"},
	})
	if s != 200 || !strings.Contains(upidQemu.(string), ":qmcreate:501:") {
		t.Fatalf("Creación Qemu falló: %d, upid: %v", s, upidQemu)
	}
	e.avanzar(3 * time.Second)

	// 2. Aprovisionamiento POST /nodes/{node}/lxc responde 200 con UPID vzcreate
	s, upidLxc, _ := e.pedir("POST", "/nodes/proxmox/lxc", url.Values{
		"vmid":       {"502"},
		"hostname":   {"ct-etapa2-test"},
		"ostemplate": {"local:vztmpl/debian-12-standard_12.2-1_amd64.tar.zst"},
		"memory":     {"1024"},
		"swap":       {"256"},
		"cores":      {"2"},
		"rootfs":     {"local-lvm:10"},
	})
	if s != 200 || !strings.Contains(upidLxc.(string), ":vzcreate:502:") {
		t.Fatalf("Creación LXC falló: %d, upid: %v", s, upidLxc)
	}
	e.avanzar(3 * time.Second)

	// 3. Edición de recursos PUT /nodes/{node}/qemu/{vmid}/config
	sPut, dataPut, _ := e.pedir("PUT", "/nodes/proxmox/qemu/501/config", url.Values{
		"cores":  {"8"},
		"memory": {"8192"},
	})
	if sPut != 200 || dataPut != nil {
		t.Fatalf("PUT /config VM apagada debe responder 200 con data null: %d %v", sPut, dataPut)
	}

	_, configQemu, _ := e.pedir("GET", "/nodes/proxmox/qemu/501/config", nil)
	cfgQ := configQemu.(map[string]any)
	if cfgQ["cores"] != float64(8) || cfgQ["memory"] != "8192" {
		t.Errorf("Config QEMU no persistió cambios: %+v", cfgQ)
	}

	// 4. Edición de recursos en LXC (swap, memory, cores)
	sPutLxc, _, _ := e.pedir("PUT", "/nodes/proxmox/lxc/502/config", url.Values{
		"cores":  {"4"},
		"memory": {"2048"},
		"swap":   {"512"},
	})
	if sPutLxc != 200 {
		t.Fatalf("PUT /config LXC falló: %d", sPutLxc)
	}
	_, configLxc, _ := e.pedir("GET", "/nodes/proxmox/lxc/502/config", nil)
	cfgL := configLxc.(map[string]any)
	if cfgL["cores"] != float64(4) || cfgL["memory"] != float64(2048) || cfgL["swap"] != float64(512) {
		t.Errorf("Config LXC no persistió cambios: %+v", cfgL)
	}

	// 5. Validar que ninguna de las nuevas rutas devuelva 501 Not Implemented
	rutasAProbar := []string{
		"/nodes/proxmox/storage",
		"/nodes/proxmox/storage/local/content",
		"/nodes/proxmox/storage/shared-nfs/content",
		"/nodes/proxmox/qemu/501/rrddata?timeframe=hour",
		"/nodes/proxmox/lxc/502/rrddata?timeframe=hour",
		"/nodes/proxmox/qemu/501/config",
		"/nodes/proxmox/lxc/502/config",
	}
	for _, r := range rutasAProbar {
		st, _, _ := e.pedir("GET", r, nil)
		if st == 501 {
			t.Errorf("La ruta %s devolvió 501 Not Implemented", r)
		}
	}
}
