package main

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// Los formatos de este archivo no están en las capturas: siguen la
// documentación de la API de Proxmox VE. Conviene confirmarlos con una captura
// real cuando haya acceso (ver docs/simulador-proxmox.md).

// macDeNet0 saca la MAC de net0: "virtio=BC:24:11:D1:C0:04,bridge=..." (qemu)
// o "name=eth0,bridge=vmbr0,hwaddr=BC:24:11:07:CC:54,..." (lxc).
var macDeNet0 = regexp.MustCompile(`(?i)(?:virtio|e1000|rtl8139|vmxnet3|hwaddr)=([0-9a-f]{2}(?::[0-9a-f]{2}){5})`)

func (i *instancia) mac() string {
	if m := macDeNet0.FindStringSubmatch(i.Config["net0"]); m != nil {
		return strings.ToLower(m[1])
	}
	return "bc:24:11:00:00:00"
}

// ipv6EnlaceLocal calcula la dirección fe80:: que Linux deriva de la MAC (EUI-64):
// se invierte el bit 7 del primer byte y se inserta ff:fe en el medio.
func ipv6EnlaceLocal(mac string) string {
	var b [6]int
	fmt.Sscanf(mac, "%x:%x:%x:%x:%x:%x", &b[0], &b[1], &b[2], &b[3], &b[4], &b[5])
	return fmt.Sprintf("fe80::%x:%x:%x:%x", (b[0]^0x02)<<8|b[1], b[2]<<8|0xff, 0xfe<<8|b[3], b[4]<<8|b[5])
}

// ==========================================
// GET /nodes/{node}/lxc/{vmid}/interfaces
// ==========================================

// interfacesLXC devuelve las interfaces del contenedor (solo si está encendido):
// lo y eth0, con los campos clásicos (hwaddr, inet, inet6) y la lista
// ip-addresses con el mismo formato que el guest agent de qemu.
func (s *Simulador) interfacesLXC(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("tipo") == tipoQemu {
		errorPVE(w, http.StatusNotImplemented, fmt.Sprintf("Method 'GET /nodes/%s/qemu/%s/interfaces' not implemented", r.PathValue("node"), r.PathValue("vmid")))
		return
	}
	inst := s.buscarInstancia(w, r)
	if inst == nil {
		return
	}
	defer s.mu.Unlock()
	if !inst.encendida() {
		errorPVE(w, http.StatusInternalServerError, fmt.Sprintf("CT %d not running", inst.Vmid))
		return
	}

	mac, ip6 := inst.mac(), ipv6EnlaceLocal(inst.mac())
	responder(w, []map[string]any{
		{
			"name": "lo", "hwaddr": "00:00:00:00:00:00", "hardware-address": "00:00:00:00:00:00",
			"inet": "127.0.0.1/8", "inet6": "::1/128",
			"ip-addresses": direcciones("127.0.0.1", 8, "::1", 128, "inet"),
		},
		{
			"name": "eth0", "hwaddr": mac, "hardware-address": mac,
			"inet": inst.IPv4 + "/24", "inet6": ip6 + "/64",
			"ip-addresses": direcciones(inst.IPv4, 24, ip6, 64, "inet"),
		},
	})
}

// ==========================================
// GET /nodes/{node}/qemu/{vmid}/agent/network-get-interfaces
// ==========================================

// interfacesAgenteQemu imita la respuesta del QEMU Guest Agent: {"data": {"result": [...]}}.
// Errores, como en Proxmox:
//   - VM apagada → 500 "VM 110 is not running"
//   - sin "agent: 1" en la config → 500 "No QEMU guest agent configured"
//   - agente configurado pero no instalado/corriendo en la VM → 500 "QEMU guest agent is not running"
func (s *Simulador) interfacesAgenteQemu(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("tipo") == tipoLXC {
		errorPVE(w, http.StatusNotImplemented, fmt.Sprintf("Method 'GET /nodes/%s/lxc/%s/agent/network-get-interfaces' not implemented", r.PathValue("node"), r.PathValue("vmid")))
		return
	}
	inst := s.buscarInstancia(w, r)
	if inst == nil {
		return
	}
	defer s.mu.Unlock()
	switch {
	case !inst.encendida():
		errorPVE(w, http.StatusInternalServerError, fmt.Sprintf("VM %d is not running", inst.Vmid))
		return
	case inst.Config["agent"] == "" || strings.HasPrefix(inst.Config["agent"], "0"):
		errorPVE(w, http.StatusInternalServerError, "No QEMU guest agent configured")
		return
	case !inst.AgenteCorre:
		errorPVE(w, http.StatusInternalServerError, "QEMU guest agent is not running")
		return
	}

	m := s.metricasActuales(inst, s.ahora())
	mac, ip6 := inst.mac(), ipv6EnlaceLocal(inst.mac())
	responder(w, map[string]any{"result": []map[string]any{
		{
			"name": "lo", "hardware-address": "00:00:00:00:00:00",
			"ip-addresses": direcciones("127.0.0.1", 8, "::1", 128, "ipv4"),
			"statistics":   estadisticas(3200, 3200),
		},
		{
			"name": "eth0", "hardware-address": mac,
			"ip-addresses": direcciones(inst.IPv4, 24, ip6, 64, "ipv4"),
			"statistics":   estadisticas(m.netin, m.netout),
		},
	}})
}

// direcciones arma ip-addresses. El guest agent usa "ipv4"/"ipv6" como tipo;
// /interfaces de lxc usa "inet"/"inet6".
func direcciones(ip4 string, prefijo4 int, ip6 string, prefijo6 int, estilo string) []map[string]any {
	tipo4, tipo6 := "ipv4", "ipv6"
	if estilo == "inet" {
		tipo4, tipo6 = "inet", "inet6"
	}
	return []map[string]any{
		{"ip-address": ip4, "ip-address-type": tipo4, "prefix": prefijo4},
		{"ip-address": ip6, "ip-address-type": tipo6, "prefix": prefijo6},
	}
}

func estadisticas(rx, tx int64) map[string]any {
	return map[string]any{
		"rx-bytes": rx, "rx-packets": rx / 600, "rx-errs": 0, "rx-dropped": 0,
		"tx-bytes": tx, "tx-packets": tx / 600, "tx-errs": 0, "tx-dropped": 0,
	}
}
