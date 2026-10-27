package services_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/memoria"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"
)

// nodoAlertas devuelve el host con los porcentajes que el test fija en cada ciclo.
type nodoAlertas struct {
	mu             sync.Mutex
	ram, cpu, disk float64
	stale          bool
}

func (n *nodoAlertas) ObtenerEstado(context.Context) (*ports.EstadoNodo, bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return &ports.EstadoNodo{
		RAM: ports.MetricaCapacidad{UsagePercent: n.ram}, CPU: ports.MetricaCPU{UsagePercent: n.cpu},
		Storage: ports.MetricaCapacidad{UsagePercent: n.disk},
	}, n.stale, nil
}

func (n *nodoAlertas) ObtenerHistorial(context.Context) ([]ports.PuntoHistorialNodo, error) {
	return nil, nil
}

func (n *nodoAlertas) fijar(ram, cpu, disk float64) {
	n.mu.Lock()
	n.ram, n.cpu, n.disk = ram, cpu, disk
	n.mu.Unlock()
}

// inventarioAlertas es el inventario que el test cambia entre ciclos.
type inventarioAlertas struct {
	proxmoxTareas
	mu         sync.Mutex
	instancias []ports.InstanciaProxmoxDTO
	err        error
}

func (p *inventarioAlertas) ListarInstancias(context.Context) ([]ports.InstanciaProxmoxDTO, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ports.InstanciaProxmoxDTO(nil), p.instancias...), p.err
}

// tareasAlertas indica qué instancias tienen una tarea de Centinela reciente.
type tareasAlertas struct {
	ports.TareaRepository
	conTarea map[int]bool
}

func (t *tareasAlertas) HuboTareaDesde(_ context.Context, vmid int, _ time.Time) (bool, error) {
	return t.conTarea[vmid], nil
}

// busAlertas registra los eventos publicados.
type busAlertas struct {
	ports.EventosService
	mu      sync.Mutex
	eventos []ports.RealtimeEvent
}

func (b *busAlertas) Publicar(_ context.Context, ev ports.RealtimeEvent) error {
	b.mu.Lock()
	b.eventos = append(b.eventos, ev)
	b.mu.Unlock()
	return nil
}
func (b *busAlertas) tomar() []ports.RealtimeEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	eventos := b.eventos
	b.eventos = nil
	return eventos
}

// auditoriaAlertas cuenta lo registrado (es seguro para uso concurrente).
type auditoriaAlertas struct {
	ports.AuditService
	mu        sync.Mutex
	registros []ports.RegistrarAuditoriaInput
}

func (a *auditoriaAlertas) Registrar(_ context.Context, in ports.RegistrarAuditoriaInput) {
	a.mu.Lock()
	a.registros = append(a.registros, in)
	a.mu.Unlock()
}

type entornoAlertas struct {
	nodo   *nodoAlertas
	inv    *inventarioAlertas
	tareas *tareasAlertas
	kv     *memoria.KVStore
	bus    *busAlertas
	audit  *auditoriaAlertas
	eval   *services.EvaluadorAlertas
}

func nuevoEntornoAlertas() *entornoAlertas {
	e := &entornoAlertas{nodo: &nodoAlertas{}, inv: &inventarioAlertas{}, tareas: &tareasAlertas{conTarea: map[int]bool{}},
		kv: memoria.Nuevo(), bus: &busAlertas{}, audit: &auditoriaAlertas{}}
	e.eval = services.NewEvaluadorAlertas(e.nodo, e.inv, e.tareas, e.kv, e.bus, e.audit, "pve", 30*time.Second)
	return e
}

// ciclo fija la RAM del host (CPU y disco quedan normales) y evalúa una vez.
func (e *entornoAlertas) cicloRAM(ram float64) []ports.RealtimeEvent {
	e.nodo.fijar(ram, 10, 10)
	e.eval.Evaluar(context.Background())
	return e.bus.tomar()
}

func (e *entornoAlertas) existe(clave string) bool {
	_, err := e.kv.Get(context.Background(), clave)
	return err == nil
}

// DoD: un pico de un solo ciclo suma al contador pero no emite nada ni se audita,
// y el contador se purga al ciclo siguiente.
func TestAlertas_PicoDeUnCicloNoAlerta(t *testing.T) {
	e := nuevoEntornoAlertas()
	if ev := e.cicloRAM(92); len(ev) != 0 {
		t.Fatalf("Un solo ciclo sobre el umbral no debe alertar: %+v", ev)
	}
	if !e.existe("alert:pending:host:pve:ram") {
		t.Fatal("El primer ciclo debe dejar el contador de persistencia")
	}
	if ev := e.cicloRAM(70); len(ev) != 0 || e.existe("alert:pending:host:pve:ram") {
		t.Fatalf("Al bajar se purga el contador sin emitir: %+v", ev)
	}
	// Dos picos no consecutivos tampoco alertan.
	e.cicloRAM(92)
	e.cicloRAM(83)
	if ev := e.cicloRAM(92); len(ev) != 0 {
		t.Fatalf("Picos no consecutivos no deben alertar: %+v", ev)
	}
	if len(e.audit.registros) != 0 {
		t.Errorf("No debe auditarse nada: %+v", e.audit.registros)
	}
}

// DoD: dos ciclos seguidos generan exactamente un ALERT_TRIGGERED CRITICAL
// con su fila de auditoría, y no se repite mientras siga saturado.
func TestAlertas_SaturacionSostenidaAlertaUnaVez(t *testing.T) {
	e := nuevoEntornoAlertas()
	e.cicloRAM(90)
	ev := e.cicloRAM(91)
	if len(ev) != 1 {
		t.Fatalf("Se esperaba exactamente un evento, vinieron %d: %+v", len(ev), ev)
	}
	alerta := ev[0]
	if alerta.Tipo != ports.EventoAlerta || alerta.Severidad != ports.SeveridadCritical || alerta.RecursoTipo != ports.RecursoHost || alerta.RecursoID != "" {
		t.Errorf("Evento inesperado: %+v", alerta)
	}
	if alerta.Detalles["type"] != services.TipoAlertaHost || alerta.Detalles["metrica"] != "ram" || alerta.Detalles["valor"] != 91.0 ||
		alerta.Detalles["umbral"] != 85.0 || alerta.Detalles["acknowledged"] != false || alerta.Detalles["alertId"] == "" {
		t.Errorf("Detalles inesperados: %+v", alerta.Detalles)
	}
	if len(e.audit.registros) != 1 || e.audit.registros[0].Accion != "ALERT_TRIGGERED" || e.audit.registros[0].Resultado != ports.ResultadoSuccess ||
		e.audit.registros[0].Detalles["alertId"] != alerta.Detalles["alertId"] {
		t.Errorf("Debe auditarse una vez como ALERT_TRIGGERED/SUCCESS con el mismo alertId: %+v", e.audit.registros)
	}
	for i := 0; i < 5; i++ {
		if ev := e.cicloRAM(95); len(ev) != 0 {
			t.Fatalf("Ciclo %d: la alerta activa no debe repetirse: %+v", i, ev)
		}
	}
	if len(e.audit.registros) != 1 {
		t.Errorf("La auditoría tampoco se repite: %d", len(e.audit.registros))
	}
}

// DoD: oscilar entre 84% y 86% después de disparada no repite la alerta ni la
// resuelve; recién por debajo de 80% se normaliza y se limpian las claves.
func TestAlertas_HisteresisYNormalizacion(t *testing.T) {
	e := nuevoEntornoAlertas()
	e.cicloRAM(86)
	disparo := e.cicloRAM(86)
	if len(disparo) != 1 {
		t.Fatalf("Se esperaba la alerta: %+v", disparo)
	}
	for i, v := range []float64{84, 86, 84, 86, 85, 80, 86, 84} {
		if ev := e.cicloRAM(v); len(ev) != 0 {
			t.Fatalf("Oscilando (ciclo %d, %.0f%%) no debe haber eventos: %+v", i, v, ev)
		}
	}
	ev := e.cicloRAM(79.9)
	if len(ev) != 1 || ev[0].Tipo != ports.EventoAlertaResuelta || ev[0].Severidad != ports.SeveridadInfo ||
		ev[0].Detalles["alertId"] != disparo[0].Detalles["alertId"] || ev[0].Detalles["umbral"] != 80.0 {
		t.Fatalf("Por debajo de 80%% se esperaba ALERT_RESOLVED INFO con el mismo alertId: %+v", ev)
	}
	if e.existe("alert:active:host:pve:ram") || e.existe("alert:pending:host:pve:ram") {
		t.Error("Al normalizarse se limpian las claves")
	}
	if ev := e.cicloRAM(50); len(ev) != 0 {
		t.Errorf("Ya resuelta no vuelve a resolverse: %+v", ev)
	}
	// Una nueva saturación vuelve a necesitar dos ciclos.
	if ev := e.cicloRAM(90); len(ev) != 0 {
		t.Fatalf("Primer ciclo de la nueva racha: %+v", ev)
	}
	if ev := e.cicloRAM(90); len(ev) != 1 || ev[0].Detalles["alertId"] == disparo[0].Detalles["alertId"] {
		t.Errorf("La nueva alerta lleva otro alertId: %+v", ev)
	}
}

func TestAlertas_UmbralesDelHost(t *testing.T) {
	casos := []struct {
		ram, cpu, disk float64
		metrica        string
	}{
		{85, 10, 10, "ram"}, {10, 90, 10, "cpu"}, {10, 10, 90, "disk"},
	}
	for _, c := range casos {
		e := nuevoEntornoAlertas()
		e.nodo.fijar(c.ram, c.cpu, c.disk)
		e.eval.Evaluar(context.Background())
		e.eval.Evaluar(context.Background())
		ev := e.bus.tomar()
		if len(ev) != 1 || ev[0].Detalles["metrica"] != c.metrica {
			t.Errorf("%s en su umbral (≥) debe alertar: %+v", c.metrica, ev)
		}
	}
	e := nuevoEntornoAlertas()
	e.nodo.fijar(84.9, 89.9, 89.9)
	e.eval.Evaluar(context.Background())
	e.eval.Evaluar(context.Background())
	if ev := e.bus.tomar(); len(ev) != 0 {
		t.Errorf("Por debajo de los umbrales no alerta: %+v", ev)
	}
}

// Una lectura vieja del host (stale, Proxmox caído) no cuenta como ciclo.
func TestAlertas_LecturaViejaNoCuenta(t *testing.T) {
	e := nuevoEntornoAlertas()
	e.nodo.stale = true
	e.cicloRAM(95)
	e.cicloRAM(95)
	if e.existe("alert:pending:host:pve:ram") || len(e.audit.registros) != 0 {
		t.Error("Con datos stale no se evalúa el host")
	}
}

// DoD: deduplicación concurrente. Con la saturación ya confirmada, muchas
// evaluaciones simultáneas emiten exactamente una alerta.
func TestAlertas_DeduplicacionConcurrente(t *testing.T) {
	e := nuevoEntornoAlertas()
	e.nodo.fijar(95, 10, 10)
	if _, err := e.kv.Incrementar(context.Background(), "alert:pending:host:pve:ram", time.Minute); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.eval.Evaluar(context.Background())
		}()
	}
	wg.Wait()
	if ev := e.bus.tomar(); len(ev) != 1 {
		t.Errorf("Se esperaba un solo evento, vinieron %d", len(ev))
	}
	if len(e.audit.registros) != 1 {
		t.Errorf("Se esperaba una sola fila de auditoría, hay %d", len(e.audit.registros))
	}
}

func TestAlertas_SaturacionDeInstancias(t *testing.T) {
	e := nuevoEntornoAlertas()
	e.nodo.fijar(10, 10, 10)
	e.inv.instancias = []ports.InstanciaProxmoxDTO{
		{Vmid: 100, Nombre: "srv-database", Tipo: "qemu", Estado: "running", Cpu: 0.97, Mem: 1 << 30, MaxMem: 4 << 30},
		{Vmid: 201, Nombre: "ct-web", Tipo: "lxc", Estado: "running", Cpu: 0.10, Mem: 975, MaxMem: 1000},
		{Vmid: 110, Nombre: "apagada", Tipo: "qemu", Estado: "stopped", Cpu: 0.99, Mem: 100, MaxMem: 100},
	}
	e.eval.Evaluar(context.Background())
	if ev := e.bus.tomar(); len(ev) != 0 {
		t.Fatalf("Primer ciclo: %+v", ev)
	}
	e.eval.Evaluar(context.Background())
	ev := e.bus.tomar()
	if len(ev) != 2 {
		t.Fatalf("Se esperaban 2 alertas (CPU de la 100 y memoria de la 201), vinieron %+v", ev)
	}
	porRecurso := map[string]ports.RealtimeEvent{}
	for _, x := range ev {
		porRecurso[x.RecursoID] = x
	}
	cpu, mem := porRecurso["100"], porRecurso["201"]
	if cpu.RecursoTipo != ports.RecursoVM || cpu.Detalles["metrica"] != "cpu" || cpu.Detalles["type"] != services.TipoAlertaInstancia ||
		cpu.Mensaje != "La instancia srv-database superó el 95% de uso de CPU (97.0%)." {
		t.Errorf("CPU de la 100: %+v", cpu)
	}
	if mem.RecursoTipo != ports.RecursoLXC || mem.Detalles["metrica"] != "mem" || mem.Detalles["valor"] != 97.5 {
		t.Errorf("Memoria de la 201: %+v", mem)
	}
	if len(e.audit.registros) != 2 || e.audit.registros[0].InstanciaID == "" {
		t.Errorf("Cada alerta de instancia se audita con su instancia: %+v", e.audit.registros)
	}
}

func TestAlertas_CaidaNoComandada(t *testing.T) {
	e := nuevoEntornoAlertas()
	e.nodo.fijar(10, 10, 10)
	encendidas := []ports.InstanciaProxmoxDTO{
		{Vmid: 100, Nombre: "srv-database", Tipo: "qemu", Estado: "running", MaxMem: 100},
		{Vmid: 110, Nombre: "srv-app", Tipo: "qemu", Estado: "running", MaxMem: 100},
		{Vmid: 201, Nombre: "ct-web", Tipo: "lxc", Estado: "running", MaxMem: 100},
	}
	e.inv.instancias = encendidas
	e.eval.Evaluar(context.Background()) // primer ciclo: solo recuerda
	if ev := e.bus.tomar(); len(ev) != 0 {
		t.Fatalf("El primer ciclo no tiene contra qué comparar: %+v", ev)
	}

	// La 100 se apaga sola; la 110 la apagó Centinela; la 201 desaparece.
	e.tareas.conTarea[110] = true
	e.inv.instancias = []ports.InstanciaProxmoxDTO{
		{Vmid: 100, Nombre: "srv-database", Tipo: "qemu", Estado: "stopped", MaxMem: 100},
		{Vmid: 110, Nombre: "srv-app", Tipo: "qemu", Estado: "stopped", MaxMem: 100},
	}
	e.eval.Evaluar(context.Background())
	ev := e.bus.tomar()
	if len(ev) != 2 {
		t.Fatalf("Se esperaban 2 caídas (100 y 201), vinieron %+v", ev)
	}
	for _, x := range ev {
		if x.Tipo != ports.EventoAlerta || x.Severidad != ports.SeveridadCritical || x.Detalles["type"] != services.TipoAlertaCaida || x.RecursoID == "110" {
			t.Errorf("Caída inesperada: %+v", x)
		}
	}
	e.eval.Evaluar(context.Background())
	if ev := e.bus.tomar(); len(ev) != 0 {
		t.Errorf("La caída no se repite: %+v", ev)
	}

	// La 100 vuelve a encender: se resuelve.
	e.inv.instancias = []ports.InstanciaProxmoxDTO{{Vmid: 100, Nombre: "srv-database", Tipo: "qemu", Estado: "running", MaxMem: 100}}
	e.eval.Evaluar(context.Background())
	ev = e.bus.tomar()
	if len(ev) != 1 || ev[0].Tipo != ports.EventoAlertaResuelta || ev[0].RecursoID != "100" {
		t.Errorf("Al volver a encender se resuelve: %+v", ev)
	}
}

// Sin inventario (Proxmox caído) no se toca el estado recordado: no hay
// caídas falsas cuando vuelve.
func TestAlertas_SinInventarioNoInventaCaidas(t *testing.T) {
	e := nuevoEntornoAlertas()
	e.nodo.fijar(10, 10, 10)
	e.inv.instancias = []ports.InstanciaProxmoxDTO{{Vmid: 100, Nombre: "srv", Tipo: "qemu", Estado: "running", MaxMem: 100}}
	e.eval.Evaluar(context.Background())
	e.inv.err = ports.ErrProxmoxNoDisponible
	e.eval.Evaluar(context.Background())
	e.inv.err = nil
	e.eval.Evaluar(context.Background())
	if ev := e.bus.tomar(); len(ev) != 0 {
		t.Errorf("No debe haber alertas: %+v", ev)
	}
}

// El ciclo termina al cancelar el contexto (apagado ordenado), y con dos
// réplicas solo una evalúa por ciclo.
func TestAlertas_CicloYTurnoPorReplica(t *testing.T) {
	e := nuevoEntornoAlertas()
	e.nodo.fijar(95, 10, 10)
	otra := services.NewEvaluadorAlertas(e.nodo, e.inv, e.tareas, e.kv, e.bus, e.audit, "pve", 30*time.Millisecond)
	propia := services.NewEvaluadorAlertas(e.nodo, e.inv, e.tareas, e.kv, e.bus, e.audit, "pve", 30*time.Millisecond)
	ctx, cancelar := context.WithCancel(context.Background())
	finA, finB := propia.Iniciar(ctx), otra.Iniciar(ctx)
	time.Sleep(50 * time.Millisecond) // un solo ciclo de 30 ms
	cancelar()
	for _, fin := range []<-chan struct{}{finA, finB} {
		select {
		case <-fin:
		case <-time.After(time.Second):
			t.Fatal("El ciclo no terminó al cancelar el contexto")
		}
	}
	if ev := e.bus.tomar(); len(ev) != 0 {
		t.Errorf("Dos réplicas en el mismo ciclo no deben contar dos ciclos: %+v", ev)
	}
}
