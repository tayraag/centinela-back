package postgres

import (
	"os"
	"regexp"
	"testing"

	"github.com/google/uuid"
	pg "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// baseVacia crea una base de datos nueva (como un docker compose down -v) con
// el mismo scripts/init.sql que corre el contenedor, y devuelve su DSN. La base
// se borra al terminar el test.
func baseVacia(t *testing.T) string {
	t.Helper()
	admin := conectarDBTest(t)
	nombre := "centinela_test_" + uuid.NewString()[:8]
	if err := admin.Exec("CREATE DATABASE " + nombre).Error; err != nil {
		t.Skipf("Sin permiso para crear una base de prueba: %v", err)
	}
	t.Cleanup(func() { admin.Exec("DROP DATABASE IF EXISTS " + nombre + " WITH (FORCE)") })

	dsn := regexp.MustCompile(`dbname=\S+`).ReplaceAllString(os.Getenv("DB_DSN"), "dbname="+nombre)
	nueva, err := gorm.Open(pg.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	initSQL, err := os.ReadFile("../../../../scripts/init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := nueva.Exec(string(initSQL)).Error; err != nil {
		t.Fatalf("init.sql: %v", err)
	}
	cerrar(nueva)
	return dsn
}

func cerrar(db *gorm.DB) {
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
}

// Instalación limpia: InitDB sobre una base vacía no debe fallar por el orden
// de las migraciones (migrarUnicidadEmail lee auditoria, que se crea en
// migrarAuditoriaParticionada). Un segundo arranque tampoco.
func TestInitDB_BaseVacia(t *testing.T) {
	t.Setenv("DB_DSN", baseVacia(t))

	for arranque := 1; arranque <= 2; arranque++ {
		db, err := InitDB()
		if err != nil {
			t.Fatalf("Arranque %d sobre base vacía: %v", arranque, err)
		}
		for _, tabla := range []string{"usuarios", "auditoria", "sesiones_activas", "tareas_asincronas"} {
			if !db.Migrator().HasTable(tabla) {
				t.Errorf("Arranque %d: falta la tabla %s", arranque, tabla)
			}
		}
		if !existeIndice(db, indiceEmailParcial) {
			t.Errorf("Arranque %d: falta el índice %s", arranque, indiceEmailParcial)
		}
		if viejos, err := indicesEmailIncondicionales(db); err != nil || len(viejos) != 0 {
			t.Errorf("Arranque %d: no debe quedar unicidad incondicional del correo: %v %v", arranque, viejos, err)
		}
		cerrar(db)
	}
}

// Camino de producción (sin transacción externa): al arrancar con registros
// viejos en auditoria_legacy, InitDB los pasa a la tabla particionada y borra
// la legacy, en una transacción.
func TestInitDB_TraspasaAuditoriaLegacy(t *testing.T) {
	t.Setenv("DB_DSN", baseVacia(t))
	db, err := InitDB()
	if err != nil {
		t.Fatalf("Primer arranque: %v", err)
	}
	if err := db.Exec(`CREATE TABLE auditoria_legacy (
			id UUID PRIMARY KEY DEFAULT uuid_generate_v7(), usuario_id UUID, accion VARCHAR(100) NOT NULL,
			instancia_id VARCHAR(100), instancia_nombre VARCHAR(255), resultado VARCHAR(50) NOT NULL,
			detalles JSONB, fecha_hora TIMESTAMPTZ NOT NULL DEFAULT NOW())`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO auditoria_legacy (accion, resultado, fecha_hora)
			SELECT 'LEGACY_' || n, 'EXITO', NOW() - (n || ' days')::interval FROM generate_series(1, 3) n`).Error; err != nil {
		t.Fatal(err)
	}
	cerrar(db)

	db, err = InitDB()
	if err != nil {
		t.Fatalf("Arranque con auditoria_legacy: %v", err)
	}
	defer cerrar(db)
	var traspasados int64
	db.Raw(`SELECT count(*) FROM auditoria WHERE accion LIKE 'LEGACY_%'`).Scan(&traspasados)
	if traspasados != 3 {
		t.Errorf("Se esperaban 3 registros traspasados a auditoria, hay %d", traspasados)
	}
	if db.Migrator().HasTable("auditoria_legacy") {
		t.Error("auditoria_legacy debe borrarse después del traspaso")
	}
}
