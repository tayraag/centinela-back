package services_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"
	"el-centinela/internal/infrastructure/crypto"

	"github.com/google/uuid"
)

// auditoriaConFiltro recuerda el filtro que le llegó al repositorio.
type auditoriaConFiltro struct {
	*mockAuditRepository
	filtros  ports.FiltrosAuditoria
	consulto bool
}

func (a *auditoriaConFiltro) Listar(ctx context.Context, orgID uuid.UUID, f ports.FiltrosAuditoria, o ports.OpcionesAuditoria) (*ports.PaginaAuditoria, error) {
	a.filtros, a.consulto = f, true
	return a.mockAuditRepository.Listar(ctx, orgID, f, o)
}

func (a *auditoriaConFiltro) ExportarRegistros(ctx context.Context, orgID uuid.UUID, f ports.FiltrosAuditoria) ([]ports.AuditoriaDTO, error) {
	a.filtros, a.consulto = f, true
	return a.mockAuditRepository.ExportarRegistros(ctx, orgID, f)
}

// Los ids son fijos: los tests buscan "110" y "201" en las exportaciones y un
// UUID aleatorio podría contenerlos.
func auditoriaConVmids() *auditoriaConFiltro {
	repo := newMockAuditRepository()
	repo.registros = []ports.AuditoriaDTO{
		{ID: uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1"), Accion: ports.AccionIniciarVM, InstanciaID: "110", RecursoTipo: ports.RecursoVM, RecursoID: "110",
			Detalles: json.RawMessage(`{"error":"can't lock file '/var/lock/qemu-server/lock-110.conf'"}`)},
		{ID: uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa2"), Accion: ports.AccionAsignarPermisos, Detalles: json.RawMessage(`{"permisos":[{"vmid":110,"nivelAcceso":"FULL_ACCESS"},{"vmid":201,"nivelAcceso":"READ_ONLY"}],"usuarioAfectado":"x"}`)},
		{ID: uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa3"), Accion: ports.AccionLogin, Detalles: json.RawMessage(`{"razon":"contraseña incorrecta"}`)},
	}
	return &auditoriaConFiltro{mockAuditRepository: repo}
}

func refsAuditoria(t *testing.T) *crypto.OfuscadorVMID {
	t.Helper()
	o, err := crypto.NuevoOfuscadorVMID("salt-de-prueba-de-al-menos-32-caracteres!")
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestAuditoria_ListadoYExportacionesNoExponenVmids(t *testing.T) {
	refs := refsAuditoria(t)
	ref110, _ := refs.OfuscarVMID(110)
	ref201, _ := refs.OfuscarVMID(201)
	repo := auditoriaConVmids()
	svc := services.NewAuditService(repo, refs)
	ctx := context.Background()

	pagina, err := svc.ListarAuditoria(ctx, uuid.New(), ports.FiltrosAuditoria{}, ports.OpcionesAuditoria{})
	if err != nil || len(pagina.Items) != 3 {
		t.Fatalf("%v %+v", err, pagina)
	}
	inicio, permisos := pagina.Items[0], pagina.Items[1]
	if inicio.InstanciaID != ref110 || inicio.RecursoID != ref110 || !strings.Contains(string(inicio.Detalles), "lock-"+ref110+".conf") {
		t.Errorf("Inicio: %+v %s", inicio, inicio.Detalles)
	}
	var detalles struct {
		Permisos []map[string]any `json:"permisos"`
	}
	_ = json.Unmarshal(permisos.Detalles, &detalles)
	if len(detalles.Permisos) != 2 || detalles.Permisos[0]["vmid"] != ref110 || detalles.Permisos[1]["vmid"] != ref201 || detalles.Permisos[0]["nivelAcceso"] != "FULL_ACCESS" {
		t.Errorf("Asignación: los vmid de detalles.permisos pasan a la referencia: %s", permisos.Detalles)
	}
	if repo.registros[0].InstanciaID != "110" {
		t.Error("No debe modificar los registros que devolvió el repositorio")
	}

	for formato, exportar := range map[string]func(context.Context, uuid.UUID, ports.FiltrosAuditoria) ([]byte, error){
		"CSV": svc.ExportarCSV, "JSON": svc.ExportarJSON,
	} {
		datos, err := exportar(ctx, uuid.New(), ports.FiltrosAuditoria{})
		if err != nil {
			t.Fatal(err)
		}
		texto := string(datos)
		if !strings.Contains(texto, ref110) || !strings.Contains(texto, ref201) || strings.Contains(texto, "110") || strings.Contains(texto, `"vmid":1`) || strings.Contains(texto, `"vmid":2`) {
			t.Errorf("%s no debe contener VMIDs:\n%s", formato, texto)
		}
	}
}

// El filtro instanciaId llega como referencia y se consulta por el VMID; una
// referencia inválida (o un VMID numérico) no consulta y devuelve vacío.
func TestAuditoria_FiltroPorReferencia(t *testing.T) {
	refs := refsAuditoria(t)
	ref110, _ := refs.OfuscarVMID(110)
	repo := auditoriaConVmids()
	svc := services.NewAuditService(repo, refs)

	if _, err := svc.ListarAuditoria(context.Background(), uuid.New(), ports.FiltrosAuditoria{InstanciaID: ref110}, ports.OpcionesAuditoria{}); err != nil || repo.filtros.InstanciaID != "110" {
		t.Errorf("La referencia debe consultarse como VMID 110: %q %v", repo.filtros.InstanciaID, err)
	}
	for _, invalida := range []string{"110", "inst_k8Pq2v"} {
		repo.consulto = false
		pagina, err := svc.ListarAuditoria(context.Background(), uuid.New(), ports.FiltrosAuditoria{InstanciaID: invalida}, ports.OpcionesAuditoria{})
		if err != nil || repo.consulto || pagina.Total != 0 || pagina.Items == nil || len(pagina.Items) != 0 {
			t.Errorf("%q: se esperaba lista vacía sin consultar: %+v %v (consultó=%v)", invalida, pagina, err, repo.consulto)
		}
		datos, err := svc.ExportarJSON(context.Background(), uuid.New(), ports.FiltrosAuditoria{InstanciaID: invalida})
		if err != nil || strings.TrimSpace(string(datos)) != "[]" {
			t.Errorf("%q: la exportación debe ser []: %s %v", invalida, datos, err)
		}
	}
}

// Sin traductor configurado, los ids de instancia salen vacíos: nunca el VMID.
func TestAuditoria_SinReferenciasNoExponeVmids(t *testing.T) {
	svc := services.NewAuditService(auditoriaConVmids())
	datos, err := svc.ExportarCSV(context.Background(), uuid.New(), ports.FiltrosAuditoria{})
	if err != nil || strings.Contains(string(datos), "110") || strings.Contains(string(datos), "201") {
		t.Errorf("Sin refs no debe salir ningún VMID: %v\n%s", err, datos)
	}
}

func TestAuditoria_FiltroRecursoPublicoListadoYExportacion(t *testing.T) {
	refs := refsAuditoria(t)
	ref, _ := refs.OfuscarVMID(110)
	for _, tipo := range []string{ports.RecursoVM, ports.RecursoLXC, ""} {
		for _, operacion := range []string{"listado", "JSON", "CSV"} {
			repo := auditoriaConVmids()
			svc := services.NewAuditService(repo, refs)
			f := ports.FiltrosAuditoria{RecursoTipo: tipo, RecursoID: ref}
			var err error
			switch operacion {
			case "listado":
				_, err = svc.ListarAuditoria(context.Background(), uuid.New(), f, ports.OpcionesAuditoria{})
			case "JSON":
				_, err = svc.ExportarJSON(context.Background(), uuid.New(), f)
			case "CSV":
				_, err = svc.ExportarCSV(context.Background(), uuid.New(), f)
			}
			if err != nil || !repo.consulto || repo.filtros.RecursoID != "110" {
				t.Fatalf("%s/%s: filtro=%+v err=%v", tipo, operacion, repo.filtros, err)
			}
		}
	}
	for _, tipo := range []string{ports.RecursoVM, ports.RecursoLXC} {
		repo := auditoriaConVmids()
		svc := services.NewAuditService(repo, refs)
		_, err := svc.ListarAuditoria(context.Background(), uuid.New(), ports.FiltrosAuditoria{RecursoTipo: tipo, RecursoID: "110"}, ports.OpcionesAuditoria{})
		if err != nil || repo.consulto {
			t.Fatal("el VMID crudo no es una referencia pública válida")
		}
	}
	for tipo, id := range map[string]string{"HOST": "host", "USER": uuid.NewString()} {
		repo := auditoriaConVmids()
		svc := services.NewAuditService(repo, refs)
		_, err := svc.ListarAuditoria(context.Background(), uuid.New(), ports.FiltrosAuditoria{RecursoTipo: tipo, RecursoID: id}, ports.OpcionesAuditoria{})
		if err != nil || repo.filtros.RecursoID != id {
			t.Fatal("se alteró un identificador ajeno a VM/LXC")
		}
	}
}

type cursorPublicoTest struct {
	row     ports.AuditoriaDTO
	next    bool
	cerrado bool
}

func (c *cursorPublicoTest) Next() bool {
	if c.next {
		return false
	}
	c.next = true
	return true
}
func (c *cursorPublicoTest) Actual() (ports.AuditoriaDTO, error) { return c.row, nil }
func (c *cursorPublicoTest) Err() error                          { return nil }
func (c *cursorPublicoTest) Close() error                        { c.cerrado = true; return nil }

type repoPublicoStreamingTest struct {
	*auditoriaConFiltro
	cursor *cursorPublicoTest
}

func (r *repoPublicoStreamingTest) AbrirExportacion(_ context.Context, _ uuid.UUID, f ports.FiltrosAuditoria) (ports.CursorAuditoria, error) {
	r.filtros = f
	r.consulto = true
	return r.cursor, nil
}
func TestAuditoria_CursorTraduceFiltrosYFilas(t *testing.T) {
	refs := refsAuditoria(t)
	ref, _ := refs.OfuscarVMID(110)
	original := auditoriaConVmids()
	repo := &repoPublicoStreamingTest{auditoriaConFiltro: original, cursor: &cursorPublicoTest{row: original.registros[0]}}
	svc := services.NewAuditService(repo, refs).(ports.AuditStreaming)
	cursor, err := svc.AbrirExportacion(context.Background(), uuid.New(), ports.FiltrosAuditoria{RecursoTipo: "VM", RecursoID: ref})
	if err != nil {
		t.Fatal(err)
	}
	if repo.filtros.RecursoID != "110" || !cursor.Next() {
		t.Fatal("filtro/cursor inválido")
	}
	row, err := cursor.Actual()
	if err != nil || row.RecursoID != ref || row.InstanciaID != ref || strings.Contains(string(row.Detalles), "lock-110.conf") {
		t.Fatalf("fila privada: %+v %v", row, err)
	}
	cursor.Close()
	if !repo.cursor.cerrado || repo.cursor.row.RecursoID != "110" {
		t.Fatal("no cerró o mutó el origen")
	}
	repo.consulto = false
	cursor, err = svc.AbrirExportacion(context.Background(), uuid.New(), ports.FiltrosAuditoria{RecursoTipo: "VM", RecursoID: "110"})
	if err != nil || repo.consulto || cursor.Next() {
		t.Fatal("referencia inválida consultó el repositorio")
	}
	cursor.Close()
}
