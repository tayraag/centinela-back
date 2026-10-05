package services_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"

	"github.com/google/uuid"
)

// proxmoxPool simula Proxmox para el pool: cada tarea termina en un instante
// dado, mide cuántas consultas hay en paralelo y puede quedar "trabado".
type proxmoxPool struct {
	proxmoxTareas
	mu          sync.Mutex
	finTarea    map[string]time.Time // upid → cuándo termina
	demora      time.Duration        // cuánto tarda cada consulta
	enVuelo     atomic.Int32
	maxEnVuelo  atomic.Int32
	consultas   atomic.Int32
	bloqueo     chan struct{} // si no es nil, cada consulta espera a que se cierre
	ctxCortados atomic.Int32  // consultas cuyo contexto llegó cancelado
}

func (p *proxmoxPool) EstadoTarea(ctx context.Context, upid string) (*ports.EstadoTareaDTO, error) {
	actual := p.enVuelo.Add(1)
	defer p.enVuelo.Add(-1)
	for {
		max := p.maxEnVuelo.Load()
		if actual <= max || p.maxEnVuelo.CompareAndSwap(max, actual) {
			break
		}
	}
	p.consultas.Add(1)
	if p.bloqueo != nil {
		<-p.bloqueo
	}
	time.Sleep(p.demora)
	if ctx.Err() != nil {
		p.ctxCortados.Add(1)
	}
	p.mu.Lock()
	fin, ok := p.finTarea[upid]
	p.mu.Unlock()
	if !ok {
		return nil, ports.ErrProxmoxNoDisponible
	}
	return &ports.EstadoTareaDTO{Terminada: !time.Now().Before(fin), ExitStatus: "OK"}, nil
}

func (p *proxmoxPool) tarea(upid string, fin time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finTarea == nil {
		p.finTarea = map[string]time.Time{}
	}
	p.finTarea[upid] = fin
}

type entornoPool struct {
	px     *proxmoxPool
	repo   *tareasEnMemoria
	pub    *publicadorFalso
	audit  *auditoriaGrabadora
	pool   *services.PoolSeguimiento
	cancel context.CancelFunc
}

func nuevoEntornoPool(t *testing.T, cfg services.ConfigSeguimiento, px *proxmoxPool) *entornoPool {
	t.Helper()
	e := &entornoPool{
		px:    px,
		repo:  &tareasEnMemoria{tareas: map[uuid.UUID]*domain.TareaAsincrona{}},
		pub:   &publicadorFalso{publicados: make(chan ports.RealtimeEvent, 500)},
		audit: &auditoriaGrabadora{},
	}
	e.pool = services.NewSeguimientoTareas(px, e.repo, e.pub, e.audit, cfg)
	return e
}

func (e *entornoPool) iniciar(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	t.Cleanup(cancel)
	e.pool.Iniciar(ctx)
}

func (e *entornoPool) esperarEventos(t *testing.T, n int, limite time.Duration) []ports.RealtimeEvent {
	t.Helper()
	var eventos []ports.RealtimeEvent
	fin := time.After(limite)
	for len(eventos) < n {
		select {
		case ev := <-e.pub.publicados:
			eventos = append(eventos, ev)
		case <-fin:
			t.Fatalf("Llegaron %d de %d TASK_FINISHED", len(eventos), n)
		}
	}
	return eventos
}

// DoD: 50 tareas simultáneas nunca generan más consultas paralelas que UPID_WORKERS.
func TestPool_50TareasRespetanElLimiteDeWorkers(t *testing.T) {
	for _, workers := range []int{8, 3} {
		t.Run(fmt.Sprintf("%d workers", workers), func(t *testing.T) {
			px := &proxmoxPool{demora: 20 * time.Millisecond}
			e := nuevoEntornoPool(t, services.ConfigSeguimiento{Workers: workers, Intervalo: 50 * time.Millisecond}, px)
			e.iniciar(t)

			ahora := time.Now()
			var wg sync.WaitGroup
			for i := 0; i < 50; i++ {
				upid := fmt.Sprintf("UPID:proxmox:%08X:0:0:qmstart:%d:centinela-api@pve!backend-token:", i, 1000+i)
				px.tarea(upid, ahora.Add(300*time.Millisecond)) // cada tarea necesita varias consultas
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					if _, err := e.pool.Seguir(context.Background(), uuid.New(), 1000+i, "start", upid); err != nil {
						t.Errorf("Seguir: %v", err)
					}
				}(i)
			}
			wg.Wait()
			e.esperarEventos(t, 50, 10*time.Second)

			if max := px.maxEnVuelo.Load(); max > int32(workers) {
				t.Errorf("Hubo %d consultas en paralelo a Proxmox, el límite era %d", max, workers)
			} else {
				t.Logf("máximo de consultas en paralelo: %d (límite %d), total de consultas: %d", max, workers, px.consultas.Load())
			}
			for id := range e.repo.tareas {
				if estado := e.repo.estado(id); estado != ports.TareaCompleted {
					t.Errorf("La tarea %s quedó %s", id, estado)
				}
			}
		})
	}
}

// DoD: el fin de una tarea se detecta en menos de 2 s.
func TestPool_DeteccionEnMenosDe2Segundos(t *testing.T) {
	px := &proxmoxPool{}
	e := nuevoEntornoPool(t, services.ConfigSeguimiento{}, px) // configuración por defecto: consulta cada 1 s
	e.iniciar(t)

	fin := time.Now().Add(1500 * time.Millisecond)
	upid := "UPID:proxmox:00000001:0:0:qmstop:110:centinela-api@pve!backend-token:"
	px.tarea(upid, fin)
	_, _ = e.pool.Seguir(context.Background(), uuid.New(), 110, "stop", upid)

	e.esperarEventos(t, 1, 5*time.Second)
	if demora := time.Since(fin); demora >= 2*time.Second {
		t.Errorf("El fin se detectó %s después de que la tarea terminó (máximo 2 s)", demora)
	} else {
		t.Logf("detectado %s después del fin de la tarea", demora.Round(time.Millisecond))
	}
}

// Seguir no bloquea el request aunque la cola esté llena; las que no entran las toma el reconciliador.
func TestPool_SeguirNoBloqueaConLaColaLlena(t *testing.T) {
	px := &proxmoxPool{bloqueo: make(chan struct{})} // los workers quedan trabados en la primera consulta
	e := nuevoEntornoPool(t, services.ConfigSeguimiento{Workers: 1, Buffer: 2, Intervalo: 20 * time.Millisecond, IntervaloReconciliar: 50 * time.Millisecond}, px)
	e.iniciar(t)

	ahora := time.Now()
	for i := 0; i < 10; i++ {
		upid := fmt.Sprintf("UPID:proxmox:%08X:0:0:qmstart:%d:x:", i, 2000+i)
		px.tarea(upid, ahora)
		inicio := time.Now()
		if _, err := e.pool.Seguir(context.Background(), uuid.New(), 2000+i, "start", upid); err != nil {
			t.Fatal(err)
		}
		if demora := time.Since(inicio); demora > 50*time.Millisecond {
			t.Fatalf("Seguir bloqueó %s con la cola llena", demora)
		}
	}
	if len(e.repo.tareas) != 10 {
		t.Fatalf("Las 10 tareas deben quedar registradas RUNNING aunque no entren en la cola: %d", len(e.repo.tareas))
	}

	close(px.bloqueo) // Proxmox vuelve a responder: el reconciliador encola el resto
	e.esperarEventos(t, 10, 5*time.Second)
}

// Las tareas que quedaron RUNNING de un arranque anterior se retoman al iniciar.
func TestPool_ReconciliadorRetomaTareasDeUnReinicio(t *testing.T) {
	px := &proxmoxPool{}
	e := nuevoEntornoPool(t, services.ConfigSeguimiento{Intervalo: 20 * time.Millisecond}, px)
	vieja := domain.TareaAsincrona{
		ID: uuid.New(), UsuarioID: uuid.New(), UpidProxmox: "UPID:proxmox:0:0:0:qmstart:110:x:",
		InstanciaID: "110", Accion: "start", Estado: ports.TareaRunning, FechaCreacion: time.Now().Add(-time.Minute),
	}
	trabada := domain.TareaAsincrona{ // de hace días y Proxmox ya no la conoce
		ID: uuid.New(), UsuarioID: uuid.New(), UpidProxmox: "UPID:proxmox:0:0:0:qmstop:110:x:",
		InstanciaID: "110", Accion: "stop", Estado: ports.TareaRunning, FechaCreacion: time.Now().Add(-72 * time.Hour),
	}
	px.tarea(vieja.UpidProxmox, time.Now())
	_ = e.repo.Crear(context.Background(), &vieja)
	_ = e.repo.Crear(context.Background(), &trabada)

	e.iniciar(t)
	eventos := e.esperarEventos(t, 2, 5*time.Second)
	if e.repo.estado(vieja.ID) != ports.TareaCompleted {
		t.Errorf("La tarea pendiente del arranque anterior debe completarse: %s", e.repo.estado(vieja.ID))
	}
	if e.repo.estado(trabada.ID) != ports.TareaFailed {
		t.Errorf("Una tarea trabada de hace días debe quedar FAILED: %s", e.repo.estado(trabada.ID))
	}
	estados := map[string]bool{}
	for _, ev := range eventos {
		estados[ev.Detalles["estado"].(string)] = true
		if ev.Detalles["tareaId"] == trabada.ID.String() &&
			(ev.Detalles["motivo"] != ports.MotivoTimeout || ev.Detalles["exitstatus"] != nil || ev.Detalles["error"] == nil) {
			t.Errorf("La trabada vence por tiempo: motivo TIMEOUT, exitstatus null y error con el texto: %+v", ev.Detalles)
		}
	}
	if !estados[ports.TareaCompleted] || !estados[ports.TareaFailed] {
		t.Errorf("Se esperaba un TASK_FINISHED de cada una: %v", estados)
	}
}

// Al cerrar el contexto los workers terminan; la consulta en curso no se corta
// y las tareas sin terminar quedan RUNNING para el próximo arranque.
func TestPool_ApagadoOrdenado(t *testing.T) {
	px := &proxmoxPool{demora: 200 * time.Millisecond}
	e := nuevoEntornoPool(t, services.ConfigSeguimiento{Workers: 2, Intervalo: 20 * time.Millisecond}, px)
	e.iniciar(t)
	upid := "UPID:proxmox:0:0:0:qmstart:110:x:"
	px.tarea(upid, time.Now().Add(time.Hour)) // no termina nunca
	id, _ := e.pool.Seguir(context.Background(), uuid.New(), 110, "start", upid)

	time.Sleep(50 * time.Millisecond) // hay una consulta en curso
	e.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := e.pool.Esperar(ctx); err != nil {
		t.Fatalf("Los workers no terminaron al apagar: %v", err)
	}
	if n := px.ctxCortados.Load(); n != 0 {
		t.Errorf("El apagado cortó %d consultas a la mitad", n)
	}
	if estado := e.repo.estado(id); estado != ports.TareaRunning {
		t.Errorf("La tarea sin terminar debe quedar RUNNING para el reconciliador: %s", estado)
	}
	consultas := px.consultas.Load()
	time.Sleep(100 * time.Millisecond)
	if px.consultas.Load() != consultas {
		t.Error("Después de apagar no deben hacerse más consultas")
	}
}

// Al terminar se audita con el mismo código de acción que el registro PENDING del handler.
func TestPool_AuditaElResultadoFinal(t *testing.T) {
	px := &proxmoxPool{}
	e := nuevoEntornoPool(t, services.ConfigSeguimiento{Intervalo: 20 * time.Millisecond}, px)
	e.iniciar(t)
	upid := "UPID:proxmox:0:0:0:qmshutdown:110:x:"
	px.tarea(upid, time.Now())
	id, _ := e.pool.Seguir(context.Background(), uuid.New(), 110, "shutdown", upid)
	e.esperarEventos(t, 1, 3*time.Second)

	reg := e.audit.buscar("SHUTDOWN")
	if reg == nil || reg.Resultado != ports.ResultadoExito || reg.InstanciaID != "110" || reg.Detalles["tareaId"] != id.String() {
		t.Errorf("Auditoría del final de la tarea: %+v", reg)
	}
}
