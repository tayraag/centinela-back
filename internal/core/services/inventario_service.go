package services

import (
	"context"
	"net"
	"sync"
	"time"

	"el-centinela/internal/core/ports"
)

// Límites de la resolución de IPs:
//   - ipWorkers: consultas simultáneas a Proxmox (guest agent / interfaces).
//   - timeoutIPInstancia: cada instancia tiene como máximo este tiempo.
//   - presupuestoIPs: toda la fase de IPs tiene como máximo este tiempo. Con
//     concurrencia acotada, si muchas instancias tardaran el máximo, una
//     segunda tanda superaría el límite global: lo que no se resuelva antes
//     queda con ip = nil.
const (
	ipWorkers          = 16
	timeoutIPInstancia = 2 * time.Second
	presupuestoIPs     = 2200 * time.Millisecond
)

// inventarioService implementa ports.InventarioService.
type inventarioService struct {
	proxmox ports.ProxmoxPort
	workers int
	timeout time.Duration
	global  time.Duration
}

// NewInventarioService crea el servicio de inventario consolidado.
func NewInventarioService(proxmox ports.ProxmoxPort) ports.InventarioService {
	return &inventarioService{proxmox: proxmox, workers: ipWorkers, timeout: timeoutIPInstancia, global: presupuestoIPs}
}

// NewInventarioServiceConLimites permite ajustar los límites (tests).
func NewInventarioServiceConLimites(proxmox ports.ProxmoxPort, workers int, timeout, global time.Duration) ports.InventarioService {
	return &inventarioService{proxmox: proxmox, workers: workers, timeout: timeout, global: global}
}

// Listar consolida VMs y contenedores de cluster/resources con sus métricas
// crudas y resuelve sus IPs.
func (s *inventarioService) Listar(ctx context.Context) ([]ports.InstanciaInventario, error) {
	instancias, err := s.proxmox.ListarInstancias(ctx)
	if err != nil {
		return nil, err
	}
	ips := s.ResolverIPs(ctx, instancias)
	resultado := make([]ports.InstanciaInventario, 0, len(instancias))
	for _, inst := range instancias {
		resultado = append(resultado, ports.InstanciaInventario{
			Vmid: inst.Vmid, Nombre: inst.Nombre, Tipo: inst.Tipo, Nodo: inst.Nodo, Estado: inst.Estado,
			CPU: inst.Cpu, Mem: inst.Mem, MaxMem: inst.MaxMem, MaxCPU: inst.MaxCpu,
			IP: ips[inst.Vmid],
		})
	}
	return resultado, nil
}

// ResolverIPs consulta en paralelo, con s.workers consultas simultáneas como
// máximo, la red de cada instancia encendida. Las apagadas no se consultan.
// Cualquier falla (sin agente, sin red, timeout, Proxmox caído) deja ip = nil.
func (s *inventarioService) ResolverIPs(ctx context.Context, instancias []ports.InstanciaProxmoxDTO) map[int]*string {
	ips := make(map[int]*string, len(instancias))
	pendientes := make(chan ports.InstanciaProxmoxDTO)
	ctxGlobal, cancelar := context.WithTimeout(ctx, s.global)
	defer cancelar()

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for i := 0; i < s.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for inst := range pendientes {
				if ctxGlobal.Err() != nil {
					continue // se acabó el presupuesto: queda nil
				}
				ctxInstancia, cancelarInstancia := context.WithTimeout(ctxGlobal, s.timeout)
				interfaces, err := s.proxmox.ObtenerInterfaces(ctxInstancia, inst.Nodo, inst.Tipo, inst.Vmid)
				cancelarInstancia()
				if err != nil {
					continue
				}
				if ip := SeleccionarIP(interfaces); ip != nil {
					mu.Lock()
					ips[inst.Vmid] = ip
					mu.Unlock()
				}
			}
		}()
	}
	for _, inst := range instancias {
		if inst.Estado == "running" {
			pendientes <- inst
		}
	}
	close(pendientes)
	wg.Wait()
	return ips
}

// SeleccionarIP elige la IP a mostrar de una instancia, ignorando loopback:
//  1. la primera IPv4 (privada o pública; no link-local 169.254.0.0/16);
//  2. si no hay IPv4, la primera IPv6 de alcance global (no fe80::/10).
//
// Devuelve nil si no hay ninguna.
func SeleccionarIP(interfaces []ports.InterfazRed) *string {
	var ipv6 *string
	for _, itf := range interfaces {
		if itf.Nombre == "lo" {
			continue
		}
		for _, d := range itf.Direcciones {
			ip := net.ParseIP(d.IP)
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
				continue
			}
			if ip.To4() != nil {
				texto := ip.String()
				return &texto
			}
			if ipv6 == nil && ip.IsGlobalUnicast() {
				texto := ip.String()
				ipv6 = &texto
			}
		}
	}
	return ipv6
}
