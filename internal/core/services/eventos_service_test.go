package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/memoria"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"

	"github.com/google/uuid"
)

// permisosFalsos implementa ports.InstanceRepository: el usuario tiene permiso
// sobre los vmid del mapa.
type permisosFalsos struct {
	mu       sync.Mutex
	permisos map[int]bool
}

func (p *permisosFalsos) VerificarAcceso(_ context.Context, _ uuid.UUID, vmid int, _ string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.permisos[vmid], nil
}

// auditoriaGrabadora guarda los registros para verificarlos.
type auditoriaGrabadora struct {
	auditoriaNula
	mu        sync.Mutex
	registros []ports.RegistrarAuditoriaInput
}

func (a *auditoriaGrabadora) Registrar(_ context.Context, in ports.RegistrarAuditoriaInput) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.registros = append(a.registros, in)
}

func (a *auditoriaGrabadora) buscar(accion string) *ports.RegistrarAuditoriaInput {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.registros {
		if a.registros[i].Accion == accion {
			return &a.registros[i]
		}
	}
	return nil
}

type entornoEventos struct {
	*entornoAuth
	eventos   ports.EventosService
	permisos  *permisosFalsos
	auditoria *auditoriaGrabadora
	avanzar   func(time.Duration)
}

func nuevoEntornoEventos(t *testing.T, rol string) *entornoEventos {
	t.Helper()
	base := nuevoEntornoAuth(t)
	var mu sync.Mutex
	reloj := time.Now()
	kv := memoria.NuevoConReloj(func() time.Time { mu.Lock(); defer mu.Unlock(); return reloj })
	base.kv = kv
	base.svc = services.NewAuthService(base.repo, kv, nil, auditoriaNula{})
	base.repo.usuario.Rol = rol

	e := &entornoEventos{
		entornoAuth: base,
		permisos:    &permisosFalsos{permisos: map[int]bool{}},
		auditoria:   &auditoriaGrabadora{},
		avanzar:     func(d time.Duration) { mu.Lock(); reloj = reloj.Add(d); mu.Unlock() },
	}
	e.eventos = services.NewEventosService(kv, base.repo, e.permisos, e.auditoria)
	return e
}

// conectar hace login + 2FA, pide un ticket y abre el stream.
func (e *entornoEventos) conectar(t *testing.T) (*ports.ConexionEventos, *ports.TokenResult) {
	t.Helper()
	tokens, _ := e.login(t)
	c := claimsDe(t, tokens.AccessToken)
	ticket, err := e.eventos.EmitirTicket(context.Background(), e.repo.usuario.ID, uuid.MustParse(c.SesionID))
	if err != nil {
		t.Fatal(err)
	}
	conexion, err := e.eventos.Conectar(context.Background(), ticket)
	if err != nil {
		t.Fatalf("Conectar: %v", err)
	}
	t.Cleanup(conexion.Cerrar)
	return conexion, tokens
}

func eventoDe(t *testing.T, recursoTipo, recursoID string) ports.RealtimeEvent {
	t.Helper()
	ev, err := ports.NewRealtimeEvent(ports.EventoTareaFinalizada, ports.SeveridadInfo, "prueba")
	if err != nil {
		t.Fatal(err)
	}
	return ev.ConRecurso(recursoTipo, recursoID)
}

func recibeEvento(t *testing.T, c *ports.ConexionEventos, esperado ports.RealtimeEvent) {
	t.Helper()
	select {
	case ev := <-c.Eventos:
		if ev.ID != esperado.ID {
			t.Fatalf("Llegó otro evento: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("No llegó el evento")
	}
}

func noRecibeNada(t *testing.T, c *ports.ConexionEventos) {
	t.Helper()
	select {
	case ev, ok := <-c.Eventos:
		if ok {
			t.Fatalf("No debía llegar ningún evento, llegó %+v", ev)
		}
		t.Fatal("El stream se cerró y no debía")
	case m := <-c.Cierre:
		t.Fatalf("El stream se cortó (%s) y no debía", m)
	case <-time.After(200 * time.Millisecond):
	}
}

func seCorta(t *testing.T, c *ports.ConexionEventos, motivo string) {
	t.Helper()
	select {
	case m := <-c.Cierre:
		if m != motivo {
			t.Fatalf("Motivo de cierre = %s, se esperaba %s", m, motivo)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("El stream no se cortó (se esperaba %s)", motivo)
	}
	select {
	case _, abierto := <-c.Eventos:
		if abierto {
			t.Fatal("Después del corte no deben llegar eventos")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Después del corte el canal de eventos debe cerrarse")
	}
}

// ==========================================
// Ticket (DoD: 401 sin ticket, reutilizado o vencido)
// ==========================================

func TestTicket_UnSoloUsoYConTTLDe30Segundos(t *testing.T) {
	e := nuevoEntornoEventos(t, "ADMIN")
	tokens, _ := e.login(t)
	sid := uuid.MustParse(claimsDe(t, tokens.AccessToken).SesionID)
	ctx := context.Background()

	ticket, err := e.eventos.EmitirTicket(ctx, e.repo.usuario.ID, sid)
	if err != nil {
		t.Fatal(err)
	}
	if id, err := uuid.Parse(ticket); err != nil || id.Version() != 4 {
		t.Fatalf("El ticket debe ser un UUID v4: %q", ticket)
	}
	valor, err := e.kv.Get(ctx, "ws_ticket:"+ticket)
	if err != nil {
		t.Fatalf("Debe existir ws_ticket:<uuid>: %v", err)
	}
	var guardado map[string]string
	_ = json.Unmarshal([]byte(valor), &guardado)
	if guardado["usuario_id"] != e.repo.usuario.ID.String() || guardado["sesion_id"] != sid.String() {
		t.Errorf("El ticket debe guardar usuario y sesión: %s", valor)
	}

	// Primer uso: abre. Segundo uso: rechazado.
	conexion, err := e.eventos.Conectar(ctx, ticket)
	if err != nil {
		t.Fatal(err)
	}
	conexion.Cerrar()
	if _, err := e.eventos.Conectar(ctx, ticket); !errors.Is(err, ports.ErrTicketInvalido) {
		t.Errorf("Un ticket reutilizado debe rechazarse, vino %v", err)
	}

	// Vencido: a los 31 s ya no sirve.
	vencido, _ := e.eventos.EmitirTicket(ctx, e.repo.usuario.ID, sid)
	e.avanzar(31 * time.Second)
	if _, err := e.eventos.Conectar(ctx, vencido); !errors.Is(err, ports.ErrTicketInvalido) {
		t.Errorf("Un ticket vencido debe rechazarse, vino %v", err)
	}

	for _, invalido := range []string{"", "no-existe", uuid.NewString()} {
		if _, err := e.eventos.Conectar(ctx, invalido); !errors.Is(err, ports.ErrTicketInvalido) {
			t.Errorf("Ticket %q debe rechazarse, vino %v", invalido, err)
		}
	}
}

func TestTicket_DeUnaSesionCerradaNoSirve(t *testing.T) {
	e := nuevoEntornoEventos(t, "ADMIN")
	tokens, _ := e.login(t)
	sid := uuid.MustParse(claimsDe(t, tokens.AccessToken).SesionID)
	ticket, _ := e.eventos.EmitirTicket(context.Background(), e.repo.usuario.ID, sid)
	_ = e.svc.CerrarSesion(context.Background(), tokens.RefreshToken)
	if _, err := e.eventos.Conectar(context.Background(), ticket); !errors.Is(err, ports.ErrTicketInvalido) {
		t.Errorf("Con la sesión cerrada el ticket no debe abrir el stream, vino %v", err)
	}
}

// ==========================================
// Distribución de eventos por rol y permisos
// ==========================================

func TestEventos_AdminRecibeTodo(t *testing.T) {
	e := nuevoEntornoEventos(t, "ADMIN")
	conexion, _ := e.conectar(t)
	for _, ev := range []ports.RealtimeEvent{eventoDe(t, ports.RecursoVM, "110"), eventoDe(t, ports.RecursoNodo, "proxmox")} {
		_ = e.eventos.Publicar(context.Background(), ev)
		recibeEvento(t, conexion, ev)
	}
}

func TestEventos_OperatorSoloSusInstanciasYEnVivo(t *testing.T) {
	e := nuevoEntornoEventos(t, "OPERATOR")
	e.permisos.permisos[110] = true
	conexion, _ := e.conectar(t)
	ctx := context.Background()

	propio := eventoDe(t, ports.RecursoVM, "110")
	_ = e.eventos.Publicar(ctx, propio)
	recibeEvento(t, conexion, propio)

	_ = e.eventos.Publicar(ctx, eventoDe(t, ports.RecursoLXC, "201")) // sin permiso
	_ = e.eventos.Publicar(ctx, eventoDe(t, ports.RecursoNodo, "proxmox"))
	noRecibeNada(t, conexion)

	// Revocación en vivo: le sacan el permiso sobre la 110 y deja de recibir sus eventos.
	e.permisos.mu.Lock()
	e.permisos.permisos[110] = false
	e.permisos.mu.Unlock()
	_ = e.eventos.Publicar(ctx, eventoDe(t, ports.RecursoVM, "110"))
	noRecibeNada(t, conexion)
}

// ==========================================
// Desconexión reactiva (DoD: logout corta el stream al instante)
// ==========================================

func TestCorte_LogoutCortaSoloElStreamDeEsaSesion(t *testing.T) {
	e := nuevoEntornoEventos(t, "ADMIN")
	conexion1, tokens1 := e.conectar(t)
	conexion2, _ := e.conectar(t) // otro navegador del mismo usuario

	if err := e.svc.CerrarSesion(context.Background(), tokens1.RefreshToken); err != nil {
		t.Fatal(err)
	}
	seCorta(t, conexion1, services.MotivoLogout)

	// El otro navegador sigue recibiendo.
	ev := eventoDe(t, ports.RecursoVM, "110")
	_ = e.eventos.Publicar(context.Background(), ev)
	recibeEvento(t, conexion2, ev)

	cierre := e.auditoria.buscar(ports.AccionEventosCierre)
	if cierre == nil || cierre.Detalles["motivo"] != services.MotivoLogout || cierre.Detalles["sesion_id"] != conexion1.SesionID.String() {
		t.Errorf("La auditoría debe registrar el corte con sesión y motivo: %+v", cierre)
	}
	if conexionAud := e.auditoria.buscar(ports.AccionEventosConexion); conexionAud == nil || conexionAud.Detalles["sesion_id"] == nil {
		t.Errorf("La auditoría debe registrar la conexión con la sesión: %+v", conexionAud)
	}
}

func TestCorte_RevocarSesionesCortaTodosLosStreamsDelUsuario(t *testing.T) {
	e := nuevoEntornoEventos(t, "ADMIN")
	conexion1, _ := e.conectar(t)
	conexion2, _ := e.conectar(t)
	if err := e.svc.RevocarSesionesUsuario(context.Background(), e.repo.usuario.ID); err != nil {
		t.Fatal(err)
	}
	seCorta(t, conexion1, services.MotivoSesionesRevocadas)
	seCorta(t, conexion2, services.MotivoSesionesRevocadas)
}

func TestCorte_UsuarioDesactivadoSeCortaEnElSiguienteEvento(t *testing.T) {
	e := nuevoEntornoEventos(t, "ADMIN")
	conexion, _ := e.conectar(t)
	e.repo.mu.Lock()
	e.repo.usuario.Activo = false
	e.repo.mu.Unlock()
	_ = e.eventos.Publicar(context.Background(), eventoDe(t, ports.RecursoVM, "110"))
	seCorta(t, conexion, services.MotivoUsuarioInactivo)
}

func TestCorte_ClienteQueCierraNoRompeNada(t *testing.T) {
	e := nuevoEntornoEventos(t, "ADMIN")
	conexion, _ := e.conectar(t)
	conexion.Cerrar()
	conexion.Cerrar() // dos veces no rompe
	_ = e.eventos.Publicar(context.Background(), eventoDe(t, ports.RecursoVM, "110"))
	if e.auditoria.buscar(ports.AccionEventosCierre) != nil {
		t.Error("Si el cliente cierra por su cuenta no es un corte del backend")
	}
}
