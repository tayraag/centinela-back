package services_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/proxmox"
	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

// proxmoxSnapshots es un Proxmox falso con la instancia 110 y sus snapshots.
type proxmoxSnapshots struct {
	proxmoxTareas
	snapshots  []ports.SnapshotDTO
	creados    []string
	errCrear   error
	tipoPedido string
}

func (p *proxmoxSnapshots) ObtenerInstancia(_ context.Context, vmid int) (*ports.InstanciaProxmoxDTO, error) {
	return &ports.InstanciaProxmoxDTO{Vmid: vmid, Nombre: "srv-110", Tipo: "qemu", Nodo: "pve1", Estado: "running"}, nil
}

func (p *proxmoxSnapshots) ListarSnapshots(_ context.Context, _, tipo string, _ int) ([]ports.SnapshotDTO, error) {
	p.tipoPedido = tipo
	return append([]ports.SnapshotDTO(nil), p.snapshots...), nil
}

func (p *proxmoxSnapshots) CrearSnapshot(_ context.Context, _, _ string, _ int, nombre, _ string) (string, error) {
	if p.errCrear != nil {
		return "", p.errCrear
	}
	p.creados = append(p.creados, nombre)
	return "UPID:pve1:0001:snapshot", nil
}

func (p *proxmoxSnapshots) RevertirSnapshot(_ context.Context, _, _ string, _ int, nombre string) (string, error) {
	p.creados = append(p.creados, "rollback:"+nombre)
	return "UPID:pve1:0002:qmrollback", p.errCrear
}

func (p *proxmoxSnapshots) EliminarSnapshot(_ context.Context, _, _ string, _ int, nombre string) (string, error) {
	p.creados = append(p.creados, "delete:"+nombre)
	return "UPID:pve1:0003:qmdelsnapshot", p.errCrear
}

// seguimientoSnapshots registra lo que se le pide seguir.
type seguimientoSnapshots struct {
	accion, upid string
	metadatos    map[string]any
	err          error
}

func (s *seguimientoSnapshots) Seguir(_ context.Context, _ uuid.UUID, _ int, accion, upid string, metadatos ...map[string]any) (uuid.UUID, error) {
	if s.err != nil {
		return uuid.Nil, s.err
	}
	s.accion, s.upid = accion, upid
	if len(metadatos) > 0 {
		s.metadatos = metadatos[0]
	}
	return uuid.Must(uuid.NewV7()), nil
}

func TestSnapshots_CreacionValida(t *testing.T) {
	px, seg := &proxmoxSnapshots{}, &seguimientoSnapshots{}
	creado, err := services.NewSnapshotService(px, seg, auditoriaNula{}).Crear(context.Background(), uuid.New(), 110, "snap_pre-upgrade", "  Punto previo  ")
	if err != nil || creado == nil || creado.Nombre != "snap_pre-upgrade" || creado.TareaID == uuid.Nil {
		t.Fatalf("Creación válida: %+v %v", creado, err)
	}
	if len(px.creados) != 1 || seg.accion != ports.AccionTareaSnapshot || seg.upid != "UPID:pve1:0001:snapshot" || seg.metadatos["snapname"] != "snap_pre-upgrade" {
		t.Errorf("Debe despachar en Proxmox y registrar la tarea con su UPID: creados=%v seguimiento=%+v", px.creados, seg)
	}
}

// DoD: nombres con espacios o caracteres especiales se rechazan sin contactar a Proxmox.
func TestSnapshots_RechazaNombresInvalidos(t *testing.T) {
	for _, nombre := range []string{"snap test!", "snap test", "", "a", "1snap", "_snap", "snap,vmstate=1", "snap/x", "current", "Current",
		"snap\nx", strings.Repeat("a", 41), "señal"} {
		px := &proxmoxSnapshots{}
		_, err := services.NewSnapshotService(px, &seguimientoSnapshots{}, auditoriaNula{}).Crear(context.Background(), uuid.New(), 110, nombre, "")
		if !errors.Is(err, ports.ErrSnapshotNombreInvalido) || len(px.creados) != 0 {
			t.Errorf("%q: se esperaba ErrSnapshotNombreInvalido sin llegar a Proxmox, vino %v (creados=%v)", nombre, err, px.creados)
		}
	}
	for _, nombre := range []string{"ab", "snap_1", "Pre-Upgrade", strings.Repeat("a", 40)} {
		if !services.NombreSnapshotValido(nombre) {
			t.Errorf("%q debería ser válido", nombre)
		}
	}
}

func TestSnapshots_RechazaRepetidoYDescripcionLarga(t *testing.T) {
	px := &proxmoxSnapshots{snapshots: []ports.SnapshotDTO{{Nombre: "existente", SnapTime: 1}}}
	svc := services.NewSnapshotService(px, &seguimientoSnapshots{}, auditoriaNula{})
	if _, err := svc.Crear(context.Background(), uuid.New(), 110, "existente", ""); !errors.Is(err, ports.ErrSnapshotExistente) {
		t.Errorf("Nombre repetido: se esperaba ErrSnapshotExistente, vino %v", err)
	}
	if _, err := svc.Crear(context.Background(), uuid.New(), 110, "nuevo", strings.Repeat("x", 256)); !errors.Is(err, ports.ErrSnapshotDescripcionInvalida) {
		t.Errorf("Descripción larga: se esperaba ErrSnapshotDescripcionInvalida, vino %v", err)
	}
	if len(px.creados) != 0 {
		t.Errorf("No debe llegar a Proxmox: %v", px.creados)
	}
}

func TestSnapshots_SeguimientoCaidoSeInforma(t *testing.T) {
	px := &proxmoxSnapshots{}
	_, err := services.NewSnapshotService(px, &seguimientoSnapshots{err: errors.New("base caída")}, auditoriaNula{}).Crear(context.Background(), uuid.New(), 110, "snap1", "")
	if !errors.Is(err, ports.ErrSeguimientoNoRegistrado) || len(px.creados) != 1 {
		t.Errorf("Se esperaba ErrSeguimientoNoRegistrado después de despachar: %v", err)
	}
}

func TestSnapshots_ListadoDelMasNuevoAlMasViejo(t *testing.T) {
	px := &proxmoxSnapshots{snapshots: []ports.SnapshotDTO{
		{Nombre: "primero", SnapTime: 100}, {Nombre: "tercero", SnapTime: 300, Padre: "segundo"}, {Nombre: "segundo", SnapTime: 200, Padre: "primero"},
	}}
	lista, err := services.NewSnapshotService(px, &seguimientoSnapshots{}, auditoriaNula{}).Listar(context.Background(), 110)
	if err != nil || len(lista) != 3 || lista[0].Nombre != "tercero" || lista[1].Nombre != "segundo" || lista[2].Nombre != "primero" {
		t.Errorf("Orden descendente por snaptime: %+v %v", lista, err)
	}
	if px.tipoPedido != "qemu" {
		t.Errorf("Debe consultar con el tipo de la instancia: %q", px.tipoPedido)
	}
}

// clienteSimulador devuelve un cliente contra el simulador de Proxmox del
// .env. Solo corre con una URL local: contra un Proxmox real este test crearía
// snapshots de verdad.
func clienteSimulador(t *testing.T) *proxmox.Client {
	t.Helper()
	_ = godotenv.Load("../../../.env")
	direccion := os.Getenv("PROXMOX_URL")
	u, err := url.Parse(direccion)
	if direccion == "" || err != nil || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") {
		t.Skip("Sin simulador local de Proxmox en PROXMOX_URL: no se prueba contra un Proxmox real")
	}
	client := proxmox.NewClient(direccion, os.Getenv("PROXMOX_TOKEN_ID"), os.Getenv("PROXMOX_TOKEN_SECRET"), proxmox.BuildTLSConfig())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := client.ListarInstancias(ctx); err != nil {
		t.Skipf("El simulador no responde: %v", err)
	}
	return client
}

// Contra el simulador: crea un snapshot en una VM (110) y en un LXC (201) y
// los encuentra en el listado, ordenado y sin la entrada "current".
func TestSnapshots_ContraElSimulador(t *testing.T) {
	client := clienteSimulador(t)
	svc := services.NewSnapshotService(client, &seguimientoSnapshots{}, auditoriaNula{})
	for _, vmid := range []int{110, 201} {
		nombre := "e2e_" + uuid.NewString()[:8]
		if _, err := svc.Crear(context.Background(), uuid.New(), vmid, nombre, "Prueba automática"); err != nil {
			t.Fatalf("%d: crear: %v", vmid, err)
		}
		var lista []ports.SnapshotDTO
		for intento := 0; intento < 40; intento++ { // la tarea del simulador tarda unos segundos
			var err error
			if lista, err = svc.Listar(context.Background(), vmid); err != nil {
				t.Fatalf("%d: listar: %v", vmid, err)
			}
			if len(lista) > 0 && lista[0].Nombre == nombre {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if len(lista) == 0 || lista[0].Nombre != nombre || lista[0].Descripcion != "Prueba automática" || lista[0].VMState {
			t.Fatalf("%d: el snapshot nuevo debe ser el primero del listado: %+v", vmid, lista)
		}
		for i, s := range lista {
			if s.Nombre == "current" {
				t.Errorf("%d: no debe incluir la entrada current", vmid)
			}
			if i > 0 && lista[i-1].SnapTime < s.SnapTime {
				t.Errorf("%d: listado fuera de orden: %+v", vmid, lista)
			}
		}
		if len(lista) > 1 && lista[0].Padre != lista[1].Nombre {
			t.Errorf("%d: el nuevo debe colgar del anterior: %+v", vmid, lista[:2])
		}
	}
}

// Si Proxmox rechaza la creación, queda auditada como falla y no se registra tarea.
func TestSnapshots_FallaDeProxmoxSeAudita(t *testing.T) {
	px, seg, audit := &proxmoxSnapshots{errCrear: ports.ErrProxmoxNoDisponible}, &seguimientoSnapshots{}, &auditoriaGrabadora{}
	_, err := services.NewSnapshotService(px, seg, audit).Crear(context.Background(), uuid.New(), 110, "snap1", "")
	if !errors.Is(err, ports.ErrProxmoxNoDisponible) || seg.upid != "" {
		t.Fatalf("Se esperaba el error de Proxmox sin tarea registrada: %v %+v", err, seg)
	}
	registro := audit.buscar(ports.AccionCrearSnapshot)
	if registro == nil || registro.Resultado != ports.ResultadoFailed || registro.InstanciaID != "110" || registro.Detalles["snapname"] != "snap1" {
		t.Errorf("Debe auditarse la falla: %+v", registro)
	}
}

// La tarea de snapshot se audita como CREAR_SNAPSHOT (con el nombre) y su
// TASK_FINISHED llega con accion SNAPSHOT.
func TestSnapshots_AuditoriaYEventoDeLaTarea(t *testing.T) {
	meta := `{"instanciaNombre":"srv-110","resource_type":"qemu","snapname":"snap1"}`
	tarea := &domain.TareaAsincrona{ID: uuid.New(), UsuarioID: uuid.New(), Accion: ports.AccionTareaSnapshot, InstanciaID: "110",
		UpidProxmox: "UPID:pve1:0001:qmsnapshot", FechaCreacion: time.Now(), Metadatos: &meta}

	inicio := services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, nil, time.Now())
	if inicio.Accion != ports.AccionCrearSnapshot || inicio.Resultado != ports.ResultadoPendiente || inicio.Detalles["snapname"] != "snap1" {
		t.Errorf("Auditoría inicial: %+v", inicio)
	}
	fin := services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, map[string]any{"exitstatus": "OK", "motivo": nil}, time.Now())
	if fin.Accion != ports.AccionCrearSnapshot || fin.Resultado != ports.ResultadoSuccess {
		t.Errorf("Auditoría de cierre: %+v", fin)
	}

	evento := services.EventoTareaFinalizada(tarea, ports.RecursoVM, services.ResultadoTarea{Estado: ports.TareaCompleted, ExitStatus: "OK"})
	if evento.Detalles["accion"] != "SNAPSHOT" || evento.Mensaje != "La tarea de creación del snapshot finalizó correctamente" {
		t.Errorf("Evento: %+v", evento)
	}
	if ports.AccionPublicaTarea(tarea.Accion) != "SNAPSHOT" {
		t.Errorf("activeTask.action: %q", ports.AccionPublicaTarea(tarea.Accion))
	}
}

func TestSnapshots_RevertirYEliminar(t *testing.T) {
	casos := []struct {
		accion, upid string
		orden        func(svc ports.SnapshotService) (uuid.UUID, error)
	}{
		{ports.AccionTareaRollbackSnapshot, "UPID:pve1:0002:qmrollback", func(svc ports.SnapshotService) (uuid.UUID, error) {
			return svc.Revertir(context.Background(), uuid.New(), 110, "base")
		}},
		{ports.AccionTareaDeleteSnapshot, "UPID:pve1:0003:qmdelsnapshot", func(svc ports.SnapshotService) (uuid.UUID, error) {
			return svc.Eliminar(context.Background(), uuid.New(), 110, "base")
		}},
	}
	for _, c := range casos {
		px, seg := &proxmoxSnapshots{snapshots: []ports.SnapshotDTO{{Nombre: "base", SnapTime: 1}}}, &seguimientoSnapshots{}
		tareaID, err := c.orden(services.NewSnapshotService(px, seg, auditoriaNula{}))
		if err != nil || tareaID == uuid.Nil || len(px.creados) != 1 {
			t.Fatalf("%s: %v %v", c.accion, err, px.creados)
		}
		if seg.accion != c.accion || seg.upid != c.upid || seg.metadatos["snapname"] != "base" {
			t.Errorf("%s: la tarea debe registrarse con su acción y UPID: %+v", c.accion, seg)
		}
	}
}

func TestSnapshots_RevertirValidaNombreYExistencia(t *testing.T) {
	px := &proxmoxSnapshots{snapshots: []ports.SnapshotDTO{{Nombre: "base", SnapTime: 1}}}
	svc := services.NewSnapshotService(px, &seguimientoSnapshots{}, auditoriaNula{})
	if _, err := svc.Revertir(context.Background(), uuid.New(), 110, "snap test!"); !errors.Is(err, ports.ErrSnapshotNombreInvalido) {
		t.Errorf("Nombre inválido: %v", err)
	}
	if _, err := svc.Eliminar(context.Background(), uuid.New(), 110, "otro"); !errors.Is(err, ports.ErrSnapshotNoEncontrado) {
		t.Errorf("Inexistente: %v", err)
	}
	if len(px.creados) != 0 {
		t.Errorf("No debe llegar a Proxmox: %v", px.creados)
	}
}

// Contra el simulador: revierte al snapshot y después lo borra.
func TestSnapshots_RevertirYEliminarContraElSimulador(t *testing.T) {
	client := clienteSimulador(t)
	svc := services.NewSnapshotService(client, &seguimientoSnapshots{}, auditoriaNula{})
	nombre := "e2e_" + uuid.NewString()[:8]
	esperarTarea := func(cumple func([]ports.SnapshotDTO) bool) []ports.SnapshotDTO {
		t.Helper()
		for intento := 0; intento < 40; intento++ {
			lista, err := svc.Listar(context.Background(), 201)
			if err == nil && cumple(lista) {
				return lista
			}
			time.Sleep(250 * time.Millisecond)
		}
		t.Fatalf("La tarea del simulador no terminó a tiempo")
		return nil
	}
	tiene := func(lista []ports.SnapshotDTO) bool {
		for _, s := range lista {
			if s.Nombre == nombre {
				return true
			}
		}
		return false
	}
	if _, err := svc.Crear(context.Background(), uuid.New(), 201, nombre, ""); err != nil {
		t.Fatal(err)
	}
	esperarTarea(tiene)
	// El simulador bloquea la instancia mientras corre una tarea: se reintenta.
	var err error
	for intento := 0; intento < 20; intento++ {
		if _, err = svc.Revertir(context.Background(), uuid.New(), 201, nombre); !errors.Is(err, ports.ErrInstanciaOcupada) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("Revertir: %v", err)
	}
	for intento := 0; intento < 40; intento++ {
		if _, err = svc.Eliminar(context.Background(), uuid.New(), 201, nombre); !errors.Is(err, ports.ErrInstanciaOcupada) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("Eliminar: %v", err)
	}
	esperarTarea(func(l []ports.SnapshotDTO) bool { return !tiene(l) })
}
