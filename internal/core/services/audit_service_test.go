package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"

	"github.com/google/uuid"
)

// mockAuditRepository implementa ports.AuditRepository en memoria para tests unitarios.
type mockAuditRepository struct {
	registros       []ports.AuditoriaDTO
	ultimoInput     *ports.RegistrarAuditoriaInput
	ultimaOrgID     uuid.UUID
	ultimasOpciones ports.OpcionesAuditoria
	registrarErr    error
}

func newMockAuditRepository() *mockAuditRepository {
	return &mockAuditRepository{
		registros: make([]ports.AuditoriaDTO, 0),
	}
}

func (m *mockAuditRepository) Registrar(ctx context.Context, input ports.RegistrarAuditoriaInput) error {
	if m.registrarErr != nil {
		return m.registrarErr
	}
	m.ultimoInput = &input
	id := uuid.New()
	var detallesJSON json.RawMessage
	if len(input.Detalles) > 0 {
		detallesJSON = json.RawMessage(`{"mock":"true"}`)
	}

	dto := ports.AuditoriaDTO{
		ID:              id,
		UsuarioID:       &input.UsuarioID,
		NombreUsuario:   "Test User",
		FechaHora:       time.Now(),
		Accion:          input.Accion,
		InstanciaID:     input.InstanciaID,
		InstanciaNombre: input.InstanciaNombre,
		Upid:            input.Upid,
		Resultado:       input.Resultado,
		Detalles:        detallesJSON,
	}
	m.registros = append(m.registros, dto)
	return nil
}

func TestAuditService_RegistrarEstrictoPropagaError(t *testing.T) {
	repo := newMockAuditRepository()
	repo.registrarErr = errors.New("postgres no disponible")
	svc := services.NewAuditService(repo)
	err := svc.RegistrarEstricto(context.Background(), ports.RegistrarAuditoriaInput{Accion: ports.AccionConfigurarInstancia})
	if err == nil || !errors.Is(err, repo.registrarErr) {
		t.Fatalf("se esperaba propagar el error de persistencia, vino %v", err)
	}
}

func (m *mockAuditRepository) Listar(ctx context.Context, orgID uuid.UUID, filtros ports.FiltrosAuditoria, opciones ports.OpcionesAuditoria) (*ports.PaginaAuditoria, error) {
	m.ultimaOrgID = orgID
	m.ultimasOpciones = opciones

	var filtrados []ports.AuditoriaDTO
	for _, r := range m.registros {
		if filtros.Accion != "" && r.Accion != filtros.Accion {
			continue
		}
		filtrados = append(filtrados, r)
	}

	return &ports.PaginaAuditoria{
		Total:  int64(len(filtrados)),
		Pagina: opciones.Pagina,
		Items:  filtrados,
	}, nil
}

func (m *mockAuditRepository) ExportarRegistros(ctx context.Context, orgID uuid.UUID, filtros ports.FiltrosAuditoria) ([]ports.AuditoriaDTO, error) {
	return m.registros, nil
}

func TestAuditService_RegistrarYListar(t *testing.T) {
	mockRepo := newMockAuditRepository()
	svc := services.NewAuditService(mockRepo)
	ctx := context.Background()

	orgID := uuid.New()
	usuarioID := uuid.New()

	// 1. Probar registrar un evento
	input := ports.RegistrarAuditoriaInput{
		UsuarioID:       usuarioID,
		Accion:          ports.AccionLogin,
		InstanciaID:     "vm-200",
		InstanciaNombre: "VM Prueba",
		Resultado:       ports.ResultadoExito,
		Detalles:        map[string]any{"ip": "127.0.0.1"},
	}

	svc.Registrar(ctx, input)

	if mockRepo.ultimoInput == nil {
		t.Fatalf("Se esperaba que Registrar llamara al repositorio")
	}
	if mockRepo.ultimoInput.Accion != ports.AccionLogin {
		t.Errorf("Acción registrada incorrecta: %s", mockRepo.ultimoInput.Accion)
	}

	// 2. Probar listar auditoría con sanitización de opciones
	opcionesInvalidas := ports.OpcionesAuditoria{
		Pagina:     0,          // Debe sanitizarse a 1
		TamanoPag:  -10,        // Debe sanitizarse a 50
		OrdenarPor: "invalido", // Debe sanitizarse a "fechaHora"
		Direccion:  "invalido", // Debe sanitizarse a "desc"
	}

	pagina, err := svc.ListarAuditoria(ctx, orgID, ports.FiltrosAuditoria{Accion: ports.AccionLogin}, opcionesInvalidas)
	if err != nil {
		t.Fatalf("Error inesperado en ListarAuditoria: %v", err)
	}

	// Verificar sanitización
	if mockRepo.ultimasOpciones.Pagina != 1 {
		t.Errorf("Pagina esperada: 1, obtenida: %d", mockRepo.ultimasOpciones.Pagina)
	}
	if mockRepo.ultimasOpciones.TamanoPag != 50 {
		t.Errorf("TamanoPag esperado: 50, obtenido: %d", mockRepo.ultimasOpciones.TamanoPag)
	}
	if mockRepo.ultimasOpciones.OrdenarPor != "fechaHora" {
		t.Errorf("OrdenarPor esperado: fechaHora, obtenido: %s", mockRepo.ultimasOpciones.OrdenarPor)
	}
	if mockRepo.ultimasOpciones.Direccion != "desc" {
		t.Errorf("Direccion esperada: desc, obtenida: %s", mockRepo.ultimasOpciones.Direccion)
	}

	// Verificar resultado
	if pagina.Total != 1 {
		t.Errorf("Total esperado: 1, obtenido: %d", pagina.Total)
	}
	if len(pagina.Items) != 1 {
		t.Fatalf("Items esperados: 1, obtenidos: %d", len(pagina.Items))
	}
	if pagina.Items[0].Accion != ports.AccionLogin {
		t.Errorf("Acción recuperada esperada: %s, obtenida: %s", ports.AccionLogin, pagina.Items[0].Accion)
	}
}

func TestAuditService_PrepararAuditoriaTareaCorrelacion(t *testing.T) {
	repo := newMockAuditRepository()
	svc := services.NewAuditService(repo)
	for accion, codigo := range map[string]string{"start": "INICIAR_VM", "stop": "DETENER_VM", "shutdown": "APAGAR_VM", "reboot": "REINICIAR_VM", "delete": "ELIMINAR_VM"} {
		for _, caso := range []struct {
			nombre, exit, motivo, resultado string
		}{
			{"exito", "OK", "", "SUCCESS"},
			{"error", "permission denied", "PROXMOX_ERROR", "FAILED"},
			{"timeout", "", "TIMEOUT", "FAILED"},
			{"advertencias", "WARNINGS: 1", "", "FAILED"},
		} {
			t.Run(accion+"/"+caso.nombre, func(t *testing.T) {
				meta := `{"instanciaNombre":"db-110","resource_type":"lxc"}`
				tarea := domain.TareaAsincrona{ID: uuid.New(), UsuarioID: uuid.New(), Accion: accion, UpidProxmox: "UPID:test", InstanciaID: "110", FechaCreacion: time.Now(), Metadatos: &meta}
				cierre := map[string]any{"exitstatus": caso.exit, "motivo": nil}
				if caso.motivo != "" {
					cierre["motivo"] = caso.motivo
				}
				inicio := svc.PrepararAuditoriaTarea(&tarea, nil, tarea.FechaCreacion)
				fin := svc.PrepararAuditoriaTarea(&tarea, cierre, tarea.FechaCreacion.Add(2500*time.Millisecond))
				if repo.ultimoInput != nil {
					t.Fatal("preparar no debe escribir fuera de la transacción de tareas")
				}
				if inicio.Resultado != "PENDIENTE" || fin.Resultado != caso.resultado || inicio.Accion != codigo || fin.Accion != codigo {
					t.Fatalf("registros incorrectos: %+v %+v", inicio, fin)
				}
				for _, reg := range []ports.RegistrarAuditoriaInput{inicio, fin} {
					if reg.UsuarioID != tarea.UsuarioID || reg.InstanciaID != "110" || reg.InstanciaNombre != "db-110" || reg.Detalles["upid"] != tarea.UpidProxmox || reg.Detalles["tareaId"] != tarea.ID.String() {
						t.Fatalf("correlación incorrecta: %+v", reg)
					}
				}
				if fin.Detalles["duracionSegundos"] != 2.5 || fin.Detalles["exitstatus"] != caso.exit {
					t.Fatalf("desenlace incorrecto: %+v", fin)
				}
				if caso.motivo != "" && fin.Detalles["motivo"] != caso.motivo {
					t.Fatalf("motivo incorrecto: %+v", fin)
				}
			})
		}
	}
}

func TestAuditService_CreacionGeneraContratoRF08(t *testing.T) {
	svc := services.NewAuditService(newMockAuditRepository())
	meta := `{"instanciaNombre":"web-108","resource_type":"qemu","cores":4,"ramMb":4096,"discoGb":40}`
	tarea := domain.TareaAsincrona{
		ID: uuid.New(), UsuarioID: uuid.New(), Accion: ports.AccionProxmoxCrearVM,
		UpidProxmox: "UPID:pve1:qmcreate:108", InstanciaID: "108",
		FechaCreacion: time.Now(), Metadatos: &meta,
	}
	inicio := svc.PrepararAuditoriaTarea(&tarea, nil, tarea.FechaCreacion)
	fin := svc.PrepararAuditoriaTarea(&tarea, map[string]any{"exitstatus": "OK", "motivo": nil, "error": nil}, tarea.FechaCreacion.Add(3*time.Second))

	for _, registro := range []ports.RegistrarAuditoriaInput{inicio, fin} {
		if registro.Accion != ports.AccionCrearInstancia || registro.Upid != tarea.UpidProxmox ||
			registro.RecursoTipo != ports.RecursoVM || registro.RecursoID != tarea.InstanciaID ||
			registro.Detalles["tareaId"] != tarea.ID.String() {
			t.Fatalf("correlación de creación incorrecta: %+v", registro)
		}
		if _, duplicado := registro.Detalles["upid"]; duplicado {
			t.Fatalf("el UPID debe ser un campo propio, no duplicarse en detalles: %+v", registro.Detalles)
		}
	}
	if inicio.Resultado != ports.ResultadoPendiente || inicio.Detalles["nombre"] != "web-108" ||
		inicio.Detalles["tipo"] != "qemu" || inicio.Detalles["cores"] != float64(4) ||
		inicio.Detalles["ramMb"] != float64(4096) || inicio.Detalles["discoGb"] != float64(40) {
		t.Fatalf("despacho de creación incompleto: %+v", inicio)
	}
	if fin.Resultado != ports.ResultadoSuccess || fin.Detalles["exitstatus"] != "OK" || fin.Detalles["duracionSegundos"] != float64(3) {
		t.Fatalf("desenlace de creación incompleto: %+v", fin)
	}
}
