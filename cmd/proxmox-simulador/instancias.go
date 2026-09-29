package main

import (
	"fmt"
	"net/http"
	"sort"
	"time"
)

// ==========================================
// GET /cluster/resources (RF-02, RF-03)
// ==========================================

// clusterResources devuelve instancias, nodo, storages y red, igual que la
// captura. Acepta ?type=vm|node|storage|sdn como el Proxmox real.
func (s *Simulador) clusterResources(w http.ResponseWriter, r *http.Request) {
	filtro := r.URL.Query().Get("type")
	if filtro != "" && filtro != "vm" && filtro != "node" && filtro != "storage" && filtro != "sdn" {
		errorParametros(w, map[string]string{"type": "value '" + filtro + "' does not have a value in the enumeration 'vm, storage, node, sdn'"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.finalizarTareas()
	ahora := s.ahora()

	recursos := []map[string]any{}
	if filtro == "" || filtro == "vm" {
		for _, inst := range s.ordenadas() {
			m := s.metricasActuales(inst, ahora)
			rec := map[string]any{
				"id":        fmt.Sprintf("%s/%d", inst.Tipo, inst.Vmid),
				"type":      inst.Tipo,
				"vmid":      inst.Vmid,
				"name":      inst.Nombre,
				"node":      s.cfg.Nodo,
				"status":    inst.Estado,
				"template":  0,
				"maxcpu":    inst.Cores,
				"maxmem":    int64(inst.MemoriaMB) * mib,
				"maxdisk":   inst.MaxDisk,
				"disk":      m.disk,
				"mem":       m.mem,
				"memhost":   m.memhost,
				"cpu":       m.cpu,
				"uptime":    inst.uptime(ahora),
				"netin":     m.netin,
				"netout":    m.netout,
				"diskread":  m.diskread,
				"diskwrite": m.diskwrite,
			}
			if inst.HA {
				rec["hastate"] = map[bool]string{true: "started", false: "stopped"}[inst.encendida()]
			}
			if inst.Lock != "" {
				rec["lock"] = inst.Lock
			}
			recursos = append(recursos, rec)
		}
	}
	if filtro == "" || filtro == "node" {
		nodo := s.resumenNodo(ahora)
		recursos = append(recursos, map[string]any{
			"id": "node/" + s.cfg.Nodo, "type": "node", "node": s.cfg.Nodo, "status": "online",
			"hastate": "online", "level": "", "cgroup-mode": 2,
			"maxcpu": nodo.cpus, "cpu": nodo.cpu, "maxmem": nodo.memTotal, "mem": nodo.memUsada,
			"maxdisk": nodo.discoTotal, "disk": nodo.discoUsado, "uptime": nodo.uptime,
		})
	}
	if filtro == "" || filtro == "storage" {
		recursos = append(recursos,
			map[string]any{"id": "storage/" + s.cfg.Nodo + "/local-lvm", "type": "storage", "node": s.cfg.Nodo, "storage": "local-lvm",
				"status": "available", "shared": 0, "content": "rootdir,images", "plugintype": "lvmthin", "maxdisk": int64(151636672512), "disk": s.discoLVMUsado()},
			map[string]any{"id": "storage/" + s.cfg.Nodo + "/local", "type": "storage", "node": s.cfg.Nodo, "storage": "local",
				"status": "available", "shared": 0, "content": "iso,import,vztmpl,backup", "plugintype": "dir", "maxdisk": int64(72722055168), "disk": int64(5477761024)},
		)
	}
	if filtro == "" || filtro == "sdn" {
		recursos = append(recursos, map[string]any{"id": "network/" + s.cfg.Nodo + "/zone/localnetwork", "type": "network",
			"network-type": "zone", "network": "localnetwork", "node": s.cfg.Nodo, "status": "ok"})
	}
	responder(w, recursos)
}

// ordenadas devuelve las instancias por vmid, para respuestas estables.
func (s *Simulador) ordenadas() []*instancia {
	lista := make([]*instancia, 0, len(s.instancias))
	for _, inst := range s.instancias {
		lista = append(lista, inst)
	}
	sort.Slice(lista, func(a, b int) bool { return lista[a].Vmid < lista[b].Vmid })
	return lista
}

func (s *Simulador) discoLVMUsado() int64 {
	var total int64 = 1024 * mib
	for _, inst := range s.instancias {
		total += inst.MaxDisk / 10
	}
	return total
}

// ==========================================
// GET /nodes/{node}/status (RF-02, RF-07, RF-11)
// ==========================================

type resumenNodo struct {
	cpus                                       int
	cpu                                        float64
	memTotal, memUsada, discoTotal, discoUsado int64
	uptime                                     int64
}

func (s *Simulador) resumenNodo(ahora time.Time) resumenNodo {
	// Base de la captura: ~2.3 GB usados por el propio host.
	memUsada := int64(2339323904)
	cpu := 0.004 + 0.003*ruido(0, ahora.Unix())
	for _, inst := range s.instancias {
		if inst.encendida() {
			m := s.metricasActuales(inst, ahora)
			memUsada += m.mem
			cpu += m.cpu * float64(inst.Cores) / 12
		}
	}
	return resumenNodo{
		cpus: 12, cpu: cpu, memTotal: 16423432192, memUsada: memUsada,
		discoTotal: 72722055168, discoUsado: 5477761024,
		uptime: 199252 + int64(ahora.Sub(s.inicio).Seconds()),
	}
}

func (s *Simulador) estadoNodo(w http.ResponseWriter, r *http.Request) {
	if !s.validarNodoYTipo(w, r) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finalizarTareas()
	n := s.resumenNodo(s.ahora())
	libre := n.memTotal - n.memUsada

	responder(w, map[string]any{
		"memory": map[string]any{"total": n.memTotal, "used": n.memUsada, "free": libre - 1084870656, "available": libre},
		"swap":   map[string]any{"total": int64(8589930496), "used": 0, "free": int64(8589930496)},
		"rootfs": map[string]any{"total": n.discoTotal, "used": n.discoUsado, "free": n.discoTotal - n.discoUsado, "avail": n.discoTotal - n.discoUsado - 3740000000},
		"cpu":    n.cpu,
		"cpuinfo": map[string]any{
			"model": "12th Gen Intel(R) Core(TM) i5-12500T", "vendor": "GenuineIntel", "family": "6", "hvm": "1",
			"sockets": 1, "cores": 6, "cpus": n.cpus, "mhz": "4400.000", "user_hz": 100,
			"flags": "fpu vme de pse tsc msr pae mce cx8 apic sep mtrr pge mca cmov pat pse36 clflush dts acpi mmx fxsr sse sse2 ss ht tm pbe syscall nx pdpe1gb rdtscp lm constant_tsc vmx aes avx avx2",
		},
		"loadavg":        []string{"0.04", "0.04", "0.04"},
		"uptime":         n.uptime,
		"idle":           0,
		"wait":           0,
		"ksm":            map[string]any{"shared": 0},
		"boot-info":      map[string]any{"mode": "efi", "secureboot": 0},
		"pveversion":     "pve-manager/9.2.2/b9984c6d90a4bd80",
		"kversion":       "Linux 7.0.2-6-pve #1 SMP PREEMPT_DYNAMIC PMX 7.0.2-6 (2026-05-20T08:55Z)",
		"current-kernel": map[string]any{"sysname": "Linux", "release": "7.0.2-6-pve", "machine": "x86_64", "version": "#1 SMP PREEMPT_DYNAMIC PMX 7.0.2-6 (2026-05-20T08:55Z)"},
	})
}

// ==========================================
// GET /nodes/{node}/qemu y /nodes/{node}/lxc (listado por tipo)
// ==========================================

func (s *Simulador) listarPorTipo(w http.ResponseWriter, r *http.Request) {
	if !s.validarNodoYTipo(w, r) {
		return
	}
	tipo := r.PathValue("tipo")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finalizarTareas()
	ahora := s.ahora()

	lista := []map[string]any{}
	for _, inst := range s.ordenadas() {
		if inst.Tipo == tipo {
			lista = append(lista, s.estadoDetallado(inst, ahora))
		}
	}
	responder(w, lista)
}

// ==========================================
// GET /nodes/{node}/{tipo}/{vmid}/status/current (RF-05, RF-11)
// ==========================================

func (s *Simulador) estadoActual(w http.ResponseWriter, r *http.Request) {
	inst := s.buscarInstancia(w, r)
	if inst == nil {
		return
	}
	defer s.mu.Unlock()
	responder(w, s.estadoDetallado(inst, s.ahora()))
}

// estadoDetallado arma el objeto de status/current (y de cada elemento del
// listado por tipo) con los campos de las capturas de RF-05.
func (s *Simulador) estadoDetallado(inst *instancia, ahora time.Time) map[string]any {
	m := s.metricasActuales(inst, ahora)
	e := map[string]any{
		"vmid":      inst.Vmid,
		"name":      inst.Nombre,
		"status":    inst.Estado,
		"cpus":      inst.Cores,
		"cpu":       m.cpu,
		"mem":       m.mem,
		"maxmem":    int64(inst.MemoriaMB) * mib,
		"disk":      m.disk,
		"maxdisk":   inst.MaxDisk,
		"uptime":    inst.uptime(ahora),
		"netin":     m.netin,
		"netout":    m.netout,
		"diskread":  m.diskread,
		"diskwrite": m.diskwrite,
	}
	if inst.encendida() {
		e["pid"] = inst.PID
	}
	if inst.Lock != "" {
		e["lock"] = inst.Lock
	}
	if inst.Tipo == tipoQemu {
		e["qmpstatus"] = inst.Estado
		e["memhost"] = m.memhost
		ha := map[string]any{"managed": 0}
		if inst.HA {
			ha = map[string]any{"managed": 1, "state": map[bool]string{true: "started", false: "stopped"}[inst.encendida()]}
		}
		e["ha"] = ha
	} else {
		e["type"] = tipoLXC
		e["swap"] = 0
		e["maxswap"] = int64(inst.SwapMB) * mib
		for _, p := range []string{"pressurecpusome", "pressurecpufull", "pressureiosome", "pressureiofull", "pressurememorysome", "pressurememoryfull"} {
			e[p] = "0.00"
		}
	}
	return e
}

// ==========================================
// POST /nodes/{node}/{tipo}/{vmid}/status/{accion} (RF-04)
// ==========================================

// accionesPorTipo son las acciones de energía que acepta cada tipo (las de las capturas).
var accionesPorTipo = map[string]map[string]bool{
	tipoQemu: {"start": true, "stop": true, "shutdown": true, "reboot": true, "reset": true},
	tipoLXC:  {"start": true, "stop": true, "shutdown": true, "reboot": true},
}

func (s *Simulador) accionEstado(w http.ResponseWriter, r *http.Request) {
	accion := r.PathValue("accion")
	if tipo := r.PathValue("tipo"); (tipo == tipoQemu || tipo == tipoLXC) && !accionesPorTipo[tipo][accion] {
		errorPVE(w, http.StatusNotImplemented, fmt.Sprintf("Method 'POST /nodes/%s/%s/%s/status/%s' not implemented", r.PathValue("node"), tipo, r.PathValue("vmid"), accion))
		return
	}
	inst := s.buscarInstancia(w, r)
	if inst == nil {
		return
	}
	defer s.mu.Unlock()
	if !s.verificarLibre(w, inst) {
		return
	}

	upid := s.crearTarea(nombreTarea(inst, accion), inst, "", func() string { return s.aplicarAccion(inst, accion) })
	responder(w, upid)
}

// nombreTarea replica los tipos de tarea de las capturas: qmstart, vzstop...
// y hastart/hastop para las instancias administradas por HA (como la 100).
func nombreTarea(inst *instancia, accion string) string {
	if inst.HA {
		switch accion {
		case "start":
			return "hastart"
		case "stop", "shutdown":
			return "hastop"
		}
	}
	prefijo := "qm"
	if inst.Tipo == tipoLXC {
		prefijo = "vz"
	}
	return prefijo + accion
}

// aplicarAccion es el efecto de la tarea al terminar. Devuelve el exitstatus.
func (s *Simulador) aplicarAccion(inst *instancia, accion string) string {
	ahora := s.ahora()
	encender := func() {
		inst.Estado = "running"
		inst.Iniciada = ahora
		s.proximoPID += 13
		inst.PID = s.proximoPID
		inst.aplicarPendientes()
	}

	switch accion {
	case "start":
		if inst.encendida() {
			return inst.etiqueta() + " already running"
		}
		encender()
	case "stop":
		if inst.encendida() {
			inst.Estado, inst.DetenidaEn, inst.PID = "stopped", ahora, 0
		}
	case "shutdown":
		if !inst.encendida() {
			return inst.etiqueta() + " not running"
		}
		inst.Estado, inst.DetenidaEn, inst.PID = "stopped", ahora, 0
	case "reboot":
		if !inst.encendida() {
			return inst.etiqueta() + " not running"
		}
		encender()
	case "reset":
		if !inst.encendida() {
			return inst.etiqueta() + " not running"
		}
		inst.Iniciada = ahora
	}
	return "OK"
}
