package services_test

import (
	"context"
	"testing"
	"time"

	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"

	"github.com/google/uuid"
)

// mockAuditRepository implementa ports.AuditRepository en memoria para tests unitarios.
type mockAuditRepository struct {
	registros []ports.AuditoriaDTO
	ultimoInput *ports.RegistrarAuditoriaInput
	ultimaOrgID uuid.UUID
	ultimasOpciones ports.OpcionesAuditoria
}

func newMockAuditRepository() *mockAuditRepository {
	return &mockAuditRepository{
		registros: make([]ports.AuditoriaDTO, 0),
	}
}

func (m *mockAuditRepository) Registrar(ctx context.Context, input ports.RegistrarAuditoriaInput) error {
	m.ultimoInput = &input
	id := uuid.New()
	detallesStr := ""
	if len(input.Detalles) > 0 {
		detallesStr = `{"mock":"true"}`
	}

	dto := ports.AuditoriaDTO{
		ID:              id,
		UsuarioID:       &input.UsuarioID,
		NombreUsuario:   "Test User",
		FechaHora:       time.Now(),
		Accion:          input.Accion,
		InstanciaID:     input.InstanciaID,
		InstanciaNombre: input.InstanciaNombre,
		Resultado:       input.Resultado,
		Detalles:        detallesStr,
	}
	m.registros = append(m.registros, dto)
	return nil
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
		Pagina:     0,        // Debe sanitizarse a 1
		TamanoPag:  -10,      // Debe sanitizarse a 50
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
