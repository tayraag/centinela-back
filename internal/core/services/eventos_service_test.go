package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/memoria"
	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/domain/topology"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"

	"github.com/google/uuid"
)

// permisosFalsos implementa ports.InstanceRepository: el usuario tiene asignado
// un nivel de acceso ("FULL_ACCESS" o "READ_ONLY") sobre los vmid del mapa.
type permisosFalsos struct {
	mu       sync.Mutex
	permisos map[int]string
}

func (p *permisosFalsos) VerificarAcceso(_ context.Context, _ uuid.UUID, vmid int, nivelRequerido string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	nivel, existe := p.permisos[vmid]
	if !existe {
		return false, nil
	}
	if nivelRequerido == ports.NivelAccesoFullAccess {
		return nivel == ports.NivelAccesoFullAccess, nil
	}
	return nivel == ports.NivelAccesoFullAccess || nivel == ports.NivelAccesoReadOnly, nil
}

func (p *permisosFalsos) PurgarPermisosPorVmid(_ context.Context, vmid int, _ string, _ *uuid.UUID) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.permisos, vmid)
	return []string{}, nil
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
		permisos:    &permisosFalsos{permisos: map[int]string{}},
		auditoria:   &auditoriaGrabadora{},
		avanzar:     func(d time.Duration) { mu.Lock(); reloj = reloj.Add(d); mu.Unlock() },
	}
	e.eventos = services.NewEventosService(kv, base.repo, e.permisos, e.auditoria, topologiaPrueba())
	return e
}

// topologiaPrueba: nodo "pve1" como "nodo-principal" y los storages por defecto.
func topologiaPrueba() *topology.Catalogo {
	c, err := topology.NuevoCatalogo(map[string]string{"pve1": topology.AliasNodoPorDefecto}, topology.AliasStoragePorDefecto, "secreto-de-prueba")
	if err != nil {
		panic(err)
	}
	return c
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

// alertaDelHost es un ALERT_TRIGGERED del servidor físico: sin recursoId.
func alertaDelHost(t *testing.T) ports.RealtimeEvent {
	t.Helper()
	ev, err := ports.NewRealtimeEvent(ports.EventoAlerta, ports.SeveridadCritical, "El uso de CPU del host superó el 90%")
	if err != nil {
		t.Fatal(err)
	}
	return ev.ConRecurso(ports.RecursoHost, "").ConDetalles(map[string]any{"metrica": "cpu", "valor": 92.4, "umbral": 90})
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
	for _, ev := range []ports.RealtimeEvent{eventoDe(t, ports.RecursoVM, "110"), alertaDelHost(t)} {
		_ = e.eventos.Publicar(context.Background(), ev)
		recibeEvento(t, conexion, ev)
	}
}

func TestEventos_OperatorSoloSusInstanciasYEnVivo(t *testing.T) {
	e := nuevoEntornoEventos(t, "OPERATOR")
	e.permisos.permisos[110] = ports.NivelAccesoReadOnly
	conexion, _ := e.conectar(t)
	ctx := context.Background()

	propio := eventoDe(t, ports.RecursoVM, "110")
	_ = e.eventos.Publicar(ctx, propio)
	recibeEvento(t, conexion, propio)

	_ = e.eventos.Publicar(ctx, eventoDe(t, ports.RecursoLXC, "201")) // sin permiso
	_ = e.eventos.Publicar(ctx, alertaDelHost(t))
	noRecibeNada(t, conexion)

	// Revocación en vivo: le sacan el permiso sobre la 110 y deja de recibir sus eventos.
	e.permisos.mu.Lock()
	delete(e.permisos.permisos, 110)
	e.permisos.mu.Unlock()
	_ = e.eventos.Publicar(ctx, eventoDe(t, ports.RecursoVM, "110"))
	noRecibeNada(t, conexion)
}

// ALERT_TRIGGERED del HOST: llega al ADMIN y se descarta para el OPERATOR,
// aunque tenga permisos sobre instancias. El TASK_FINISHED de una de sus
// instancias le sigue llegando (retrocompatibilidad).
func TestEventos_AlertaDelHostSoloParaAdmin(t *testing.T) {
	ctx := context.Background()

	admin := nuevoEntornoEventos(t, "ADMIN")
	conAdmin, _ := admin.conectar(t)
	alerta := alertaDelHost(t)
	_ = admin.eventos.Publicar(ctx, alerta)
	recibeEvento(t, conAdmin, alerta)

	operador := nuevoEntornoEventos(t, "OPERATOR")
	operador.permisos.permisos[110] = ports.NivelAccesoFullAccess
	conOperador, _ := operador.conectar(t)
	_ = operador.eventos.Publicar(ctx, alertaDelHost(t))
	noRecibeNada(t, conOperador)
	tarea := eventoDe(t, ports.RecursoVM, "110")
	_ = operador.eventos.Publicar(ctx, tarea)
	recibeEvento(t, conOperador, tarea)
}

// El evento del HOST viaja sin recursoId (omitempty) y los de instancia lo
// mantienen.
func TestEventos_RecursoIdOpcional(t *testing.T) {
	serializar := func(ev ports.RealtimeEvent) map[string]any {
		crudo, _ := json.Marshal(ev)
		var m map[string]any
		_ = json.Unmarshal(crudo, &m)
		return m
	}
	host := serializar(alertaDelHost(t))
	if _, tiene := host["recursoId"]; tiene || host["recursoTipo"] != "HOST" || host["tipo"] != "ALERT_TRIGGERED" {
		t.Errorf("Alerta del host: %v", host)
	}
	if vm := serializar(eventoDe(t, ports.RecursoVM, "110")); vm["recursoId"] != "110" {
		t.Errorf("Un evento de VM debe conservar su recursoId: %v", vm)
	}
}

func TestEventos_TASK_FINISHED_CincoAccionesCanonicas(t *testing.T) {
	// Validar que el worker o generador despache TASK_FINISHED con las 5 acciones canónicas
	// ("START", "SHUTDOWN", "STOP", "REBOOT", "DELETE") cumpliendo el esquema unificado.
	acciones := []struct {
		accionInterna string
		accionSSE     string
		nombreMensaje string
	}{
		{"start", "START", "encendido"},
		{"shutdown", "SHUTDOWN", "apagado ordenado"},
		{"stop", "STOP", "apagado"},
		{"reboot", "REBOOT", "reinicio"},
		{"delete", "DELETE", "eliminación"},
	}

	for _, c := range acciones {
		t.Run(c.accionInterna, func(t *testing.T) {
			tareaID := uuid.New()
			tarea := &domain.TareaAsincrona{
				ID:          tareaID,
				InstanciaID: "101",
				Accion:      c.accionInterna,
			}
			res := services.ResultadoTarea{
				Estado:     ports.TareaCompleted,
				ExitStatus: "OK",
			}
			ev := services.EventoTareaFinalizada(tarea, ports.RecursoVM, res)

			if ev.Tipo != ports.EventoTareaFinalizada {
				t.Fatalf("tipo = %s, se esperaba %s", ev.Tipo, ports.EventoTareaFinalizada)
			}
			if ev.Severidad != ports.SeveridadInfo {
				t.Fatalf("severidad = %s, se esperaba INFO", ev.Severidad)
			}
			if ev.RecursoTipo != ports.RecursoVM || ev.RecursoID != "101" {
				t.Fatalf("recursoTipo/Id = %s/%s, se esperaba VM/101", ev.RecursoTipo, ev.RecursoID)
			}
			esperadoMensaje := fmt.Sprintf("La tarea de %s finalizó correctamente", c.nombreMensaje)
			if ev.Mensaje != esperadoMensaje {
				t.Fatalf("mensaje = %q, se esperaba %q", ev.Mensaje, esperadoMensaje)
			}

			// Validar las 6 claves del payload de detalles
			d := ev.Detalles
			if len(d) != 6 {
				t.Fatalf("detalles debe tener 6 claves, tiene %d: %v", len(d), d)
			}
			if d["tareaId"] != tareaID.String() {
				t.Errorf("tareaId = %v, se esperaba %s", d["tareaId"], tareaID)
			}
			if d["accion"] != c.accionSSE {
				t.Errorf("accion = %v, se esperaba %s", d["accion"], c.accionSSE)
			}
			if d["estado"] != ports.TareaCompleted {
				t.Errorf("estado = %v, se esperaba COMPLETED", d["estado"])
			}
			if d["exitstatus"] != "OK" {
				t.Errorf("exitstatus = %v, se esperaba OK", d["exitstatus"])
			}
			if d["motivo"] != nil {
				t.Errorf("motivo = %v, se esperaba nil", d["motivo"])
			}
			if d["error"] != nil {
				t.Errorf("error = %v, se esperaba nil", d["error"])
			}
		})
	}
}

func TestEventos_TASK_FINISHED_CasosDeFallo(t *testing.T) {
	// Fallo por PROXMOX_ERROR
	t.Run("fallo_proxmox_error", func(t *testing.T) {
		tareaID := uuid.New()
		tarea := &domain.TareaAsincrona{
			ID:          tareaID,
			InstanciaID: "201",
			Accion:      "stop",
		}
		res := services.ResultadoTarea{
			Estado:     ports.TareaFailed,
			ExitStatus: "CT 201 not running",
			Motivo:     ports.MotivoProxmoxError,
			Error:      "CT 201 not running",
		}
		ev := services.EventoTareaFinalizada(tarea, ports.RecursoLXC, res)

		if ev.Tipo != ports.EventoTareaFinalizada {
			t.Fatalf("tipo = %s", ev.Tipo)
		}
		if ev.Severidad != ports.SeveridadWarning {
			t.Fatalf("severidad = %s, se esperaba WARNING", ev.Severidad)
		}
		if ev.RecursoTipo != ports.RecursoLXC || ev.RecursoID != "201" {
			t.Fatalf("recursoTipo/Id = %s/%s", ev.RecursoTipo, ev.RecursoID)
		}
		if ev.Mensaje != "La tarea de apagado falló" {
			t.Fatalf("mensaje = %q", ev.Mensaje)
		}

		d := ev.Detalles
		if d["tareaId"] != tareaID.String() || d["accion"] != "STOP" || d["estado"] != ports.TareaFailed {
			t.Fatalf("identificadores incorrectos: %v", d)
		}
		if d["exitstatus"] != "CT 201 not running" {
			t.Errorf("exitstatus = %v", d["exitstatus"])
		}
		if d["motivo"] != ports.MotivoProxmoxError {
			t.Errorf("motivo = %v, se esperaba PROXMOX_ERROR", d["motivo"])
		}
		if d["error"] != "CT 201 not running" {
			t.Errorf("error = %v", d["error"])
		}
	})

	// Fallo por TIMEOUT
	t.Run("fallo_timeout", func(t *testing.T) {
		tareaID := uuid.New()
		tarea := &domain.TareaAsincrona{
			ID:          tareaID,
			InstanciaID: "100",
			Accion:      "start",
		}
		res := services.ResultadoTarea{
			Estado: ports.TareaFailed,
			Motivo: ports.MotivoTimeout,
			Error:  "Excedido el límite máximo de ejecución de 10 minutos",
		}
		ev := services.EventoTareaFinalizada(tarea, ports.RecursoVM, res)

		if ev.Severidad != ports.SeveridadWarning {
			t.Fatalf("severidad = %s, se esperaba WARNING", ev.Severidad)
		}
		d := ev.Detalles
		if d["estado"] != ports.TareaFailed {
			t.Errorf("estado = %v", d["estado"])
		}
		if d["motivo"] != ports.MotivoTimeout {
			t.Errorf("motivo = %v, se esperaba TIMEOUT", d["motivo"])
		}
		if d["exitstatus"] != nil {
			t.Errorf("exitstatus = %v, se esperaba nil", d["exitstatus"])
		}
		if d["error"] == nil || !strings.Contains(d["error"].(string), "Excedido el límite") {
			t.Errorf("error = %v, se esperaba mensaje de timeout", d["error"])
		}
	})
}

func TestEventos_TASK_FINISHED_MatrizDistribucionRBAC(t *testing.T) {
	ctx := context.Background()
	ev101 := services.EventoTareaFinalizada(&domain.TareaAsincrona{
		ID:          uuid.New(),
		InstanciaID: "101",
		Accion:      "start",
	}, ports.RecursoVM, services.ResultadoTarea{Estado: ports.TareaCompleted, ExitStatus: "OK"})

	ev202 := services.EventoTareaFinalizada(&domain.TareaAsincrona{
		ID:          uuid.New(),
		InstanciaID: "202",
		Accion:      "delete",
	}, ports.RecursoLXC, services.ResultadoTarea{Estado: ports.TareaCompleted, ExitStatus: "OK"})

	// 1. ADMIN recibe eventos de cualquier instancia
	t.Run("AdminRecibeCualquierInstancia", func(t *testing.T) {
		eAdmin := nuevoEntornoEventos(t, "ADMIN")
		conexionAdmin, _ := eAdmin.conectar(t)
		_ = eAdmin.eventos.Publicar(ctx, ev101)
		recibeEvento(t, conexionAdmin, ev101)
		_ = eAdmin.eventos.Publicar(ctx, ev202)
		recibeEvento(t, conexionAdmin, ev202)
	})

	// 2. OPERATOR con READ_ONLY recibe evento de su instancia
	t.Run("OperatorReadOnlyRecibeSuInstancia", func(t *testing.T) {
		eOpRO := nuevoEntornoEventos(t, "OPERATOR")
		eOpRO.permisos.permisos[101] = ports.NivelAccesoReadOnly
		conexionOpRO, _ := eOpRO.conectar(t)
		_ = eOpRO.eventos.Publicar(ctx, ev101)
		recibeEvento(t, conexionOpRO, ev101)

		// No debe recibir evento de la 202 (sin permiso)
		_ = eOpRO.eventos.Publicar(ctx, ev202)
		noRecibeNada(t, conexionOpRO)
	})

	// 3. OPERATOR con FULL_ACCESS recibe evento de su instancia
	t.Run("OperatorFullAccessRecibeSuInstancia", func(t *testing.T) {
		eOpFA := nuevoEntornoEventos(t, "OPERATOR")
		eOpFA.permisos.permisos[202] = ports.NivelAccesoFullAccess
		conexionOpFA, _ := eOpFA.conectar(t)
		_ = eOpFA.eventos.Publicar(ctx, ev202)
		recibeEvento(t, conexionOpFA, ev202)

		// No debe recibir evento de la 101 (sin permiso)
		_ = eOpFA.eventos.Publicar(ctx, ev101)
		noRecibeNada(t, conexionOpFA)
	})

	// 4. OPERATOR sin asignación no recibe eventos bajo ningún concepto
	t.Run("OperatorSinPermisosNoRecibeNada", func(t *testing.T) {
		eOpNone := nuevoEntornoEventos(t, "OPERATOR")
		conexionOpNone, _ := eOpNone.conectar(t)
		_ = eOpNone.eventos.Publicar(ctx, ev101)
		_ = eOpNone.eventos.Publicar(ctx, ev202)
		noRecibeNada(t, conexionOpNone)
	})
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

func TestCorte_RevocacionSelectivaConservaElStreamActual(t *testing.T) {
	e := nuevoEntornoEventos(t, "ADMIN")
	revocada, _ := e.conectar(t)
	conservada, _ := e.conectar(t)
	_ = e.kv.Publish(context.Background(), ports.CanalEventos, ports.MensajeBus{
		Tipo: ports.MensajeSesionesRevocadas, UsuarioID: &e.repo.usuario.ID, SesionID: &revocada.SesionID,
	})
	seCorta(t, revocada, services.MotivoSesionesRevocadas)
	ev := eventoDe(t, ports.RecursoVM, "110")
	_ = e.eventos.Publicar(context.Background(), ev)
	recibeEvento(t, conservada, ev)
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

// Un OPERATOR no ve nombres reales de nodos ni storages en los textos del
// evento (el error de Proxmox puede nombrarlos); un ADMIN los ve tal cual.
func TestEventos_TopologiaEnmascaradaParaOperator(t *testing.T) {
	const errorProxmox = "storage 'local-lvm' does not exist on node pve1"
	for _, rol := range []string{"OPERATOR", "ADMIN"} {
		e := nuevoEntornoEventos(t, rol)
		e.permisos.permisos[110] = ports.NivelAccesoReadOnly
		conexion, _ := e.conectar(t)
		ev := eventoDe(t, ports.RecursoVM, "110").ConDetalles(map[string]any{"exitstatus": errorProxmox, "error": errorProxmox, "motivo": ports.MotivoProxmoxError, "duracion": 3})
		_ = e.eventos.Publicar(context.Background(), ev)
		select {
		case llegado := <-conexion.Eventos:
			esperado := errorProxmox
			if rol != "ADMIN" {
				esperado = "storage 'Almacenamiento Local Rápido' does not exist on node nodo-principal"
			}
			if llegado.Detalles["exitstatus"] != esperado || llegado.Detalles["error"] != esperado {
				t.Errorf("%s: detalles = %v, se esperaba %q", rol, llegado.Detalles, esperado)
			}
			if llegado.Detalles["motivo"] != ports.MotivoProxmoxError || llegado.Detalles["duracion"] != float64(3) { // el bus viaja en JSON
				t.Errorf("%s: el resto de detalles se conserva: %v", rol, llegado.Detalles)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no llegó el evento", rol)
		}
	}
}
