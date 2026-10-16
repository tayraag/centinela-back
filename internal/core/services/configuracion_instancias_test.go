package services_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"el-centinela/internal/adapters/secondary/memoria"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"
)

const mib = int64(1024 * 1024)

// proxmoxConfigFalso imita lo que importa de Proxmox para cambiar CPU y RAM:
// apagada o con hot-plug el cambio se aplica; encendida sin hot-plug queda
// pendiente (la config en uso no cambia), como en una VM QEMU.
type proxmoxConfigFalso struct {
	proxmoxTareas
	mu       sync.Mutex
	inst     ports.InstanciaProxmoxDTO
	enUso    ports.ConfigInstanciaDTO
	hotplug  bool
	puts     int
	digestPU string
}

func (p *proxmoxConfigFalso) ObtenerInstancia(context.Context, int) (*ports.InstanciaProxmoxDTO, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := p.inst
	return &i, nil
}

func (p *proxmoxConfigFalso) ObtenerConfig(_ context.Context, _, _ string, _ int, enUso bool) (*ports.ConfigInstanciaDTO, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.enUso
	c.Digest = "digest-actual"
	return &c, nil
}

func (p *proxmoxConfigFalso) ActualizarConfig(_ context.Context, _, _ string, _ int, cores, memoriaMB int, digest string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.puts++
	p.digestPU = digest
	if p.inst.Estado != "running" || p.hotplug {
		if cores > 0 {
			p.enUso.Cores = cores
		}
		if memoriaMB > 0 {
			p.enUso.MemoriaMB = memoriaMB
		}
	}
	return nil
}

// ampliadorFalso registra lo que se le pidió validar.
type ampliadorFalso struct {
	llamadas     int
	deltaCores   int
	deltaMemoria int64
	rechazo      error
}

func (a *ampliadorFalso) AmpliarInstancia(_ context.Context, _ string, deltaCores int, deltaMemoria int64, aplicar func() error) error {
	a.llamadas++
	a.deltaCores, a.deltaMemoria = deltaCores, deltaMemoria
	if a.rechazo != nil {
		return a.rechazo
	}
	return aplicar()
}

func vmConfig(estado string, cores, memoriaMB int) *proxmoxConfigFalso {
	return &proxmoxConfigFalso{
		inst:  ports.InstanciaProxmoxDTO{Vmid: 110, Nodo: "pve1", Tipo: "qemu", Estado: estado, MaxCpu: cores, MaxMem: int64(memoriaMB) * mib},
		enUso: ports.ConfigInstanciaDTO{Cores: cores, MemoriaMB: memoriaMB},
	}
}

func TestReconfigurar_ApagadaNoRequiereReinicio(t *testing.T) {
	px, cuotas := vmConfig("stopped", 2, 2048), &ampliadorFalso{}
	res, err := services.NewConfiguracionInstancias(px, cuotas).Reconfigurar(context.Background(), 110, 4, 4096)
	if err != nil || res.RequiresReboot {
		t.Fatalf("Apagada: requiresReboot debe ser false: %+v %v", res, err)
	}
	if px.puts != 1 || px.enUso.Cores != 4 || px.enUso.MemoriaMB != 4096 || px.digestPU != "digest-actual" {
		t.Errorf("Debe aplicar el cambio con el digest leído: puts=%d enUso=%+v digest=%q", px.puts, px.enUso, px.digestPU)
	}
	if res.Anterior.Cores != 2 || res.Anterior.MemoriaMB != 2048 {
		t.Errorf("Debe informar la config anterior: %+v", res.Anterior)
	}
}

func TestReconfigurar_EncendidaSinHotplugRequiereReinicio(t *testing.T) {
	px := vmConfig("running", 2, 2048)
	res, err := services.NewConfiguracionInstancias(px, &ampliadorFalso{}).Reconfigurar(context.Background(), 110, 0, 4096)
	if err != nil || !res.RequiresReboot {
		t.Fatalf("Encendida y con el cambio pendiente: requiresReboot debe ser true: %+v %v", res, err)
	}
}

func TestReconfigurar_EncendidaConHotplugNoRequiereReinicio(t *testing.T) {
	px := vmConfig("running", 2, 2048)
	px.hotplug = true
	res, err := services.NewConfiguracionInstancias(px, &ampliadorFalso{}).Reconfigurar(context.Background(), 110, 4, 4096)
	if err != nil || res.RequiresReboot {
		t.Fatalf("Aplicado en caliente: requiresReboot debe ser false: %+v %v", res, err)
	}
}

// DeltaRAM: solo el incremento se valida contra el margen del nodo.
func TestReconfigurar_ValidaSoloElIncremento(t *testing.T) {
	px, cuotas := vmConfig("stopped", 2, 2048), &ampliadorFalso{}
	if _, err := services.NewConfiguracionInstancias(px, cuotas).Reconfigurar(context.Background(), 110, 0, 6144); err != nil {
		t.Fatal(err)
	}
	if cuotas.llamadas != 1 || cuotas.deltaMemoria != 4096*mib || cuotas.deltaCores != 0 {
		t.Errorf("Debe validar DeltaRAM = 6144 - 2048 MiB y 0 vCPU: llamadas=%d deltaRAM=%d deltaCores=%d", cuotas.llamadas, cuotas.deltaMemoria/mib, cuotas.deltaCores)
	}

	// Bajar recursos no necesita validación.
	px, cuotas = vmConfig("stopped", 4, 4096), &ampliadorFalso{}
	if _, err := services.NewConfiguracionInstancias(px, cuotas).Reconfigurar(context.Background(), 110, 2, 1024); err != nil || cuotas.llamadas != 0 || px.puts != 1 {
		t.Errorf("Una reducción se aplica sin validar cuota: err=%v validaciones=%d puts=%d", err, cuotas.llamadas, px.puts)
	}
}

// Si no hay margen, no se escribe nada en Proxmox.
func TestReconfigurar_SinMargenNoEscribeEnProxmox(t *testing.T) {
	px := vmConfig("running", 2, 2048)
	cuotas := &ampliadorFalso{rechazo: ports.ErrRecursosInsuficientes}
	if _, err := services.NewConfiguracionInstancias(px, cuotas).Reconfigurar(context.Background(), 110, 2, 65536); !errors.Is(err, ports.ErrRecursosInsuficientes) {
		t.Fatalf("Se esperaba ErrRecursosInsuficientes, vino %v", err)
	}
	if px.puts != 0 {
		t.Errorf("Sin margen no debe haber PUT /config en Proxmox: %d", px.puts)
	}
}

// --- AmpliarInstancia (CuotasService) ---
// Nodo de proxmoxAprovisionamiento: 8 vCPU y 8 GiB (1 GiB usado), con una
// instancia de 2 vCPU y 2 GiB. Margen de RAM: 8 - 2 - 15% de headroom = 4,8 GiB.

func TestAmpliarInstancia_DentroDelMargenAplica(t *testing.T) {
	svc := services.NewCuotasService(&proxmoxAprovisionamiento{}, nil, memoria.Nuevo())
	aplicado := false
	err := svc.AmpliarInstancia(context.Background(), "pve1", 2, 4096*mib, func() error { aplicado = true; return nil })
	if err != nil || !aplicado {
		t.Fatalf("4 GiB y 2 vCPU entran en el margen: err=%v aplicado=%v", err, aplicado)
	}
}

func TestAmpliarInstancia_FueraDelMargenNoAplica(t *testing.T) {
	casos := []struct {
		nombre  string
		cores   int
		memoria int64
	}{
		{"RAM (respeta el headroom del 15%)", 0, 5 * 1024 * mib},
		{"vCPU", 7, 0},
	}
	for _, c := range casos {
		svc := services.NewCuotasService(&proxmoxAprovisionamiento{}, nil, memoria.Nuevo())
		aplicado := false
		err := svc.AmpliarInstancia(context.Background(), "pve1", c.cores, c.memoria, func() error { aplicado = true; return nil })
		if !errors.Is(err, ports.ErrRecursosInsuficientes) || aplicado {
			t.Errorf("%s: se esperaba ErrRecursosInsuficientes sin aplicar, vino %v (aplicado=%v)", c.nombre, err, aplicado)
		}
	}
}

// capacidadMutable refleja en el inventario lo que se va aplicando (como
// Proxmox con la instancia apagada).
type capacidadMutable struct {
	proxmoxAprovisionamiento
	mu       sync.Mutex
	maxMemGB int64
}

func (c *capacidadMutable) ListarInstancias(context.Context) ([]ports.InstanciaProxmoxDTO, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return []ports.InstanciaProxmoxDTO{{Nodo: "pve1", MaxCpu: 2, MaxMem: c.maxMemGB * 1024 * mib}}, nil
}

// Dos ampliaciones simultáneas de 3 GiB (cabe una sola en 4,8 GiB): el lock de
// capacidad las serializa y la segunda ve la capacidad ya consumida.
func TestAmpliarInstancia_ConcurrentesNoSobrecargan(t *testing.T) {
	px := &capacidadMutable{maxMemGB: 2}
	svc := services.NewCuotasService(px, nil, memoria.Nuevo())
	var aplicadas, rechazadas atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := svc.AmpliarInstancia(context.Background(), "pve1", 0, 3*1024*mib, func() error {
				px.mu.Lock()
				px.maxMemGB += 3
				px.mu.Unlock()
				return nil
			})
			switch {
			case err == nil:
				aplicadas.Add(1)
			case errors.Is(err, ports.ErrRecursosInsuficientes):
				rechazadas.Add(1)
			default:
				t.Errorf("error inesperado: %v", err)
			}
		}()
	}
	wg.Wait()
	if aplicadas.Load() != 1 || rechazadas.Load() != 1 {
		t.Errorf("Se esperaba 1 aplicada y 1 rechazada, vinieron %d y %d", aplicadas.Load(), rechazadas.Load())
	}
}
