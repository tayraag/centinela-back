package services_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"
)

func itf(nombre string, dirs ...ports.DireccionIP) ports.InterfazRed {
	return ports.InterfazRed{Nombre: nombre, Direcciones: dirs}
}
func v4(ip string) ports.DireccionIP { return ports.DireccionIP{IP: ip, Prefijo: 24, Version: 4} }
func v6(ip string) ports.DireccionIP { return ports.DireccionIP{IP: ip, Prefijo: 64, Version: 6} }

func TestSeleccionarIP(t *testing.T) {
	lo := itf("lo", v4("127.0.0.1"), v6("::1"))
	casos := []struct {
		nombre     string
		interfaces []ports.InterfazRed
		esperada   string // "" = nil
	}{
		{"solo loopback", []ports.InterfazRed{lo}, ""},
		{"sin interfaces", nil, ""},
		{"IPv4 privada", []ports.InterfazRed{lo, itf("eth0", v6("fe80::1"), v4("192.168.1.110"))}, "192.168.1.110"},
		{"IPv4 pública", []ports.InterfazRed{itf("eth0", v4("200.45.10.3"))}, "200.45.10.3"},
		{"primera IPv4 entre varias interfaces", []ports.InterfazRed{lo, itf("eth0", v4("10.0.0.5")), itf("eth1", v4("172.16.0.9"))}, "10.0.0.5"},
		{"IPv4 gana sobre IPv6 global aunque venga después", []ports.InterfazRed{itf("eth0", v6("2001:db8::10")), itf("eth1", v4("10.0.0.5"))}, "10.0.0.5"},
		{"sin IPv4: IPv6 global", []ports.InterfazRed{lo, itf("eth0", v6("fe80::be24:11ff:fe5e:2210"), v6("2001:db8::10"))}, "2001:db8::10"},
		{"solo link-local IPv6", []ports.InterfazRed{lo, itf("eth0", v6("fe80::be24:11ff:fe5e:2210"))}, ""},
		{"IPv4 link-local 169.254 se ignora", []ports.InterfazRed{itf("eth0", v4("169.254.10.1"))}, ""},
		{"interfaz lo con IP no-loopback igual se ignora", []ports.InterfazRed{itf("lo", v4("10.9.9.9"))}, ""},
		{"IP mal formada se ignora", []ports.InterfazRed{itf("eth0", v4("no-es-ip"), v4("10.0.0.7"))}, "10.0.0.7"},
	}
	for _, c := range casos {
		got := services.SeleccionarIP(c.interfaces)
		if (got == nil && c.esperada != "") || (got != nil && *got != c.esperada) {
			t.Errorf("%s: se esperaba %q, vino %v", c.nombre, c.esperada, deref(got))
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// proxmoxIPs simula Proxmox para el inventario: cada vmid tiene sus interfaces,
// un error o una demora. Mide las consultas en paralelo.
type proxmoxIPs struct {
	proxmoxTareas
	instancias []ports.InstanciaProxmoxDTO
	interfaces map[int][]ports.InterfazRed
	errores    map[int]error
	demora     map[int]time.Duration
	consultas  sync.Map // vmid → true
	enVuelo    atomic.Int32
	maxVuelo   atomic.Int32
}

func (p *proxmoxIPs) ListarInstancias(context.Context) ([]ports.InstanciaProxmoxDTO, error) {
	return p.instancias, nil
}

func (p *proxmoxIPs) ObtenerInterfaces(ctx context.Context, _, _ string, vmid int) ([]ports.InterfazRed, error) {
	p.consultas.Store(vmid, true)
	actual := p.enVuelo.Add(1)
	defer p.enVuelo.Add(-1)
	for max := p.maxVuelo.Load(); actual > max && !p.maxVuelo.CompareAndSwap(max, actual); max = p.maxVuelo.Load() {
	}
	select {
	case <-time.After(p.demora[vmid]):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := p.errores[vmid]; err != nil {
		return nil, err
	}
	return p.interfaces[vmid], nil
}

func TestInventario_ConsolidaYEsTolerante(t *testing.T) {
	px := &proxmoxIPs{
		instancias: []ports.InstanciaProxmoxDTO{
			{Vmid: 110, Nombre: "Servidor-Prueba", Tipo: "qemu", Nodo: "proxmox", Estado: "running", Cpu: 0.02, Mem: 400, MaxMem: 2048, MaxCpu: 2},
			{Vmid: 101, Nombre: "back", Tipo: "lxc", Nodo: "proxmox", Estado: "running", Cpu: 0.01, Mem: 25, MaxMem: 1024, MaxCpu: 1},
			{Vmid: 9003, Nombre: "sin-agente", Tipo: "qemu", Nodo: "proxmox", Estado: "running"},
			{Vmid: 201, Nombre: "apagado", Tipo: "lxc", Nodo: "proxmox", Estado: "stopped"},
			{Vmid: 202, Nombre: "sin-red", Tipo: "lxc", Nodo: "proxmox", Estado: "running"},
		},
		interfaces: map[int][]ports.InterfazRed{
			110: {itf("lo", v4("127.0.0.1")), itf("eth0", v4("192.168.1.110"))},
			101: {itf("lo", v4("127.0.0.1")), itf("eth0", v4("192.168.1.101"))},
			202: {itf("lo", v4("127.0.0.1"))},
		},
		errores: map[int]error{9003: errors.Join(ports.ErrProxmoxNoDisponible, errors.New("QEMU guest agent is not running"))},
	}
	inventario, err := services.NewInventarioService(px).Listar(context.Background())
	if err != nil {
		t.Fatalf("Ninguna falla de red debe hacer fallar el listado: %v", err)
	}
	if len(inventario) != 5 {
		t.Fatalf("Se esperaban 5 instancias, vinieron %d", len(inventario))
	}
	porVmid := map[int]ports.InstanciaInventario{}
	for _, inst := range inventario {
		porVmid[inst.Vmid] = inst
	}
	esperadas := map[int]string{110: "192.168.1.110", 101: "192.168.1.101", 9003: "", 201: "", 202: ""}
	for vmid, ip := range esperadas {
		if got := porVmid[vmid].IP; (got == nil) != (ip == "") || (got != nil && *got != ip) {
			t.Errorf("IP de %d = %s, se esperaba %q", vmid, deref(got), ip)
		}
	}
	if m := porVmid[110]; m.Tipo != "qemu" || m.CPU != 0.02 || m.Mem != 400 || m.MaxMem != 2048 || m.MaxCPU != 2 || m.Nombre != "Servidor-Prueba" {
		t.Errorf("Métricas crudas de la 110: %+v", m)
	}
	if m := porVmid[101]; m.Tipo != "lxc" || m.MaxMem != 1024 {
		t.Errorf("Métricas crudas de la 101: %+v", m)
	}
	if _, consultada := px.consultas.Load(201); consultada {
		t.Error("Una instancia apagada no debe consultarse")
	}
}

func veinteInstancias(demora time.Duration) *proxmoxIPs {
	px := &proxmoxIPs{interfaces: map[int][]ports.InterfazRed{}, demora: map[int]time.Duration{}}
	for i := 0; i < 20; i++ {
		vmid := 300 + i
		px.instancias = append(px.instancias, ports.InstanciaProxmoxDTO{Vmid: vmid, Tipo: "qemu", Nodo: "proxmox", Estado: "running"})
		px.interfaces[vmid] = []ports.InterfazRed{itf("eth0", v4("10.0.1.1"))}
		px.demora[vmid] = demora
	}
	return px
}

// DoD: 20 instancias concurrentes no exceden 2,5 s globales.
func TestInventario_20InstanciasEnMenosDe2_5Segundos(t *testing.T) {
	for _, demora := range []time.Duration{300 * time.Millisecond, 1500 * time.Millisecond, 5 * time.Second} {
		px := veinteInstancias(demora)
		inicio := time.Now()
		ips := services.NewInventarioService(px).ResolverIPs(context.Background(), px.instancias)
		total := time.Since(inicio)
		if total > 2500*time.Millisecond {
			t.Errorf("Con %s por instancia la resolución tardó %s (máximo 2,5 s)", demora, total)
		}
		if max := px.maxVuelo.Load(); max > 16 {
			t.Errorf("Hubo %d consultas en paralelo (máximo 16)", max)
		}
		t.Logf("%s por instancia → %s total, %d IPs resueltas, máx %d en paralelo", demora, total.Round(time.Millisecond), len(ips), px.maxVuelo.Load())
		if demora <= 300*time.Millisecond && len(ips) != 20 {
			t.Errorf("Con respuestas rápidas deben resolverse las 20: %d", len(ips))
		}
		if demora >= 5*time.Second && len(ips) != 0 {
			t.Errorf("Si todas superan el timeout individual quedan en nil: %d", len(ips))
		}
	}
}

// Una instancia colgada no demora a las demás más allá de su timeout de 2 s.
func TestInventario_UnaInstanciaColgadaNoTrabaAlResto(t *testing.T) {
	px := veinteInstancias(50 * time.Millisecond)
	px.demora[305] = time.Minute
	inicio := time.Now()
	ips := services.NewInventarioService(px).ResolverIPs(context.Background(), px.instancias)
	if total := time.Since(inicio); total > 2200*time.Millisecond {
		t.Errorf("Tardó %s: la colgada debe cortarse a los 2 s", total)
	}
	if len(ips) != 19 || ips[305] != nil {
		t.Errorf("Deben resolverse las otras 19 y la colgada quedar en nil: %d resueltas", len(ips))
	}
}
