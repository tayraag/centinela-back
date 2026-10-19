package services_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"

	"github.com/google/uuid"
)

// purgerEnMemoria simula la purga atómica de permisos en BD y el registro de auditoría.
type purgerEnMemoria struct {
	mu            sync.Mutex
	permisos      map[int]map[uuid.UUID]string // vmid -> usuarioID -> nivel
	auditorias    []ports.RegistrarAuditoriaInput
	purgadoLlamado bool
}

func nuevoPurgerEnMemoria() *purgerEnMemoria {
	return &purgerEnMemoria{
		permisos: make(map[int]map[uuid.UUID]string),
	}
}

func (p *purgerEnMemoria) asignar(vmid int, usuarioID uuid.UUID, nivel string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.permisos[vmid]; !ok {
		p.permisos[vmid] = make(map[uuid.UUID]string)
	}
	p.permisos[vmid][usuarioID] = nivel
}

func (p *purgerEnMemoria) tienePermiso(vmid int, usuarioID uuid.UUID) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if usuarios, ok := p.permisos[vmid]; ok {
		_, tiene := usuarios[usuarioID]
		return tiene
	}
	return false
}

func (p *purgerEnMemoria) VerificarAcceso(_ context.Context, userID uuid.UUID, vmid int, _ string) (bool, error) {
	return p.tienePermiso(vmid, userID), nil
}

func (p *purgerEnMemoria) PurgarPermisosPorVmid(_ context.Context, vmid int, recursoTipo string, actorID *uuid.UUID) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.purgadoLlamado = true

	var usuariosAfectados []string
	if usuarios, ok := p.permisos[vmid]; ok {
		for uID := range usuarios {
			usuariosAfectados = append(usuariosAfectados, uID.String())
		}
		delete(p.permisos, vmid)
	}

	var actor uuid.UUID
	if actorID != nil {
		actor = *actorID
	}

	if recursoTipo == "" {
		recursoTipo = ports.RecursoVM
	}

	detalles := map[string]any{
		"vmid":               vmid,
		"usuarios_afectados": usuariosAfectados,
		"resource_type":      recursoTipo,
	}

	p.auditorias = append(p.auditorias, ports.RegistrarAuditoriaInput{
		UsuarioID:   actor,
		Accion:      ports.AccionInstancePermissionsPurged,
		InstanciaID: strconv.Itoa(vmid),
		Resultado:   "SUCCESS",
		Detalles:    detalles,
	})

	return usuariosAfectados, nil
}

// TestReciclajeVMID_SecuenciaEstrictaDestruccionYPurga verifica los Criterios de Aceptación (DoD):
// 1. Paso 1 (Emisión Previa de Evento): El operador recibe TASK_FINISHED por SSE antes de la purga.
// 2. Paso 2 (Transacción Atómica de Purga en BD): Tras la eliminación completada de la máquina 110,
//    los permisos vinculados a 110 se eliminan y no están disponibles.
// 3. Se genera la traza en auditoría con acción "INSTANCE_PERMISSIONS_PURGED" detallando los usuarios desvinculados.
// 4. Al aprovisionar una nueva máquina que reutilice el VMID 110, el operador no la visualiza ni tiene acceso.
func TestReciclajeVMID_SecuenciaEstrictaDestruccionYPurga(t *testing.T) {
	// 1. Configurar entorno: operador con permisos sobre VMID 110
	e := nuevoEntornoEventos(t, "OPERATOR")
	operadorID := e.repo.usuario.ID

	purger := nuevoPurgerEnMemoria()
	purger.asignar(110, operadorID, ports.NivelAccesoFullAccess)
	purger.asignar(120, operadorID, ports.NivelAccesoReadOnly) // otra máquina que debe preservarse

	// Recrear eventosService usando el purger como instanceRepo
	eventosSvc := services.NewEventosService(e.kv, e.repo, purger, e.auditoria)

	// Conectar operador a SSE con login y sesión válidos
	tokens, _ := e.login(t)
	c := claimsDe(t, tokens.AccessToken)
	ticket, err := eventosSvc.EmitirTicket(context.Background(), operadorID, uuid.MustParse(c.SesionID))
	if err != nil {
		t.Fatalf("Error al emitir ticket: %v", err)
	}
	conexion, err := eventosSvc.Conectar(context.Background(), ticket)
	if err != nil {
		t.Fatalf("Error al conectar SSE: %v", err)
	}
	defer conexion.Cerrar()

	// Proxmox simulado para la tarea de eliminación (delete) de VM 110
	px := &proxmoxTareas{vueltas: 1, exitStatus: "OK", tipo: "qemu"}
	repoTareas := &tareasEnMemoria{tareas: map[uuid.UUID]*domain.TareaAsincrona{}}

	adminID := uuid.New()
	cfg := services.ConfigSeguimiento{
		Intervalo:      10 * time.Millisecond,
		PermisosPurger: purger,
		OnTaskFinished: func(ctx context.Context, tarea *domain.TareaAsincrona, recursoTipo string, r services.ResultadoTarea) {
			// Paso 1 (Emisión Previa de Evento): Publicar primero el evento TASK_FINISHED en Redis Pub/Sub
			resultado := ports.ResultadoExito
			if r.Estado != ports.TareaCompleted {
				resultado = ports.ResultadoFalla
			}
			e.auditoria.Registrar(ctx, ports.RegistrarAuditoriaInput{
				UsuarioID:   tarea.UsuarioID,
				Accion:      strings.ToUpper(tarea.Accion),
				InstanciaID: tarea.InstanciaID,
				Resultado:   resultado,
				Detalles:    r.Detalles(tarea),
			})
			if err := eventosSvc.Publicar(ctx, services.EventoTareaFinalizada(tarea, recursoTipo, r)); err != nil {
				t.Errorf("Error al publicar evento: %v", err)
			}
		},
	}

	seg := services.NewSeguimientoTareas(px, repoTareas, services.NewAuditService(nil), cfg)
	ctxPool, cancelPool := context.WithCancel(context.Background())
	defer cancelPool()
	seg.Iniciar(ctxPool)

	// 2. Ejecutar la tarea de destrucción de la VM 110
	tareaID, err := seg.Seguir(context.Background(), adminID, 110, "delete", "UPID:proxmox:0001:0002:0003:qmdelete:110:centinela-api@pve!backend-token:")
	if err != nil {
		t.Fatalf("Error al registrar tarea: %v", err)
	}

	// 3. DoD Criterio 4: El operador DEBE recibir el evento TASK_FINISHED por SSE
	// porque el evento fue emitido antes de purgar los permisos.
	select {
	case ev := <-conexion.Eventos:
		if ev.Tipo != ports.EventoTareaFinalizada {
			t.Errorf("Se esperaba evento TASK_FINISHED, recibido %s", ev.Tipo)
		}
		if ev.RecursoID != "110" {
			t.Errorf("El evento debía ser de la máquina 110, recibido %s", ev.RecursoID)
		}
		detallesCrudos, _ := json.Marshal(ev.Detalles)
		var det map[string]any
		_ = json.Unmarshal(detallesCrudos, &det)
		if det["tareaId"] != tareaID.String() {
			t.Errorf("tareaId esperado %s, obtenido %v", tareaID, det["tareaId"])
		}
		if det["estado"] != ports.TareaCompleted {
			t.Errorf("estado esperado %s, obtenido %v", ports.TareaCompleted, det["estado"])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("El operador no recibió el evento TASK_FINISHED por SSE antes de la revocación")
	}

	// Esperar que la tarea termine y se complete la purga atómica
	time.Sleep(100 * time.Millisecond)

	// 4. DoD Criterio 1: Tras completarse la eliminación de la máquina 110,
	// los permisos del operador sobre 110 ya no existen.
	tieneAcceso110, err := purger.VerificarAcceso(context.Background(), operadorID, 110, ports.NivelAccesoReadOnly)
	if err != nil {
		t.Fatalf("Error al verificar acceso: %v", err)
	}
	if tieneAcceso110 {
		t.Errorf("VULNERABILIDAD: El operador aún conserva permisos sobre el VMID 110 tras su eliminación")
	}

	// La otra máquina (120) no debe haber sido afectada
	tieneAcceso120, _ := purger.VerificarAcceso(context.Background(), operadorID, 120, ports.NivelAccesoReadOnly)
	if !tieneAcceso120 {
		t.Errorf("El permiso sobre VMID 120 no debía ser eliminado")
	}

	// 5. DoD Criterio 2: Al aprovisionar una nueva máquina simulada que reutilice el VMID 110,
	// el operador NO tiene acceso y ningún evento nuevo de esa máquina le llega.
	_ = eventosSvc.Publicar(context.Background(), ports.RealtimeEvent{
		Tipo:        ports.EventoInstanciaEstado,
		RecursoTipo: ports.RecursoVM,
		RecursoID:   "110",
		Severidad:   ports.SeveridadInfo,
		Mensaje:     "Nueva máquina 110 encendida",
	})
	select {
	case ev := <-conexion.Eventos:
		t.Fatalf("VULNERABILIDAD: El operador recibió un evento de una máquina reutilizada 110: %+v", ev)
	case <-time.After(200 * time.Millisecond):
		// Correcto: no recibió nada
	}

	// 6. DoD Criterio 3: Se generó la traza en auditoría con acción "INSTANCE_PERMISSIONS_PURGED"
	// detallando los IDs de los usuarios desvinculados.
	purger.mu.Lock()
	defer purger.mu.Unlock()
	var trazaPurga *ports.RegistrarAuditoriaInput
	for i := range purger.auditorias {
		if purger.auditorias[i].Accion == ports.AccionInstancePermissionsPurged {
			trazaPurga = &purger.auditorias[i]
			break
		}
	}
	if trazaPurga == nil {
		t.Fatalf("No se encontró traza de auditoría con acción %s", ports.AccionInstancePermissionsPurged)
	}
	if trazaPurga.InstanciaID != "110" {
		t.Errorf("InstanciaID esperado '110', obtenido '%s'", trazaPurga.InstanciaID)
	}
	if trazaPurga.Resultado != "SUCCESS" {
		t.Errorf("Resultado esperado 'SUCCESS', obtenido '%s'", trazaPurga.Resultado)
	}
	afectados, ok := trazaPurga.Detalles["usuarios_afectados"].([]string)
	if !ok || len(afectados) != 1 || afectados[0] != operadorID.String() {
		t.Errorf("Detalle usuarios_afectados no coincide: esperado [%s], obtenido %v", operadorID, trazaPurga.Detalles["usuarios_afectados"])
	}
}

// TestReciclajeVMID_FalloDeBorradoConservaPermisos verifica el Criterio 5 (DoD):
// Si la tarea de destrucción termina con FAILED (error en Proxmox o timeout),
// NO se eliminan los registros de permisos_instancia para no desincronizar
// el estado de una máquina que sigue viva físicamente.
func TestReciclajeVMID_FalloDeBorradoConservaPermisos(t *testing.T) {
	entornoAuth := nuevoEntornoAuth(t)
	operadorID := entornoAuth.repo.usuario.ID

	purger := nuevoPurgerEnMemoria()
	purger.asignar(110, operadorID, ports.NivelAccesoFullAccess)

	// Simular fallo de Proxmox al eliminar (ej. timeout o error de bloqueo)
	px := &proxmoxTareas{vueltas: 0, exitStatus: "TASK ERROR: VM 110 is locked (backup)", tipo: "qemu"}
	repoTareas := &tareasEnMemoria{tareas: map[uuid.UUID]*domain.TareaAsincrona{}}

	eventoPublicado := make(chan ports.RealtimeEvent, 1)
	cfg := services.ConfigSeguimiento{
		Intervalo:      10 * time.Millisecond,
		PermisosPurger: purger,
		OnTaskFinished: func(ctx context.Context, tarea *domain.TareaAsincrona, recursoTipo string, r services.ResultadoTarea) {
			eventoPublicado <- services.EventoTareaFinalizada(tarea, recursoTipo, r)
		},
	}

	seg := services.NewSeguimientoTareas(px, repoTareas, services.NewAuditService(nil), cfg)
	ctxPool, cancelPool := context.WithCancel(context.Background())
	defer cancelPool()
	seg.Iniciar(ctxPool)

	adminID := uuid.New()
	_, err := seg.Seguir(context.Background(), adminID, 110, "delete", "UPID:proxmox:0001:0002:0003:qmdelete:110:centinela-api@pve!backend-token:")
	if err != nil {
		t.Fatalf("Error al registrar tarea: %v", err)
	}

	// Esperar que la tarea finalice como fallida
	select {
	case ev := <-eventoPublicado:
		if ev.Severidad != ports.SeveridadWarning {
			t.Errorf("Severidad esperada WARNING por fallo, obtenida %s", ev.Severidad)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Timeout esperando finalización de tarea fallida")
	}

	time.Sleep(50 * time.Millisecond)

	// DoD Criterio 5: Purga NO debe haberse ejecutado; permisos deben permanecer intactos.
	if purger.purgadoLlamado {
		t.Errorf("ERROR: PurgarPermisosPorVmid fue llamado a pesar de que la tarea falló!")
	}
	tieneAcceso, err := purger.VerificarAcceso(context.Background(), operadorID, 110, ports.NivelAccesoFullAccess)
	if err != nil || !tieneAcceso {
		t.Errorf("Los permisos sobre 110 debían permanecer intactos tras un fallo de Proxmox")
	}
}

// TestReciclajeVMID_AccionDistintaDeDeleteNoPurga verifica que acciones como start/stop
// no disparen la purga de permisos aunque terminen con éxito.
func TestReciclajeVMID_AccionDistintaDeDeleteNoPurga(t *testing.T) {
	operadorID := uuid.New()
	purger := nuevoPurgerEnMemoria()
	purger.asignar(110, operadorID, ports.NivelAccesoFullAccess)

	px := &proxmoxTareas{vueltas: 0, exitStatus: "OK", tipo: "qemu"}
	repoTareas := &tareasEnMemoria{tareas: map[uuid.UUID]*domain.TareaAsincrona{}}

	finalizada := make(chan struct{}, 1)
	cfg := services.ConfigSeguimiento{
		Intervalo:      10 * time.Millisecond,
		PermisosPurger: purger,
		OnTaskFinished: func(_ context.Context, _ *domain.TareaAsincrona, _ string, _ services.ResultadoTarea) {
			finalizada <- struct{}{}
		},
	}

	seg := services.NewSeguimientoTareas(px, repoTareas, services.NewAuditService(nil), cfg)
	ctxPool, cancelPool := context.WithCancel(context.Background())
	defer cancelPool()
	seg.Iniciar(ctxPool)

	_, err := seg.Seguir(context.Background(), uuid.New(), 110, "start", "UPID:proxmox:0001:0002:0003:qmstart:110:centinela-api@pve!backend-token:")
	if err != nil {
		t.Fatalf("Error al registrar tarea: %v", err)
	}

	select {
	case <-finalizada:
	case <-time.After(3 * time.Second):
		t.Fatal("Timeout esperando finalización de tarea start")
	}

	time.Sleep(50 * time.Millisecond)

	if purger.purgadoLlamado {
		t.Errorf("ERROR: PurgarPermisosPorVmid no debe ejecutarse para la acción 'start'")
	}
	tieneAcceso, _ := purger.VerificarAcceso(context.Background(), operadorID, 110, ports.NivelAccesoFullAccess)
	if !tieneAcceso {
		t.Errorf("Los permisos sobre 110 no debían cambiar con acción 'start'")
	}
}
