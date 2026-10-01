package main

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// /lxc/{vmid}/interfaces está contrastado contra Proxmox VE 9.2.2 real ("prefix"
// como string, {"data": null} con el contenedor apagado). El guest agent de qemu
// sigue la especificación de QEMU (QAPI GuestIpAddress: "prefix" es un entero).

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

// interfacesLXC devuelve las interfaces del contenedor: lo y eth0, con los
// campos clásicos (hwaddr, inet, inet6) y la lista ip-addresses.
// Como en Proxmox VE 9.2.2 real:
//   - "prefix" va como string ("24"), a diferencia del guest agent de qemu;
//   - con el contenedor apagado NO es un error: 200 con {"data": null}.
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
		responder(w, nil) // {"data": null}
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

// direcciones arma ip-addresses. Los dos endpoints difieren, igual que en Proxmox:
//   - guest agent de qemu ("ipv4"): tipo "ipv4"/"ipv6" y prefix ENTERO (24);
//   - /interfaces de lxc ("inet"): tipo "inet"/"inet6" y prefix STRING ("24").
func direcciones(ip4 string, prefijo4 int, ip6 string, prefijo6 int, estilo string) []map[string]any {
	tipo4, tipo6 := "ipv4", "ipv6"
	var p4, p6 any = prefijo4, prefijo6
	if estilo == "inet" {
		tipo4, tipo6 = "inet", "inet6"
		p4, p6 = strconv.Itoa(prefijo4), strconv.Itoa(prefijo6)
	}
	return []map[string]any{
		{"ip-address": ip4, "ip-address-type": tipo4, "prefix": p4},
		{"ip-address": ip6, "ip-address-type": tipo6, "prefix": p6},
	}
}

func estadisticas(rx, tx int64) map[string]any {
	return map[string]any{
		"rx-bytes": rx, "rx-packets": rx / 600, "rx-errs": 0, "rx-dropped": 0,
		"tx-bytes": tx, "tx-packets": tx / 600, "tx-errs": 0, "tx-dropped": 0,
	}
}
