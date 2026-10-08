package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/memoria"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"
)

// proxmoxNodo simula Proxmox para el estado del nodo: cuenta las consultas y
// puede fallar o demorar.
type proxmoxNodo struct {
	proxmoxTareas
	llamadas atomic.Int32
	mu       sync.Mutex
	err      error
	demora   time.Duration
	cpu      float64
}

func (p *proxmoxNodo) ObtenerEstadoNodo(_ context.Context, nodo string) (*ports.NodeStatusDTO, error) {
	p.llamadas.Add(1)
	p.mu.Lock()
	err, demora, cpu := p.err, p.demora, p.cpu
	p.mu.Unlock()
	time.Sleep(demora)
	if err != nil {
		return nil, err
	}
	// Valores de la captura real de GET /nodes/pve/status (RF-02).
	return &ports.NodeStatusDTO{
		CPU: cpu, CPUs: 12,
		MemTotal: 16423432192, MemUsada: 2721751040,
		DiscoTotal: 72722055168, DiscoUsado: 5477756928,
		UptimeSegs: 199252,
	}, nil
}

func (p *proxmoxNodo) ListarInstancias(context.Context) ([]ports.InstanciaProxmoxDTO, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	return []ports.InstanciaProxmoxDTO{
		{Vmid: 100, Tipo: "qemu", Estado: "running"}, {Vmid: 110, Tipo: "qemu", Estado: "stopped"},
		{Vmid: 9003, Tipo: "qemu", Estado: "paused"}, {Vmid: 101, Tipo: "lxc", Estado: "running"},
		{Vmid: 201, Tipo: "lxc", Estado: "stopped"},
	}, nil
}

func (p *proxmoxNodo) fallar(err error) { p.mu.Lock(); p.err = err; p.mu.Unlock() }

type entornoNodo struct {
	px      *proxmoxNodo
	kv      *memoria.KVStore
	svc     ports.NodoService
	avanzar func(time.Duration)
}

func nuevoEntornoNodo() *entornoNodo {
	var mu sync.Mutex
	reloj := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	ahora := func() time.Time { mu.Lock(); defer mu.Unlock(); return reloj }
	kv := memoria.NuevoConReloj(ahora)
	px := &proxmoxNodo{cpu: 0.0377}
	return &entornoNodo{
		px: px, kv: kv, svc: services.NewNodoServiceConReloj(px, kv, "proxmox", ahora),
		avanzar: func(d time.Duration) { mu.Lock(); reloj = reloj.Add(d); mu.Unlock() },
	}
}

func TestNodo_NormalizaComoPideElContrato(t *testing.T) {
	e := nuevoEntornoNodo()
	estado, stale, err := e.svc.ObtenerEstado(context.Background())
	if err != nil || stale {
		t.Fatalf("Primera lectura: %v stale=%v", err, stale)
	}
	esperado := ports.EstadoNodo{
		CPU:           ports.MetricaCPU{UsagePercent: 3.77, Cores: 12},
		RAM:           ports.MetricaCapacidad{UsedGb: 2.53, TotalGb: 15.3, UsagePercent: 16.57},
		Storage:       ports.MetricaCapacidad{UsedGb: 5.1, TotalGb: 67.73, UsagePercent: 7.53},
		UptimeSeconds: 199252,
		InstancesSummary: ports.ResumenInstancias{
			VMs: ports.ResumenEstado{Running: 1, Stopped: 1, Paused: 1, Total: 3},
			LXC: ports.ResumenEstado{Running: 1, Stopped: 1, Paused: 0, Total: 2},
		},
		FetchedAt: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
	}
	if *estado != esperado {
		t.Errorf("Normalización:\n got  %+v\n want %+v", *estado, esperado)
	}
}

func TestNodo_CacheVigenteNoConsultaProxmox(t *testing.T) {
	e := nuevoEntornoNodo()
	ctx := context.Background()
	_, _, _ = e.svc.ObtenerEstado(ctx)
	for i := 0; i < 5; i++ {
		e.avanzar(time.Second)
		if _, stale, err := e.svc.ObtenerEstado(ctx); err != nil || stale {
			t.Fatalf("Con la caché vigente: %v stale=%v", err, stale)
		}
	}
	if n := e.px.llamadas.Load(); n != 1 {
		t.Errorf("Con node:status:current vigente no se consulta Proxmox: hubo %d consultas", n)
	}
}

func TestNodo_AlVencerElTTLRefrescaAmbasClaves(t *testing.T) {
	e := nuevoEntornoNodo()
	ctx := context.Background()
	_, _, _ = e.svc.ObtenerEstado(ctx)

	e.avanzar(10 * time.Second) // vence node:status:current
	if _, err := e.kv.Get(ctx, "node:status:current"); !errors.Is(err, ports.ErrClaveNoEncontrada) {
		t.Fatal("A los 10 s node:status:current debe haber vencido")
	}
	e.px.mu.Lock()
	e.px.cpu = 0.5
	e.px.mu.Unlock()
	estado, stale, err := e.svc.ObtenerEstado(ctx)
	if err != nil || stale || estado.CPU.UsagePercent != 50 || e.px.llamadas.Load() != 2 {
		t.Fatalf("Al vencer debe traer datos frescos: %+v stale=%v err=%v consultas=%d", estado.CPU, stale, err, e.px.llamadas.Load())
	}
	for _, clave := range []string{"node:status:current", "node:status:last_known"} {
		valor, err := e.kv.Get(ctx, clave)
		var guardado ports.EstadoNodo
		if err != nil || json.Unmarshal([]byte(valor), &guardado) != nil || guardado.CPU.UsagePercent != 50 {
			t.Errorf("%s debe quedar actualizada: %s %v", clave, valor, err)
		}
	}
	// last_known no vence nunca.
	e.avanzar(365 * 24 * time.Hour)
	if _, err := e.kv.Get(ctx, "node:status:last_known"); err != nil {
		t.Errorf("node:status:last_known no debe tener TTL: %v", err)
	}
}

func TestNodo_ProxmoxCaidoDevuelveUltimoConocidoStale(t *testing.T) {
	e := nuevoEntornoNodo()
	ctx := context.Background()
	primero, _, _ := e.svc.ObtenerEstado(ctx)

	e.avanzar(time.Minute)
	e.px.fallar(ports.ErrProxmoxNoDisponible)
	estado, stale, err := e.svc.ObtenerEstado(ctx)
	if err != nil || !stale {
		t.Fatalf("Con Proxmox caído y lectura previa: 200 stale=true, vino err=%v stale=%v", err, stale)
	}
	if !estado.FetchedAt.Equal(primero.FetchedAt) {
		t.Errorf("fetchedAt debe ser el de la lectura original (%v), vino %v", primero.FetchedAt, estado.FetchedAt)
	}
}

func TestNodo_SinLecturaPreviaDevuelveElError(t *testing.T) {
	for _, errProxmox := range []error{
		errors.Join(ports.ErrProxmoxNoDisponible, ports.ErrProxmoxTimeout),
		ports.ErrProxmoxNoDisponible,
	} {
		e := nuevoEntornoNodo()
		e.px.fallar(errProxmox)
		_, _, err := e.svc.ObtenerEstado(context.Background())
		if !errors.Is(err, ports.ErrProxmoxNoDisponible) {
			t.Fatalf("Sin lectura previa debe devolver el error de Proxmox, vino %v", err)
		}
		if errors.Is(errProxmox, ports.ErrProxmoxTimeout) != errors.Is(err, ports.ErrProxmoxTimeout) {
			t.Errorf("Debe conservar si fue timeout (504) o no (502): %v", err)
		}
	}
}

func TestNodo_PausaTrasFallaNoReintentaEnCadaRequest(t *testing.T) {
	e := nuevoEntornoNodo()
	ctx := context.Background()
	_, _, _ = e.svc.ObtenerEstado(ctx)
	e.avanzar(time.Minute)
	e.px.fallar(ports.ErrProxmoxTimeout)

	_, _, _ = e.svc.ObtenerEstado(ctx) // falla y registra
	for i := 0; i < 3; i++ {
		e.avanzar(time.Second)
		if _, stale, _ := e.svc.ObtenerEstado(ctx); !stale {
			t.Fatal("Durante la pausa debe responder stale")
		}
	}
	if n := e.px.llamadas.Load(); n != 2 {
		t.Errorf("Durante los 5 s posteriores a una falla no se reintenta Proxmox: hubo %d consultas", n)
	}
	e.avanzar(5 * time.Second)
	e.px.fallar(nil)
	if _, stale, err := e.svc.ObtenerEstado(ctx); err != nil || stale {
		t.Errorf("Pasada la pausa y con Proxmox de vuelta, debe traer datos frescos: %v stale=%v", err, stale)
	}
}

func TestNodo_PedidosSimultaneosCompartenUnaConsulta(t *testing.T) {
	e := nuevoEntornoNodo()
	e.px.demora = 100 * time.Millisecond
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := e.svc.ObtenerEstado(context.Background()); err != nil {
				t.Errorf("Pedido simultáneo: %v", err)
			}
		}()
	}
	wg.Wait()
	if n := e.px.llamadas.Load(); n != 1 {
		t.Errorf("20 pedidos simultáneos con la caché vacía deben hacer 1 sola consulta a Proxmox, hubo %d", n)
	}
}

func TestNodo_ArranqueEnFrioNoBloqueaRecuperacionInmediata(t *testing.T) {
	e := nuevoEntornoNodo()
	ctx := context.Background()

	// Proxmox falla en la primera petición (arranque en frío, no hay caché)
	e.px.fallar(ports.ErrProxmoxNoDisponible)
	_, _, err := e.svc.ObtenerEstado(ctx)
	if err == nil {
		t.Fatal("Se esperaba error por arranque en frío")
	}

	// Proxmox se recupera inmediatamente
	e.px.fallar(nil)

	// Avanzamos solo 1 segundo (estamos dentro de la ventana de pausaTrasFallaNodo de 5s)
	e.avanzar(time.Second)

	// Al solicitar el estado, como NO hay caché previa, el circuit breaker
	// DEBE permitir la llamada a Proxmox (half-open inmediato) en lugar de devolver
	// 502 ciegamente.
	estado, stale, err := e.svc.ObtenerEstado(ctx)
	if err != nil {
		t.Fatalf("No debió bloquearse la petición por cooldown; err: %v", err)
	}
	if stale {
		t.Errorf("No debe devolver stale, sino datos frescos de la recuperación")
	}
	if estado == nil {
		t.Fatal("El estado no debería ser nil")
	}

	if e.px.llamadas.Load() != 2 {
		t.Errorf("Se esperaba que intente llamar a Proxmox (2 llamadas en total), hizo %d", e.px.llamadas.Load())
	}
}
