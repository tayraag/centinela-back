package main

import (
	"crypto/sha1"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	tipoQemu = "qemu"
	tipoLXC  = "lxc"

	mib = 1024 * 1024
	gib = 1024 * mib
)

// instancia es una VM (qemu) o un contenedor (lxc) del simulador.
type instancia struct {
	Vmid   int
	Tipo   string // "qemu" | "lxc"
	Nombre string
	Estado string // "running" | "stopped"
	HA     bool   // administrada por HA: sus tareas se llaman hastart/hastop, como en las capturas

	Cores     int
	MemoriaMB int
	SwapMB    int   // solo lxc
	MaxDisk   int64 // bytes
	DiscoUso  int64 // bytes usados (lxc; en qemu Proxmox reporta 0)
	MemUso    int64 // memoria que "consume" mientras está encendida

	Iniciada   time.Time // desde cuándo está encendida (para uptime)
	DetenidaEn time.Time // última vez que se apagó (para el historial de métricas)
	PID        int

	Lock      string            // "create", "snapshot", "rollback", ... mientras corre una tarea que bloquea la config
	Config    map[string]string // resto de la config, tal como la devuelve GET /config
	Pendiente map[string]string // cambios de config en una VM encendida, se aplican al reiniciar

	Snapshots []*snapshot
	Actual    string // snapshot padre del estado actual ("" si no hay)
}

// inventarioInicial reproduce el inventario de las capturas reales
// ("API proxmox respuestas/data extraida de las peticiones.md", RF-02 y RF-10).
// Las instancias encendidas arrancan con el uptime que tenían en la captura.
func inventarioInicial(ahora time.Time) []*instancia {
	encendida := func(uptime int) time.Time { return ahora.Add(-time.Duration(uptime) * time.Second) }

	lista := []*instancia{
		{
			Vmid: 100, Tipo: tipoQemu, Nombre: "PruebaLucas", Estado: "running", HA: true,
			Cores: 1, MemoriaMB: 2048, MaxDisk: 10 * gib, MemUso: 241901568, Iniciada: encendida(2484),
			Config: map[string]string{
				"meta":    "creation-qemu=11.0.0,ctime=1786823333",
				"smbios1": "uuid=53024778-9250-4285-b4dd-57ea1e12f327",
				"net0":    "virtio=BC:24:11:D1:C0:04,bridge=vmbr0,firewall=1",
				"scsi0":   "local-lvm:vm-100-disk-0,iothread=1,size=10G",
				"cpu":     "x86-64-v2-AES",
				"vmgenid": "398ed4e9-1f88-4e86-a973-3e5fd4727b78",
				"ostype":  "l26",
				"boot":    "order=scsi0;ide2;net0",
				"scsihw":  "virtio-scsi-single",
				"numa":    "0",
				"ide2":    "local:iso/debian-13.6.0-amd64-netinst.iso,media=cdrom,size=755M",
				"sockets": "1",
			},
			Snapshots: []*snapshot{
				{Nombre: "mi_primer_snapshot", SnapTime: 1787092953},
				{Nombre: "mi_segundo_snapshot", SnapTime: 1787094110, Padre: "mi_primer_snapshot"},
			},
			Actual: "mi_segundo_snapshot",
		},
		{
			Vmid: 101, Tipo: tipoLXC, Nombre: "back", Estado: "running",
			Cores: 1, MemoriaMB: 1024, SwapMB: 512, MaxDisk: 8350298112, DiscoUso: 689111040, MemUso: 25628672, Iniciada: encendida(2234),
			Config: configLXC(101, "debian", 8, "BC:24:11:3A:10:01"),
		},
		{
			Vmid: 110, Tipo: tipoQemu, Nombre: "Servidor-Prueba", Estado: "stopped",
			Cores: 2, MemoriaMB: 2048, MaxDisk: 20 * gib, MemUso: 412090368,
			Config: configQemu(110, 20, "BC:24:11:5E:22:10"),
		},
		{
			Vmid: 9000, Tipo: tipoQemu, Nombre: "test-vm-go-9000", Estado: "stopped",
			Cores: 1, MemoriaMB: 512, MaxDisk: 0, MemUso: 201326592,
			Config: configQemu(9000, 0, "BC:24:11:90:00:00"),
		},
		{
			Vmid: 9001, Tipo: tipoQemu, Nombre: "test-vm-go-9001", Estado: "stopped",
			Cores: 1, MemoriaMB: 512, MaxDisk: 0, MemUso: 201326592,
			Config: configQemu(9001, 0, "BC:24:11:90:01:00"),
		},
		{
			Vmid: 9002, Tipo: tipoLXC, Nombre: "test-lxc-go-9002", Estado: "running",
			Cores: 1, MemoriaMB: 512, SwapMB: 512, MaxDisk: 4143677440, DiscoUso: 688058368, MemUso: 22872064, Iniciada: encendida(2316),
			Config: configLXC(9002, "debian", 4, "BC:24:11:90:02:00"),
		},
		{
			Vmid: 9003, Tipo: tipoQemu, Nombre: "test-vm-go-9003", Estado: "running",
			Cores: 1, MemoriaMB: 512, MaxDisk: 0, MemUso: 217640960, Iniciada: encendida(2483),
			Config: configQemu(9003, 0, "BC:24:11:90:03:00"),
		},
		{
			// Contenedor creado desde Postman en la captura de RF-07; su config es la de RF-10.
			Vmid: 201, Tipo: tipoLXC, Nombre: "Contenedor-Prueba-Postman", Estado: "stopped",
			Cores: 1, MemoriaMB: 512, SwapMB: 512, MaxDisk: 8 * gib, DiscoUso: 599904256, MemUso: 21046067,
			Config: map[string]string{
				"net0":         "name=eth0,bridge=vmbr0,hwaddr=BC:24:11:07:CC:54,ip=dhcp,type=veth",
				"arch":         "amd64",
				"unprivileged": "1",
				"ostype":       "debian",
				"rootfs":       "local-lvm:vm-201-disk-0,size=8G",
			},
		},
	}
	for i, inst := range lista {
		if inst.Estado == "running" {
			inst.PID = 520000 + i*1000
		}
		inst.Pendiente = map[string]string{}
		for _, snap := range inst.Snapshots {
			snap.cores, snap.memoriaMB = inst.Cores, inst.MemoriaMB
		}
	}
	return lista
}

func configQemu(vmid, discoGB int, mac string) map[string]string {
	cfg := map[string]string{
		"net0":    fmt.Sprintf("virtio=%s,bridge=vmbr0,firewall=1", mac),
		"cpu":     "x86-64-v2-AES",
		"ostype":  "l26",
		"boot":    "order=scsi0;ide2;net0",
		"scsihw":  "virtio-scsi-single",
		"numa":    "0",
		"sockets": "1",
		"smbios1": fmt.Sprintf("uuid=00000000-0000-4000-8000-%012d", vmid),
	}
	if discoGB > 0 {
		cfg["scsi0"] = fmt.Sprintf("local-lvm:vm-%d-disk-0,iothread=1,size=%dG", vmid, discoGB)
	}
	return cfg
}

func configLXC(vmid int, ostype string, discoGB int, mac string) map[string]string {
	return map[string]string{
		"net0":         fmt.Sprintf("name=eth0,bridge=vmbr0,hwaddr=%s,ip=dhcp,type=veth", mac),
		"arch":         "amd64",
		"unprivileged": "1",
		"ostype":       ostype,
		"rootfs":       fmt.Sprintf("local-lvm:vm-%d-disk-0,size=%dG", vmid, discoGB),
	}
}

// ==========================================
// Helpers de la instancia
// ==========================================

func (i *instancia) encendida() bool { return i.Estado == "running" }

// uptime en segundos (0 si está apagada).
func (i *instancia) uptime(ahora time.Time) int64 {
	if !i.encendida() {
		return 0
	}
	return int64(ahora.Sub(i.Iniciada).Seconds())
}

// etiqueta es cómo Proxmox nombra la instancia en los mensajes de error.
func (i *instancia) etiqueta() string {
	if i.Tipo == tipoLXC {
		return fmt.Sprintf("CT %d", i.Vmid)
	}
	return fmt.Sprintf("VM %d", i.Vmid)
}

// clavesNumericas son las claves de config que Proxmox devuelve como número
// JSON (sin comillas), según las capturas reales. Ojo: en qemu "memory" NO está,
// porque la API real la devuelve como string ("memory": "2048").
var clavesNumericas = map[string]map[string]bool{
	tipoQemu: {"cores": true, "sockets": true, "numa": true, "onboot": true},
	tipoLXC:  {"cores": true, "memory": true, "swap": true, "unprivileged": true, "onboot": true},
}

// tipar convierte a entero los valores que Proxmox devuelve como número.
func (i *instancia) tipar(clave, valor string) any {
	if clavesNumericas[i.Tipo][clave] {
		if n, err := strconv.Atoi(valor); err == nil {
			return n
		}
	}
	return valor
}

// configCompleta arma la config como la devuelve GET /config. Con pendientes
// aplicados (lo que devuelve Proxmox por defecto) o la actual (?current=1).
func (i *instancia) configCompleta(conPendientes bool) map[string]any {
	cfg := map[string]any{}
	for k, v := range i.Config {
		cfg[k] = i.tipar(k, v)
	}
	cfg["cores"] = i.Cores
	if i.Tipo == tipoQemu {
		// En qemu Proxmox devuelve memory como string (ver captura RF-10).
		cfg["memory"] = fmt.Sprint(i.MemoriaMB)
		cfg["name"] = i.Nombre
	} else {
		cfg["memory"] = i.MemoriaMB
		cfg["swap"] = i.SwapMB
		cfg["hostname"] = i.Nombre
	}
	if conPendientes {
		for k, v := range i.Pendiente {
			cfg[k] = i.tipar(k, v)
		}
	}
	if i.Actual != "" {
		cfg["parent"] = i.Actual
	}
	if i.Lock != "" {
		cfg["lock"] = i.Lock
	}
	cfg["digest"] = digest(cfg)
	return cfg
}

// digest imita el hash SHA-1 que Proxmox agrega a cada config.
func digest(cfg map[string]any) string {
	claves := make([]string, 0, len(cfg))
	for k := range cfg {
		if k != "digest" {
			claves = append(claves, k)
		}
	}
	sort.Strings(claves)
	var b strings.Builder
	for _, k := range claves {
		fmt.Fprintf(&b, "%s: %v\n", k, cfg[k])
	}
	return fmt.Sprintf("%x", sha1.Sum([]byte(b.String())))
}

// aplicarPendientes vuelca los cambios de config pendientes (se llama al encender/reiniciar).
func (i *instancia) aplicarPendientes() {
	for k, v := range i.Pendiente {
		i.setConfig(k, v)
	}
	i.Pendiente = map[string]string{}
}

// setConfig aplica un valor de config ya validado.
func (i *instancia) setConfig(clave, valor string) {
	switch clave {
	case "cores":
		fmt.Sscan(valor, &i.Cores)
	case "memory":
		fmt.Sscan(valor, &i.MemoriaMB)
	case "swap":
		fmt.Sscan(valor, &i.SwapMB)
	case "name", "hostname":
		i.Nombre = valor
	default:
		i.Config[clave] = valor
	}
}
