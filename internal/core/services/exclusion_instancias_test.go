package services_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/memoria"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"

	"github.com/google/uuid"
)

// tareasEnCursoFalsas: las tareas RUNNING por vmid, como tareas_asincronas.
type tareasEnCursoFalsas struct {
	ports.TareaRepository
	mu      sync.Mutex
	enCurso map[int]bool
	falla   error
}

func (f *tareasEnCursoFalsas) BuscarTareasActivasPorVmids(_ context.Context, vmids []int) (map[int]ports.ActiveTaskDTO, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.falla != nil {
		return nil, f.falla
	}
	activas := map[int]ports.ActiveTaskDTO{}
	for _, v := range vmids {
		if f.enCurso[v] {
			activas[v] = ports.ActiveTaskDTO{TareaID: uuid.NewString(), Action: "START", Status: ports.TareaRunning}
		}
	}
	return activas, nil
}

func (f *tareasEnCursoFalsas) registrar(vmid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enCurso[vmid] = true
}

func nuevaExclusion() (ports.ExclusionInstancias, *memoria.KVStore, *tareasEnCursoFalsas) {
	kv := memoria.Nuevo()
	tareas := &tareasEnCursoFalsas{enCurso: map[int]bool{}}
	return services.NewExclusionInstancias(kv, tareas), kv, tareas
}

func TestExclusion_InstanciaOciosaPasaYLiberaElLock(t *testing.T) {
	excl, kv, _ := nuevaExclusion()
	liberar, err := excl.Reservar(context.Background(), 110)
	if err != nil {
		t.Fatalf("Una instancia ociosa debe poder reservarse: %v", err)
	}
	if _, err := kv.Get(context.Background(), "lock:instance:110:operation"); err != nil {
		t.Error("Mientras está reservada debe existir lock:instance:110:operation")
	}
	liberar()
	if _, err := kv.Get(context.Background(), "lock:instance:110:operation"); !errors.Is(err, ports.ErrClaveNoEncontrada) {
		t.Errorf("Al liberar se debe borrar el lock: %v", err)
	}
	// Otras instancias son independientes.
	if _, err := excl.Reservar(context.Background(), 201); err != nil {
		t.Errorf("Otra instancia no debe verse afectada: %v", err)
	}
}

// Comprobación 1: lock Redis tomado por otra operación => ocupada, de inmediato.
func TestExclusion_LockTomadoEsOcupada(t *testing.T) {
	excl, kv, _ := nuevaExclusion()
	soltar, err := kv.AcquireLock(context.Background(), "lock:instance:110:operation", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer soltar(context.Background())

	inicio := time.Now()
	if _, err := excl.Reservar(context.Background(), 110); !errors.Is(err, ports.ErrInstanciaOcupada) {
		t.Fatalf("Con el lock tomado se esperaba ErrInstanciaOcupada, vino %v", err)
	}
	if demora := time.Since(inicio); demora > 500*time.Millisecond {
		t.Errorf("Debe rechazar sin hacer cola, tardó %s", demora)
	}
}

// Comprobación 2: tarea RUNNING en tareas_asincronas => ocupada, y el lock se suelta.
func TestExclusion_TareaEnCursoEsOcupada(t *testing.T) {
	excl, kv, tareas := nuevaExclusion()
	tareas.registrar(110)
	if _, err := excl.Reservar(context.Background(), 110); !errors.Is(err, ports.ErrInstanciaOcupada) {
		t.Fatalf("Con una tarea RUNNING se esperaba ErrInstanciaOcupada, vino %v", err)
	}
	if _, err := kv.Get(context.Background(), "lock:instance:110:operation"); !errors.Is(err, ports.ErrClaveNoEncontrada) {
		t.Error("Al rechazar por tarea en curso no debe quedar el lock tomado")
	}
}

// Si no se puede consultar la base, no se despacha (y el lock se suelta).
func TestExclusion_ErrorDeBaseNoDespacha(t *testing.T) {
	excl, kv, tareas := nuevaExclusion()
	tareas.falla = errors.New("conexión rechazada")
	if _, err := excl.Reservar(context.Background(), 110); !errors.Is(err, ports.ErrExclusionNoDisponible) {
		t.Fatalf("Se esperaba ErrExclusionNoDisponible, vino %v", err)
	}
	if _, err := kv.Get(context.Background(), "lock:instance:110:operation"); !errors.Is(err, ports.ErrClaveNoEncontrada) {
		t.Error("Con la base caída no debe quedar el lock tomado")
	}
}

// Ráfaga: 50 operaciones simultáneas sobre la misma instancia. Cada una que
// pasa "despacha" (registra su tarea RUNNING, como el handler) y libera. Solo
// una debe pasar: la reserva es atómica y después bloquea la tarea registrada.
func TestExclusion_RafagaConcurrenteSoloPasaUna(t *testing.T) {
	excl, _, tareas := nuevaExclusion()
	const n = 50
	var (
		pasaron  atomic.Int32
		ocupadas atomic.Int32
		otros    atomic.Int32
		largada  = make(chan struct{})
		wg       sync.WaitGroup
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-largada
			liberar, err := excl.Reservar(context.Background(), 110)
			switch {
			case err == nil:
				pasaron.Add(1)
				time.Sleep(20 * time.Millisecond) // la llamada a Proxmox
				tareas.registrar(110)             // Seguir() deja la tarea RUNNING
				liberar()
			case errors.Is(err, ports.ErrInstanciaOcupada):
				ocupadas.Add(1)
			default:
				otros.Add(1)
			}
		}()
	}
	close(largada)
	wg.Wait()
	if pasaron.Load() != 1 || ocupadas.Load() != n-1 || otros.Load() != 0 {
		t.Errorf("Pasaron %d, ocupadas %d, otros errores %d: se esperaba 1 y %d", pasaron.Load(), ocupadas.Load(), otros.Load(), n-1)
	}
}
