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

// Guarda defensiva: sin tabla auditoria no hay historial que clasificar.
func TestClasificarEliminados_SinTablaAuditoria(t *testing.T) {
	tx := transaccionDescartable(t)
	if err := tx.Exec(`ALTER TABLE auditoria RENAME TO auditoria_oculta_test`).Error; err != nil {
		t.Fatal(err)
	}
	if err := clasificarEliminados(tx); err != nil {
		t.Errorf("Sin tabla auditoria debe salir sin error: %v", err)
	}
	if err := migrarUnicidadEmail(tx); err != nil {
		t.Errorf("La migración completa tampoco debe fallar sin auditoria: %v", err)
	}
	// La transacción se descarta al terminar: la tabla vuelve a su nombre.
}
