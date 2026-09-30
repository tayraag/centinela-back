package main

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// ==========================================
// POST /nodes/{node}/qemu y /nodes/{node}/lxc (RF-07)
// ==========================================

// disco "local-lvm:20" → 20 GB en el storage local-lvm (formato de las capturas).
var discoNuevo = regexp.MustCompile(`^([a-zA-Z0-9_\-]+):(\d+)$`)

// crearInstancia aprovisiona una VM o un contenedor con los parámetros de las
// capturas (vmid, name/hostname, memory, cores, net0, scsi0/rootfs...).
// La instancia aparece enseguida, apagada y con lock "create" hasta que
// termina la tarea (qmcreate / vzcreate).
func (s *Simulador) crearInstancia(w http.ResponseWriter, r *http.Request) {
	if !s.validarNodoYTipo(w, r) {
		return
	}
	tipo := r.PathValue("tipo")
	params, ok := leerParametros(w, r)
	if !ok {
		return
	}

	errores := map[string]string{}
	vmid, err := strconv.Atoi(params["vmid"])
	switch {
	case params["vmid"] == "":
		errores["vmid"] = "property is missing and it is not optional"
	case err != nil:
		errores["vmid"] = fmt.Sprintf("type check ('integer') failed - got '%s'", params["vmid"])
	case vmid < 100 || vmid > 999999999:
		errores["vmid"] = fmt.Sprintf("invalid format - value must be between 100 and 999999999")
	}
	if tipo == tipoLXC && params["ostemplate"] == "" {
		errores["ostemplate"] = "property is missing and it is not optional"
	}
	defectoMem := 512
	memoria := enteroParam(params, "memory", defectoMem, 16, errores)
	cores := enteroParam(params, "cores", 1, 1, errores)
	swap := enteroParam(params, "swap", 512, 0, errores)

	claveDisco := "scsi0"
	if tipo == tipoLXC {
		claveDisco = "rootfs"
	}
	storage, discoGB := "local-lvm", 0
	if v := params[claveDisco]; v != "" {
		m := discoNuevo.FindStringSubmatch(v)
		if m == nil {
			errores[claveDisco] = "invalid format - expected '<storage>:<tamaño en GB>', ej. 'local-lvm:20'"
		} else {
			storage = m[1]
			discoGB, _ = strconv.Atoi(m[2])
		}
	} else if tipo == tipoLXC {
		discoGB = 4
	}
	if len(errores) > 0 {
		errorParametros(w, errores)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.finalizarTareas()
	if _, existe := s.instancias[vmid]; existe {
		// VMs y LXC comparten el espacio de IDs (ver nota de la captura RF-07).
		errorPVE(w, http.StatusInternalServerError, fmt.Sprintf("unable to create VM %d - VM %d already exists on node '%s'", vmid, vmid, s.cfg.Nodo))
		return
	}

	inst := &instancia{
		Vmid: vmid, Tipo: tipo, Estado: "stopped",
		Cores: cores, MemoriaMB: memoria, MaxDisk: int64(discoGB) * gib,
		MemUso: int64(memoria) * mib / 5, Pendiente: map[string]string{},
	}
	mac := fmt.Sprintf("BC:24:11:%02X:%02X:%02X", vmid>>16&0xff, vmid>>8&0xff, vmid&0xff)
	if tipo == tipoQemu {
		inst.Nombre = valorODefecto(params["name"], fmt.Sprintf("VM %d", vmid))
		inst.Config = configQemu(vmid, 0, mac)
		inst.Config["scsihw"] = valorODefecto(params["scsihw"], "virtio-scsi-single")
		if discoGB > 0 {
			inst.Config["scsi0"] = fmt.Sprintf("%s:vm-%d-disk-0,size=%dG", storage, vmid, discoGB)
		}
		if v := params["net0"]; v != "" {
			inst.Config["net0"] = agregarMAC(v, mac, "virtio")
		}
		if v := params["ide2"]; v != "" {
			inst.Config["ide2"] = v
		}
	} else {
		inst.Nombre = valorODefecto(params["hostname"], fmt.Sprintf("CT%d", vmid))
		inst.SwapMB = swap
		inst.DiscoUso = int64(discoGB) * gib / 14
		ostype := "debian"
		if plantilla := params["ostemplate"]; strings.Contains(plantilla, "ubuntu") {
			ostype = "ubuntu"
		}
		inst.Config = configLXC(vmid, ostype, discoGB, mac)
		inst.Config["rootfs"] = fmt.Sprintf("%s:vm-%d-disk-0,size=%dG", storage, vmid, discoGB)
		if v := params["net0"]; v != "" {
			inst.Config["net0"] = agregarMAC(v, mac, "hwaddr")
		}
		// La contraseña de root (params["password"]) no se guarda: igual que
		// Proxmox, no se puede volver a leer por la API.
	}
	inst.IPv4 = s.ipLibre()
	if tipo == tipoQemu && params["agent"] != "" {
		// El agente queda configurado, pero una VM recién creada no tiene sistema
		// operativo instalado: hasta que alguien lo instale, "QEMU guest agent is not running".
		inst.Config["agent"] = params["agent"]
	}
	s.instancias[vmid] = inst

	upid := s.crearTarea(prefijoTarea(inst)+"create", inst, "create", func() string { return "OK" })
	responder(w, upid)
}

// agregarMAC completa la MAC que Proxmox genera al crear la interfaz de red.
func agregarMAC(net0, mac, clave string) string {
	if clave == "virtio" {
		if strings.HasPrefix(net0, "virtio,") {
			return "virtio=" + mac + strings.TrimPrefix(net0, "virtio")
		}
		return net0
	}
	if strings.Contains(net0, "hwaddr=") {
		return net0
	}
	return net0 + ",hwaddr=" + mac + ",type=veth"
}

// ==========================================
// GET /nodes/{node}/{tipo}/{vmid}/config (RF-10)
// ==========================================

// leerConfig devuelve la config con los cambios pendientes aplicados (lo que
// hace Proxmox por defecto); con ?current=1 devuelve la que está en uso.
func (s *Simulador) leerConfig(w http.ResponseWriter, r *http.Request) {
	inst := s.buscarInstancia(w, r)
	if inst == nil {
		return
	}
	defer s.mu.Unlock()
	responder(w, inst.configCompleta(r.URL.Query().Get("current") != "1"))
}

// ==========================================
// PUT /nodes/{node}/{tipo}/{vmid}/config (RF-10)
// ==========================================

// clavesNoEditables son parámetros que Proxmox no deja cambiar por PUT /config.
var clavesNoEditables = map[string]bool{"vmid": true, "digest": true, "lock": true, "parent": true, "snapname": true}

// editarConfig responde {"data": null} como en la captura. En una VM qemu
// encendida, memory/cores/sockets quedan como "cambio pendiente" hasta el
// próximo reinicio; en un lxc se aplican en caliente (igual que en Proxmox).
func (s *Simulador) editarConfig(w http.ResponseWriter, r *http.Request) {
	params, ok := leerParametros(w, r)
	if !ok {
		return
	}
	inst := s.buscarInstancia(w, r)
	if inst == nil {
		return
	}
	defer s.mu.Unlock()
	if inst.Lock != "" {
		errorPVE(w, http.StatusInternalServerError, fmt.Sprintf("%s is locked (%s)", inst.etiqueta(), inst.Lock))
		return
	}

	errores := map[string]string{}
	if d := params["digest"]; d != "" && d != inst.configCompleta(true)["digest"] {
		errorPVE(w, http.StatusInternalServerError, "detected modified configuration - file changed by other user? Try again.")
		return
	}
	enteroParam(params, "memory", 0, 16, errores)
	enteroParam(params, "cores", 0, 1, errores)
	enteroParam(params, "swap", 0, 0, errores)
	for clave := range params {
		if clavesNoEditables[clave] && clave != "digest" {
			errores[clave] = "property is read-only"
		}
	}
	if (inst.Tipo == tipoQemu && params["hostname"] != "") || (inst.Tipo == tipoLXC && params["name"] != "") {
		errores["name"] = "property is not defined in schema and the schema does not allow additional properties"
	}
	if len(errores) > 0 {
		errorParametros(w, errores)
		return
	}

	for clave, valor := range params {
		if clave == "digest" {
			continue
		}
		enCaliente := inst.Tipo == tipoLXC || !inst.encendida()
		if (clave == "memory" || clave == "cores" || clave == "sockets") && !enCaliente {
			inst.Pendiente[clave] = valor
			continue
		}
		inst.setConfig(clave, valor)
	}
	responder(w, nil)
}

// enteroParam valida un parámetro entero opcional con un mínimo. Si falta,
// devuelve el valor por defecto; si es inválido lo anota en errores.
func enteroParam(params map[string]string, clave string, defecto, minimo int, errores map[string]string) int {
	v, existe := params[clave]
	if !existe || v == "" {
		return defecto
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		errores[clave] = fmt.Sprintf("type check ('integer') failed - got '%s'", v)
		return defecto
	}
	if n < minimo {
		errores[clave] = fmt.Sprintf("value must have a minimum value of %d", minimo)
		return defecto
	}
	return n
}

// ==========================================
// DELETE /nodes/{node}/{tipo}/{vmid} (BAC-24B)
// ==========================================

// borrarInstancia imita el destroy de Proxmox (no está en las capturas; sigue
// la documentación de la API de Proxmox VE):
//   - encendida → 500 "VM 110 is running - destroy failed" (CT: "CT 101 is running - destroy failed");
//   - administrada por HA sin purge=1 → 500 "unable to remove VM 100 - used in HA resources and purge parameter not set.";
//   - ocupada con otra tarea → el error de lock;
//   - apagada → 200 con el UPID (qmdestroy / vzdestroy). La instancia se quita
//     del inventario cuando la tarea termina.
func (s *Simulador) borrarInstancia(w http.ResponseWriter, r *http.Request) {
	purge := r.URL.Query().Get("purge") == "1"
	inst := s.buscarInstancia(w, r)
	if inst == nil {
		return
	}
	defer s.mu.Unlock()
	if !s.verificarLibre(w, inst) {
		return
	}
	if inst.encendida() {
		errorPVE(w, http.StatusInternalServerError, inst.etiqueta()+" is running - destroy failed")
		return
	}
	if inst.HA && !purge {
		errorPVE(w, http.StatusInternalServerError, fmt.Sprintf("unable to remove %s - used in HA resources and purge parameter not set.", inst.etiqueta()))
		return
	}

	vmid := inst.Vmid
	upid := s.crearTarea(prefijoTarea(inst)+"destroy", inst, "destroyed", func() string {
		delete(s.instancias, vmid)
		return "OK"
	})
	responder(w, upid)
}

// ipLibre asigna a una instancia nueva la primera IP 192.168.1.150-250 sin usar.
func (s *Simulador) ipLibre() string {
	usadas := map[string]bool{}
	for _, inst := range s.instancias {
		usadas[inst.IPv4] = true
	}
	for n := 150; n <= 250; n++ {
		if ip := fmt.Sprintf("192.168.1.%d", n); !usadas[ip] {
			return ip
		}
	}
	return "192.168.1.254"
}
