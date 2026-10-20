package services_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/memoria"
	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"

	"github.com/google/uuid"
)

// accesoWSFalso es el estado que consulta el servicio: usuarios y permisos,
// protegidos con mutex porque el test los cambia mientras la vigilancia los lee.
type accesoWSFalso struct {
	ports.AuthRepository
	mu       sync.Mutex
	usuarios map[uuid.UUID]domain.Usuario
	permisos map[uuid.UUID]map[int]bool
	fallaDB  bool
}

func (a *accesoWSFalso) BuscarUsuarioPorID(_ context.Context, id uuid.UUID) (*domain.Usuario, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fallaDB {
		return nil, errors.New("conexión rechazada")
	}
	u, ok := a.usuarios[id]
	if !ok {
		return nil, ports.ErrUsuarioNoEncontrado
	}
	return &u, nil
}

func (a *accesoWSFalso) VerificarAcceso(_ context.Context, id uuid.UUID, vmid int, _ string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fallaDB {
		return false, errors.New("conexión rechazada")
	}
	return a.permisos[id][vmid], nil
}

func (a *accesoWSFalso) PurgarPermisosPorVmid(_ context.Context, vmid int, _ string, _ *uuid.UUID) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, perm := range a.permisos {
		delete(perm, vmid)
	}
	return []string{}, nil
}

func (a *accesoWSFalso) cambiar(f func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f()
}

// kvContado cuenta las suscripciones al bus que siguen abiertas.
type kvContado struct {
	*memoria.KVStore
	mu       sync.Mutex
	abiertas int
}

func (k *kvContado) Subscribe(ctx context.Context, canal string) (<-chan *ports.Mensaje, func() error, error) {
	mensajes, cancelar, err := k.KVStore.Subscribe(ctx, canal)
	if err != nil {
		return nil, nil, err
	}
	k.mu.Lock()
	k.abiertas++
	k.mu.Unlock()
	var una sync.Once
	return mensajes, func() error {
		una.Do(func() { k.mu.Lock(); k.abiertas--; k.mu.Unlock() })
		return cancelar()
	}, nil
}

func (k *kvContado) suscripcionesAbiertas() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.abiertas
}

type entornoWS struct {
	kv       *kvContado
	acceso   *accesoWSFalso
	svc      ports.MetricasVivoService
	admin    uuid.UUID
	operador uuid.UUID
}

func identidadWS(usuario uuid.UUID) ports.SesionWebSocket {
	return ports.SesionWebSocket{UsuarioID: usuario, SesionID: uuid.New()}
}

func nuevoEntornoWS() *entornoWS {
	admin, operador := uuid.New(), uuid.New()
	acceso := &accesoWSFalso{
		usuarios: map[uuid.UUID]domain.Usuario{
			admin:    {ID: admin, Rol: "ADMIN", Activo: true},
			operador: {ID: operador, Rol: "OPERATOR", Activo: true},
		},
		permisos: map[uuid.UUID]map[int]bool{operador: {110: true}},
	}
	kv := &kvContado{KVStore: memoria.Nuevo()}
	return &entornoWS{kv: kv, acceso: acceso, admin: admin, operador: operador,
		svc: services.NewMetricasVivoService(kv, acceso, acceso, 20*time.Millisecond)}
}

func (e *entornoWS) ticket(t *testing.T, usuario uuid.UUID) string {
	t.Helper()
	ticket := uuid.NewString()
	if err := e.kv.Set(context.Background(), "ws:ticket:"+ticket, identidadWS(usuario), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	return ticket
}

func esperarMotivo(t *testing.T, cortes <-chan string, limite time.Duration) string {
	t.Helper()
	select {
	case m := <-cortes:
		return m
	case <-time.After(limite):
		return ""
	}
}

func TestMetricasVivo_ConsumirTicket(t *testing.T) {
	e := nuevoEntornoWS()
	ctx := context.Background()

	ticket := e.ticket(t, e.operador)
	if identidad, err := e.svc.ConsumirTicket(ctx, ticket, 110); err != nil || identidad.UsuarioID != e.operador || identidad.SesionID == uuid.Nil {
		t.Fatalf("Ticket válido con permiso: %v %v", identidad, err)
	}
	if _, err := e.svc.ConsumirTicket(ctx, ticket, 110); !errors.Is(err, ports.ErrTicketInvalido) {
		t.Errorf("Un ticket reutilizado debe ser inválido: %v", err)
	}
	if _, err := e.svc.ConsumirTicket(ctx, uuid.NewString(), 110); !errors.Is(err, ports.ErrTicketInvalido) {
		t.Errorf("Un ticket inexistente debe ser inválido: %v", err)
	}
	if _, err := e.svc.ConsumirTicket(ctx, e.ticket(t, e.operador), 9002); !errors.Is(err, ports.ErrAccesoInstanciaDenegado) {
		t.Errorf("Sin permiso sobre la instancia: %v", err)
	}
	if _, err := e.svc.ConsumirTicket(ctx, e.ticket(t, e.admin), 9002); err != nil {
		t.Errorf("ADMIN ve cualquier instancia: %v", err)
	}

	e.acceso.cambiar(func() { u := e.acceso.usuarios[e.operador]; u.Activo = false; e.acceso.usuarios[e.operador] = u })
	if _, err := e.svc.ConsumirTicket(ctx, e.ticket(t, e.operador), 110); !errors.Is(err, ports.ErrCuentaSuspendida) {
		t.Errorf("Cuenta suspendida: %v", err)
	}
	e.acceso.cambiar(func() {
		u := e.acceso.usuarios[e.operador]
		ahora := time.Now()
		u.EliminadoEn = &ahora
		e.acceso.usuarios[e.operador] = u
	})
	if _, err := e.svc.ConsumirTicket(ctx, e.ticket(t, e.operador), 110); !errors.Is(err, ports.ErrTicketInvalido) {
		t.Errorf("Cuenta eliminada: el ticket no sirve: %v", err)
	}

	// Fail-closed: si no se puede comprobar el acceso, no se abre el canal.
	e.acceso.cambiar(func() { e.acceso.fallaDB = true })
	if _, err := e.svc.ConsumirTicket(ctx, e.ticket(t, e.admin), 110); err == nil {
		t.Error("Con la base caída no debe aceptarse el ticket")
	}
}

// Cada causa de pérdida de acceso corta la conexión con su motivo.
func TestMetricasVivo_VigilarCortaAlPerderElAcceso(t *testing.T) {
	casos := []struct {
		nombre string
		causa  func(e *entornoWS, identidad ports.SesionWebSocket)
		motivo string
	}{
		{"logout", func(e *entornoWS, identidad ports.SesionWebSocket) {
			_ = e.kv.Publish(context.Background(), ports.CanalEventos, ports.MensajeBus{Tipo: ports.MensajeLogout, UsuarioID: &e.operador, SesionID: &identidad.SesionID})
		}, ports.MotivoWSLogout},
		{"sesiones revocadas", func(e *entornoWS, _ ports.SesionWebSocket) {
			_ = e.kv.Publish(context.Background(), ports.CanalEventos, ports.MensajeBus{Tipo: ports.MensajeSesionesRevocadas, UsuarioID: &e.operador})
		}, ports.MotivoWSSesionesRevocadas},
		{"permiso quitado", func(e *entornoWS, _ ports.SesionWebSocket) {
			e.acceso.cambiar(func() { delete(e.acceso.permisos[e.operador], 110) })
		}, ports.MotivoWSAccesoDenegado},
		{"suspendido", func(e *entornoWS, _ ports.SesionWebSocket) {
			e.acceso.cambiar(func() { u := e.acceso.usuarios[e.operador]; u.Activo = false; e.acceso.usuarios[e.operador] = u })
		}, ports.MotivoWSUsuarioInactivo},
		{"eliminado", func(e *entornoWS, _ ports.SesionWebSocket) {
			e.acceso.cambiar(func() {
				u := e.acceso.usuarios[e.operador]
				ahora := time.Now()
				u.Activo, u.EliminadoEn = false, &ahora
				e.acceso.usuarios[e.operador] = u
			})
		}, ports.MotivoWSUsuarioInactivo},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			e := nuevoEntornoWS()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			identidad := identidadWS(e.operador)
			cortes, err := e.svc.Vigilar(ctx, identidad, 110)
			if err != nil {
				t.Fatal(err)
			}
			if m := esperarMotivo(t, cortes, 100*time.Millisecond); m != "" {
				t.Fatalf("Con acceso vigente no debe cortarse: %s", m)
			}
			c.causa(e, identidad)
			if m := esperarMotivo(t, cortes, time.Second); m != c.motivo {
				t.Errorf("motivo = %q, se esperaba %q", m, c.motivo)
			}
		})
	}
}

func TestMetricasVivo_RevocacionSelectivaSoloCortaLaSesionIndicada(t *testing.T) {
	e := nuevoEntornoWS()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	revocada := identidadWS(e.operador)
	conservada := identidadWS(e.operador)
	corteRevocada, _ := e.svc.Vigilar(ctx, revocada, 110)
	corteConservada, _ := e.svc.Vigilar(ctx, conservada, 110)
	_ = e.kv.Publish(ctx, ports.CanalEventos, ports.MensajeBus{
		Tipo: ports.MensajeSesionesRevocadas, UsuarioID: &e.operador, SesionID: &revocada.SesionID,
	})
	if motivo := esperarMotivo(t, corteRevocada, time.Second); motivo != ports.MotivoWSSesionesRevocadas {
		t.Fatalf("la sesión revocada no se cortó: %q", motivo)
	}
	if motivo := esperarMotivo(t, corteConservada, 150*time.Millisecond); motivo != "" {
		t.Fatalf("la sesión conservada no debía cortarse: %q", motivo)
	}
}

// El logout de OTRO usuario no corta, y un error transitorio de la base tampoco
// (no es evidencia de que perdió el acceso).
func TestMetricasVivo_VigilarNoCortaSinMotivo(t *testing.T) {
	e := nuevoEntornoWS()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cortes, err := e.svc.Vigilar(ctx, identidadWS(e.operador), 110)
	if err != nil {
		t.Fatal(err)
	}
	otro, sesion := uuid.New(), uuid.New()
	_ = e.kv.Publish(ctx, ports.CanalEventos, ports.MensajeBus{Tipo: ports.MensajeLogout, UsuarioID: &otro, SesionID: &sesion})
	e.acceso.cambiar(func() { e.acceso.fallaDB = true })
	if m := esperarMotivo(t, cortes, 200*time.Millisecond); m != "" {
		t.Errorf("No debía cortarse: %s", m)
	}
	e.acceso.cambiar(func() { e.acceso.fallaDB = false; delete(e.acceso.permisos[e.operador], 110) })
	if m := esperarMotivo(t, cortes, time.Second); m != ports.MotivoWSAccesoDenegado {
		t.Errorf("Al volver la base debe detectar el permiso quitado: %q", m)
	}
}

// Al cancelar el contexto (el cliente cerró la pestaña) la goroutine de
// vigilancia termina y libera la suscripción al bus.
func TestMetricasVivo_VigilarTerminaAlCancelar(t *testing.T) {
	e := nuevoEntornoWS()
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := e.svc.Vigilar(ctx, identidadWS(e.operador), 110); err != nil {
		t.Fatal(err)
	}
	cancel()
	limite := time.Now().Add(time.Second)
	for e.kv.suscripcionesAbiertas() > 0 && time.Now().Before(limite) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := e.kv.suscripcionesAbiertas(); n != 0 {
		t.Errorf("La suscripción al bus debe liberarse al cerrar la conexión: quedan %d", n)
	}
}
