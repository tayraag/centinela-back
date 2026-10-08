package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	pg "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// conectarDBTest conecta a la base de datos local para pruebas de integración.
func conectarDBTest(t *testing.T) *gorm.DB {
	t.Helper()

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

func TestMigrarAuditoriaParticionada_TraspasoLegacy(t *testing.T) {
	db := conectarDBTest(t)
	
	// Obtener un usuario base
	var usuario domain.Usuario
	if err := db.First(&usuario).Error; err != nil {
		t.Fatalf("Se requiere al menos un usuario en la base de datos para ejecutar el test: %v", err)
	}

	// Iniciar una transacción para no afectar la DB de pruebas
	tx := db.Begin()
	defer tx.Rollback()

	// 1. Arrange: Limpiar tablas y crear la tabla PLANA 'auditoria' para simular el estado pre-migración
	tx.Exec("DROP TABLE IF EXISTS auditoria CASCADE")
	tx.Exec("DROP TABLE IF EXISTS auditoria_legacy CASCADE")

	// Crear tabla plana
	err := tx.Exec(`
		CREATE TABLE auditoria (
			id               UUID         NOT NULL DEFAULT uuid_generate_v7() PRIMARY KEY,
			usuario_id       UUID         REFERENCES usuarios(id) ON UPDATE CASCADE ON DELETE RESTRICT,
			accion           VARCHAR(100) NOT NULL,
			instancia_id     VARCHAR(100),
			instancia_nombre VARCHAR(255),
			resultado        VARCHAR(50)  NOT NULL,
			detalles         JSONB,
			fecha_hora       TIMESTAMPTZ  NOT NULL DEFAULT NOW()
		)
	`).Error
	if err != nil {
		t.Fatalf("Fallo al crear tabla plana auditoria: %v", err)
	}

	// Insertar 5 registros de prueba con fechas anteriores
	for i := 0; i < 5; i++ {
		fecha := time.Now().AddDate(0, 0, -i*10) // Fechas pasadas
		id := uuid.New()
		err := tx.Exec(`
			INSERT INTO auditoria (id, fecha_hora, usuario_id, accion, instancia_id, resultado)
			VALUES (?, ?, ?, ?, ?, ?)
		`, id, fecha, usuario.ID, fmt.Sprintf("LEGACY_ACTION_%d", i), "101", "EXITO").Error
		if err != nil {
			t.Fatalf("Fallo al insertar registro legacy %d: %v", i, err)
		}
	}

	// 2. Act: Ejecutar la migración
	if err := migrarAuditoriaParticionada(tx); err != nil {
		t.Fatalf("Fallo en migrarAuditoriaParticionada: %v", err)
	}

	// 3. Assert: Verificar que los datos sigan existiendo y sean legibles por el Repositorio
	repo := NewAuditRepository(tx)
	
	filtros := ports.FiltrosAuditoria{}
	opciones := ports.OpcionesAuditoria{
		Pagina:     1,
		TamanoPag:  50,
		OrdenarPor: "fechaHora",
		Direccion:  "desc",
	}

	pagina, err := repo.Listar(context.Background(), usuario.OrganizacionID, filtros, opciones)
	if err != nil {
		t.Fatalf("Fallo al listar auditoría post-migración: %v", err)
	}

	// Contar cuántos eventos LEGACY_ACTION_ hay en el resultado
	legaciesCount := 0
	for _, item := range pagina.Items {
		if len(item.Accion) >= 14 && item.Accion[:14] == "LEGACY_ACTION_" {
			legaciesCount++
		}
	}

	if legaciesCount != 5 {
		t.Errorf("Se esperaban 5 registros migrados, se encontraron %d en la paginación", legaciesCount)
	}

	exported, err := repo.ExportarRegistros(context.Background(), usuario.OrganizacionID, filtros)
	if err != nil {
		t.Fatalf("Fallo al exportar registros: %v", err)
	}
	if len(exported) == 0 {
		t.Errorf("La exportación devolvió 0 registros")
	}

	// Validar que la tabla auditoria_legacy ya no exista
	var legacyExiste int
	tx.Raw(`
		SELECT count(*)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'auditoria_legacy'
		  AND n.nspname = current_schema()
	`).Scan(&legacyExiste)

	if legacyExiste != 0 {
		t.Errorf("La tabla auditoria_legacy no fue eliminada tras la migración")
	}
}

func TestMigrarAuditoriaParticionada_RollbackEnFallo(t *testing.T) {
	db := conectarDBTest(t)

	var usuario domain.Usuario
	if err := db.First(&usuario).Error; err != nil {
		t.Fatalf("Se requiere al menos un usuario: %v", err)
	}

	tx := db.Begin()
	defer tx.Rollback()

	tx.Exec("DROP TABLE IF EXISTS auditoria CASCADE")
	tx.Exec("DROP TABLE IF EXISTS auditoria_legacy CASCADE")

	// Crear tabla plana legacy
	err := tx.Exec(`
		CREATE TABLE auditoria (
			id               UUID         NOT NULL DEFAULT uuid_generate_v7() PRIMARY KEY,
			usuario_id       UUID         REFERENCES usuarios(id) ON UPDATE CASCADE ON DELETE RESTRICT,
			accion           VARCHAR(100) NOT NULL,
			instancia_id     VARCHAR(100),
			instancia_nombre VARCHAR(255),
			resultado        VARCHAR(50)  NOT NULL,
			detalles         JSONB,
			fecha_hora       TIMESTAMPTZ  NOT NULL DEFAULT NOW()
		)
	`).Error
	if err != nil {
		t.Fatalf("Fallo al crear tabla plana: %v", err)
	}

	for i := 0; i < 5; i++ {
		fecha := time.Now().AddDate(0, 0, -i*10)
		id := uuid.New()
		err := tx.Exec(`
			INSERT INTO auditoria (id, fecha_hora, usuario_id, accion, instancia_id, resultado)
			VALUES (?, ?, ?, ?, ?, ?)
		`, id, fecha, usuario.ID, fmt.Sprintf("LEGACY_ACTION_%d", i), "101", "EXITO").Error
		if err != nil {
			t.Fatalf("Fallo al insertar registro legacy: %v", err)
		}
	}

	// Provocar que la migración falle creando la tabla destino con una restricción estricta
	// MigrarAuditoriaParticionada hace RENAME de auditoria a auditoria_legacy, y luego intenta crear
	// 'auditoria' de nuevo, PERO IF NOT EXISTS.
	// Entonces, si creamos 'auditoria_legacy' manualmente y luego 'auditoria' con restricción estricta,
	// fallará en el INSERT.
	
	// Renombrar manualmente a legacy
	tx.Exec(`ALTER TABLE auditoria RENAME TO auditoria_legacy`)

	// Crear auditoria destino con longitud de accion muy corta (VARCHAR(5))
	// Para forzar que el volcado desde legacy falle
	tx.Exec(`
		CREATE TABLE auditoria (
			id               UUID         NOT NULL DEFAULT uuid_generate_v7(),
			usuario_id       UUID         REFERENCES usuarios(id) ON UPDATE CASCADE ON DELETE RESTRICT,
			accion           VARCHAR(5)   NOT NULL,
			instancia_id     VARCHAR(100),
			instancia_nombre VARCHAR(255),
			resultado        VARCHAR(50)  NOT NULL,
			detalles         JSONB,
			fecha_hora       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
			PRIMARY KEY (id, fecha_hora)
		) PARTITION BY RANGE (fecha_hora);
	`)

	// Act: Ejecutar la migración (debe fallar y hacer rollback)
	err = migrarAuditoriaParticionada(tx)
	if err == nil {
		t.Fatalf("Se esperaba un error por volcado fallido, pero no ocurrió")
	}

	// Assert: La tabla legacy debe seguir existiendo y con datos
	var legacyExiste int
	tx.Raw(`
		SELECT count(*) FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'auditoria_legacy' AND n.nspname = current_schema()
	`).Scan(&legacyExiste)

	if legacyExiste == 0 {
		t.Errorf("La tabla auditoria_legacy fue eliminada a pesar de fallar la migración")
	}

	var count int64
	tx.Table("auditoria_legacy").Count(&count)
	if count != 5 {
		t.Errorf("Se esperaban 5 registros en auditoria_legacy tras el rollback, se encontraron %d", count)
	}
}
