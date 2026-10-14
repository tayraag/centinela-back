package services_test

import (
	"context"
	"encoding/json"
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

func (p *proxmoxTareas) ObtenerEstadoNodo(context.Context, string) (*ports.NodeStatusDTO, error) {
	return &ports.NodeStatusDTO{}, nil
}

func (p *proxmoxTareas) ObtenerInterfaces(context.Context, string, string, int) ([]ports.InterfazRed, error) {
	return nil, nil
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
func (p *proxmoxTareas) Shutdown(context.Context, string, int, string) (string, error) {
	return "", nil
}
func (p *proxmoxTareas) Reboot(context.Context, string, int, string) (string, error) { return "", nil }
func (p *proxmoxTareas) EliminarInstancia(context.Context, string, int, string) (string, error) {
	return "", nil
}
func (p *proxmoxTareas) ObtenerStorages(context.Context, string) ([]ports.StorageDTO, error) {
	return nil, nil
}
func (p *proxmoxTareas) ObtenerContenidoStorage(context.Context, string, string) ([]ports.StorageContentDTO, error) {
	return nil, nil
}
func (p *proxmoxTareas) ObtenerMetricasRRD(context.Context, string, string, int, string) ([]domain.PuntoMetricaDTO, error) {
	return nil, nil
}

type tareasEnMemoria struct {
	mu     sync.Mutex
	tareas map[uuid.UUID]*domain.TareaAsincrona
	audit  *auditoriaGrabadora
}

func (r *tareasEnMemoria) Crear(_ context.Context, t *domain.TareaAsincrona, auditoria ports.RegistrarAuditoriaInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copia := *t
	r.tareas[t.ID] = &copia
	return nil
}
func (r *tareasEnMemoria) ActualizarEstado(_ context.Context, id uuid.UUID, estado string, metadatos map[string]any, auditoria ports.RegistrarAuditoriaInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tareas[id]; ok {
		if r.audit != nil {
			r.audit.Registrar(context.Background(), auditoria)
		}
		t.Estado = estado
		if metadatos != nil {
			crudo, _ := json.Marshal(metadatos)
			texto := string(crudo)
			t.Metadatos = &texto
		}
		return nil
	}
	return errors.New("no existe")
}
func (r *tareasEnMemoria) BuscarTareasActivasPorVmids(ctx context.Context, vmids []int) (map[int]ports.ActiveTaskDTO, error) {
	return map[int]ports.ActiveTaskDTO{}, nil
}
func (r *tareasEnMemoria) ListarEnCurso(context.Context) ([]domain.TareaAsincrona, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var lista []domain.TareaAsincrona
	for _, t := range r.tareas {
		if t.Estado == ports.TareaRunning {
			lista = append(lista, *t)
		}
	}
	return lista, nil
}
func (r *tareasEnMemoria) metadatos(id uuid.UUID) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tareas[id].Metadatos == nil {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(*r.tareas[id].Metadatos), &m)
	return m
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
	cfg := services.ConfigSeguimiento{
		Intervalo: 10 * time.Millisecond,
		OnTaskFinished: func(ctx context.Context, tarea *domain.TareaAsincrona, recursoTipo string, r services.ResultadoTarea) {
			_ = pub.Publicar(ctx, services.EventoTareaFinalizada(tarea, recursoTipo, r))
		},
	}
	seg := services.NewSeguimientoTareas(px, repo, services.NewAuditService(nil), cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	seg.Iniciar(ctx)

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
	case <-time.After(10 * time.Second): // con errores de red hay backoff de 1, 2, 4… s
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
	// Contrato: siempre las seis claves; accion en mayúsculas; motivo y error null.
	esperados := map[string]any{"tareaId": id.String(), "accion": "START", "estado": ports.TareaCompleted, "exitstatus": "OK", "motivo": nil, "error": nil}
	if len(ev.Detalles) != len(esperados) {
		t.Errorf("Detalles: %+v", ev.Detalles)
	}
	for clave, valor := range esperados {
		if v, ok := ev.Detalles[clave]; !ok || v != valor {
			t.Errorf("detalles.%s = %v (presente=%v), se esperaba %v", clave, v, ok, valor)
		}
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
	if ev.Detalles["estado"] != ports.TareaFailed || ev.Detalles["accion"] != "STOP" || ev.Detalles["motivo"] != ports.MotivoProxmoxError ||
		ev.Detalles["exitstatus"] != "CT 201 not running" || ev.Detalles["error"] != "CT 201 not running" {
		t.Errorf("Detalles: %+v", ev.Detalles)
	}
}

// Proxmox marca "WARNINGS: N" cuando la tarea terminó bien pero con
// advertencias (p. ej. el aviso de nesting de systemd): no es un fallo.
func TestSeguimiento_AvisoDeProxmoxNoEsFalla(t *testing.T) {
	id, repo, ev := seguirTarea(t, &proxmoxTareas{vueltas: 1, exitStatus: "WARNINGS: 1", tipo: "lxc"}, "start")

	if repo.estado(id) != ports.TareaCompleted {
		t.Errorf("Estado final = %s, se esperaba COMPLETED", repo.estado(id))
	}
	if ev.Severidad != ports.SeveridadInfo || ev.RecursoTipo != ports.RecursoLXC || ev.Mensaje != "La tarea de encendido finalizó correctamente" {
		t.Errorf("Evento inesperado: %+v", ev)
	}
	if ev.Detalles["estado"] != ports.TareaCompleted || ev.Detalles["exitstatus"] != "WARNINGS: 1" ||
		ev.Detalles["motivo"] != nil || ev.Detalles["error"] != nil {
		t.Errorf("Detalles: %+v", ev.Detalles)
	}
}

func TestSeguimiento_BorradoUsaAccionDelete(t *testing.T) {
	_, _, ev := seguirTarea(t, &proxmoxTareas{exitStatus: "OK", tipo: "qemu"}, "delete")
	if ev.Mensaje != "La tarea de eliminación finalizó correctamente" || ev.Detalles["accion"] != "DELETE" {
		t.Errorf("Evento del borrado: %q %+v", ev.Mensaje, ev.Detalles)
	}
}

func TestSeguimiento_CreacionNormalizaEventoYConservaContexto(t *testing.T) {
	casos := []struct {
		nombre      string
		accion      string
		tipoProxmox string
		recursoTipo string
	}{
		{"código de dominio VM", ports.AccionCrearVM, ports.TipoInstanciaQemu, ports.RecursoVM},
		{"operación UPID VM", ports.AccionProxmoxCrearVM, ports.TipoInstanciaQemu, ports.RecursoVM},
		{"código de dominio LXC", ports.AccionCrearLXC, ports.TipoInstanciaLXC, ports.RecursoLXC},
		{"operación UPID LXC", ports.AccionProxmoxCrearLXC, ports.TipoInstanciaLXC, ports.RecursoLXC},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			id, repo, ev := seguirTarea(t, &proxmoxTareas{exitStatus: "OK", tipo: caso.tipoProxmox}, caso.accion)

			if ev.RecursoTipo != caso.recursoTipo || ev.RecursoID != "110" {
				t.Fatalf("recurso de creación incorrecto: %+v", ev)
			}
			if ev.Detalles["accion"] != "CREATE" || ev.Detalles["estado"] != ports.TareaCompleted {
				t.Fatalf("detalles de creación incorrectos: %+v", ev.Detalles)
			}
			if ev.Mensaje != "La tarea de creación finalizó correctamente" {
				t.Fatalf("mensaje de creación incorrecto: %q", ev.Mensaje)
			}

			repo.mu.Lock()
			persistida := *repo.tareas[id]
			repo.mu.Unlock()
			if persistida.UsuarioID == uuid.Nil || persistida.InstanciaID != "110" {
				t.Fatalf("la tarea no conservó creador y VMID reservado: %+v", persistida)
			}
		})
	}
}

func TestSeguimiento_ReintentaSiProxmoxNoResponde(t *testing.T) {
	id, repo, _ := seguirTarea(t, &proxmoxTareas{fallasRed: 3, exitStatus: "OK", tipo: "qemu"}, "start")
	if repo.estado(id) != ports.TareaCompleted {
		t.Errorf("Después de errores de red transitorios debe terminar COMPLETED, quedó %s", repo.estado(id))
	}
}
