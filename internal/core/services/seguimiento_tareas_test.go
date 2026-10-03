package services_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"

	"github.com/google/uuid"
)

// proxmoxTareas simula Proxmox: la tarea termina después de `vueltas` consultas.
type proxmoxTareas struct {
	mu         sync.Mutex
	vueltas    int
	exitStatus string
	fallasRed  int // primeras consultas que fallan por red
	tipo       string
}

func (p *proxmoxTareas) EstadoTarea(context.Context, string) (*ports.EstadoTareaDTO, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fallasRed > 0 {
		p.fallasRed--
		return nil, ports.ErrProxmoxNoDisponible
	}
	if p.vueltas > 0 {
		p.vueltas--
		return &ports.EstadoTareaDTO{}, nil
	}
	return &ports.EstadoTareaDTO{Terminada: true, ExitStatus: p.exitStatus}, nil
}
func (p *proxmoxTareas) ObtenerInstancia(_ context.Context, vmid int) (*ports.InstanciaProxmoxDTO, error) {
	return &ports.InstanciaProxmoxDTO{Vmid: vmid, Tipo: p.tipo}, nil
}
func (p *proxmoxTareas) ListarInstancias(context.Context) ([]ports.InstanciaProxmoxDTO, error) {
	return nil, nil
}
func (p *proxmoxTareas) IniciarInstancia(context.Context, int) (string, error) { return "", nil }
func (p *proxmoxTareas) DetenerInstancia(context.Context, int) (string, error) { return "", nil }
func (p *proxmoxTareas) ReiniciarInstancia(context.Context, int) (string, error) { return "", nil }
func (p *proxmoxTareas) Shutdown(context.Context, string, int, string) (string, error) { return "", nil }
func (p *proxmoxTareas) Reboot(context.Context, string, int, string) (string, error) { return "", nil }
func (p *proxmoxTareas) EliminarInstancia(context.Context, int) error { return nil }

type tareasEnMemoria struct {
	mu     sync.Mutex
	tareas map[uuid.UUID]*domain.TareaAsincrona
}

func (r *tareasEnMemoria) Crear(_ context.Context, t *domain.TareaAsincrona) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copia := *t
	r.tareas[t.ID] = &copia
	return nil
}
func (r *tareasEnMemoria) ActualizarEstado(_ context.Context, id uuid.UUID, estado string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tareas[id]; ok {
		t.Estado = estado
		return nil
	}
	return errors.New("no existe")
}
func (r *tareasEnMemoria) BuscarTareasActivasPorVmids(ctx context.Context, vmids []int) (map[int]string, error) {
	return map[int]string{}, nil
}
func (r *tareasEnMemoria) estado(id uuid.UUID) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tareas[id].Estado
}

// publicadorFalso captura los eventos publicados.
type publicadorFalso struct {
	ports.EventosService
	publicados chan ports.RealtimeEvent
}

func (p *publicadorFalso) Publicar(_ context.Context, ev ports.RealtimeEvent) error {
	p.publicados <- ev
	return nil
}

func seguirTarea(t *testing.T, px *proxmoxTareas, accion string) (uuid.UUID, *tareasEnMemoria, ports.RealtimeEvent) {
	t.Helper()
	repo := &tareasEnMemoria{tareas: map[uuid.UUID]*domain.TareaAsincrona{}}
	pub := &publicadorFalso{publicados: make(chan ports.RealtimeEvent, 1)}
	seg := services.NewSeguimientoTareas(px, repo, pub, 10*time.Millisecond)

	id, err := seg.Seguir(context.Background(), uuid.New(), 110, accion, "UPID:proxmox:0001:0002:0003:qm"+accion+":110:centinela-api@pve!backend-token:")
	if err != nil {
		t.Fatal(err)
	}
	if estado := repo.estado(id); estado != ports.TareaRunning {
		t.Errorf("Al registrarla la tarea debe estar RUNNING, está %s", estado)
	}
	select {
	case ev := <-pub.publicados:
		return id, repo, ev
	case <-time.After(3 * time.Second):
		t.Fatal("No se publicó TASK_FINISHED")
		return id, repo, ports.RealtimeEvent{}
	}
}

func TestSeguimiento_TareaExitosa(t *testing.T) {
	id, repo, ev := seguirTarea(t, &proxmoxTareas{vueltas: 3, exitStatus: "OK", tipo: "qemu"}, "start")

	if repo.estado(id) != ports.TareaCompleted {
		t.Errorf("Estado final = %s, se esperaba COMPLETED", repo.estado(id))
	}
	if ev.Tipo != ports.EventoTareaFinalizada || ev.Severidad != ports.SeveridadInfo || ev.RecursoTipo != ports.RecursoVM || ev.RecursoID != "110" {
		t.Errorf("Evento inesperado: %+v", ev)
	}
	if ev.Mensaje != "La tarea de encendido finalizó correctamente" {
		t.Errorf("Mensaje: %q", ev.Mensaje)
	}
	if ev.Detalles["tareaId"] != id.String() || ev.Detalles["estado"] != ports.TareaCompleted || ev.Detalles["accion"] != "start" {
		t.Errorf("Detalles: %+v", ev.Detalles)
	}
	if _, ok := ev.Detalles["upid"]; ok {
		t.Error("El UPID no debe viajar al front (en tareas_asincronas está oculto)")
	}
}

func TestSeguimiento_TareaFallida(t *testing.T) {
	id, repo, ev := seguirTarea(t, &proxmoxTareas{exitStatus: "CT 201 not running", tipo: "lxc"}, "stop")
	if repo.estado(id) != ports.TareaFailed {
		t.Errorf("Estado final = %s, se esperaba FAILED", repo.estado(id))
	}
	if ev.Severidad != ports.SeveridadWarning || ev.RecursoTipo != ports.RecursoLXC || ev.Mensaje != "La tarea de apagado falló" {
		t.Errorf("Evento inesperado: %+v", ev)
	}
	if ev.Detalles["estado"] != ports.TareaFailed || ev.Detalles["error"] != "CT 201 not running" {
		t.Errorf("Detalles: %+v", ev.Detalles)
	}
}

func TestSeguimiento_ReintentaSiProxmoxNoResponde(t *testing.T) {
	id, repo, _ := seguirTarea(t, &proxmoxTareas{fallasRed: 3, exitStatus: "OK", tipo: "qemu"}, "start")
	if repo.estado(id) != ports.TareaCompleted {
		t.Errorf("Después de errores de red transitorios debe terminar COMPLETED, quedó %s", repo.estado(id))
	}
}
