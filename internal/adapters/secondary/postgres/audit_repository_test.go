package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/postgres"
	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	pg "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// conectarDBTest conecta a la base de datos local para pruebas de integración.
// Si no hay conexión o no existe .env, saltea el test con t.Skip.
func conectarDBTest(t *testing.T) *gorm.DB {
	t.Helper()

	// Intentar cargar .env buscando hacia arriba en las carpetas
	_ = godotenv.Load("../../../../.env", "../../../.env", "../../.env", ".env")

	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		t.Skip("Saltando test de integración: DB_DSN no está configurada en .env")
	}

	db, err := gorm.Open(pg.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Skipf("Saltando test de integración: no se pudo conectar a PostgreSQL: %v", err)
	}

	return db
}

func TestAuditRepository_CrearYRecuperarEvento(t *testing.T) {
	db := conectarDBTest(t)
	repo := postgres.NewAuditRepository(db)
	ctx := context.Background()

	// 1. Obtener un usuario existente en la base de datos para la prueba
	var usuario domain.Usuario
	if err := db.First(&usuario).Error; err != nil {
		t.Fatalf("Se requiere al menos un usuario en la base de datos para ejecutar el test: %v", err)
	}

	accionUnica := "TEST_EVENTO_" + uuid.New().String()[:8]

	// 2. Registrar evento con detalles
	inputConDetalles := ports.RegistrarAuditoriaInput{
		UsuarioID:       usuario.ID,
		Accion:          accionUnica,
		InstanciaID:     "vm-101",
		InstanciaNombre: "Servidor Test",
		Resultado:       ports.ResultadoExito,
		Detalles: map[string]any{
			"origen": "test_unitario",
			"campo":  "valor_prueba",
		},
	}

	if err := repo.Registrar(ctx, inputConDetalles); err != nil {
		t.Fatalf("Fallo al registrar evento de auditoría: %v", err)
	}

	// Limpieza al finalizar el test
	t.Cleanup(func() {
		db.Exec("DELETE FROM auditoria WHERE accion = ?", accionUnica)
	})

	// 3. Recuperar el evento listando por organización y filtrando por la acción
	filtros := ports.FiltrosAuditoria{
		Accion: accionUnica,
	}
	opciones := ports.OpcionesAuditoria{
		Pagina:     1,
		TamanoPag:  10,
		OrdenarPor: "fechaHora",
		Direccion:  "desc",
	}

	pagina, err := repo.Listar(ctx, usuario.OrganizacionID, filtros, opciones)
	if err != nil {
		t.Fatalf("Fallo al listar auditoría: %v", err)
	}

	// 4. Aserciones sobre los datos recuperados
	if pagina.Total != 1 {
		t.Fatalf("Se esperaba 1 registro, se obtuvieron %d", pagina.Total)
	}
	if len(pagina.Items) != 1 {
		t.Fatalf("Se esperaba 1 item en la página, se obtuvieron %d", len(pagina.Items))
	}

	recuperado := pagina.Items[0]

	if recuperado.Accion != accionUnica {
		t.Errorf("Accion incorrecta. Esperado: %s, Obtenido: %s", accionUnica, recuperado.Accion)
	}
	if recuperado.UsuarioID == nil || *recuperado.UsuarioID != usuario.ID {
		t.Errorf("UsuarioID incorrecto. Esperado: %s, Obtenido: %v", usuario.ID, recuperado.UsuarioID)
	}
	if recuperado.NombreUsuario != usuario.NombreCompleto {
		t.Errorf("NombreUsuario incorrecto. Esperado: %s, Obtenido: %s", usuario.NombreCompleto, recuperado.NombreUsuario)
	}
	if recuperado.InstanciaID != "vm-101" {
		t.Errorf("InstanciaID incorrecto. Esperado: vm-101, Obtenido: %s", recuperado.InstanciaID)
	}
	if recuperado.InstanciaNombre != "Servidor Test" {
		t.Errorf("InstanciaNombre incorrecto. Esperado: Servidor Test, Obtenido: %s", recuperado.InstanciaNombre)
	}
	if recuperado.Resultado != ports.ResultadoExito {
		t.Errorf("Resultado incorrecto. Esperado: EXITO, Obtenido: %s", recuperado.Resultado)
	}
	if recuperado.Detalles == "" {
		t.Errorf("Se esperaban detalles JSON serializados, se obtuvo string vacío")
	}
	if time.Since(recuperado.FechaHora) > 1*time.Minute {
		t.Errorf("La fechaHora registrada es inconsistente: %v", recuperado.FechaHora)
	}
}

func TestAuditRepository_CrearSinDetalles(t *testing.T) {
	db := conectarDBTest(t)
	repo := postgres.NewAuditRepository(db)
	ctx := context.Background()

	var usuario domain.Usuario
	if err := db.First(&usuario).Error; err != nil {
		t.Fatalf("Se requiere al menos un usuario en la base de datos: %v", err)
	}

	accionSinDetalles := "TEST_LOGIN_" + uuid.New().String()[:8]

	// Registrar evento SIN detalles (prueba que no falle la sintaxis jsonb)
	input := ports.RegistrarAuditoriaInput{
		UsuarioID: usuario.ID,
		Accion:    accionSinDetalles,
		Resultado: ports.ResultadoExito,
		Detalles:  nil,
	}

	if err := repo.Registrar(ctx, input); err != nil {
		t.Fatalf("Fallo al registrar evento sin detalles (error jsonb regresión): %v", err)
	}

	t.Cleanup(func() {
		db.Exec("DELETE FROM auditoria WHERE accion = ?", accionSinDetalles)
	})

	pagina, err := repo.Listar(ctx, usuario.OrganizacionID, ports.FiltrosAuditoria{Accion: accionSinDetalles}, ports.OpcionesAuditoria{
		Pagina:     1,
		TamanoPag:  10,
		OrdenarPor: "fechaHora",
		Direccion:  "desc",
	})
	if err != nil {
		t.Fatalf("Fallo al listar auditoría: %v", err)
	}

	if pagina.Total != 1 {
		t.Fatalf("Se esperaba 1 registro sin detalles, se obtuvieron %d", pagina.Total)
	}
	if pagina.Items[0].Detalles != "" {
		t.Errorf("Se esperaba Detalles vacío en el DTO, se obtuvo: %s", pagina.Items[0].Detalles)
	}
}
