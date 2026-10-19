package postgres

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const archivoMigracionParticionadoAuditoria = "20261018_particionado_auditoria.sql"

func leerMigracionParticionadoAuditoria(t *testing.T) string {
	t.Helper()
	ruta := filepath.Join("..", "..", "..", "..", "migrations", archivoMigracionParticionadoAuditoria)
	contenido, err := os.ReadFile(ruta)
	if err != nil {
		t.Fatalf("no se pudo leer %s: %v", ruta, err)
	}
	return string(contenido)
}

func TestMigracionParticionadoAuditoria_DeclaraParticionesEIndice(t *testing.T) {
	sql := leerMigracionParticionadoAuditoria(t)
	for _, fragmento := range []string{
		"CREATE TABLE IF NOT EXISTS auditoria_2026_q3",
		"2026-07-01 00:00:00+00",
		"2026-10-01 00:00:00+00",
		"CREATE TABLE IF NOT EXISTS auditoria_2026_q4",
		"2027-01-01 00:00:00+00",
		"CREATE TABLE IF NOT EXISTS auditoria_2027_q1",
		"2027-04-01 00:00:00+00",
		"CREATE INDEX IF NOT EXISTS idx_auditoria_query",
		"ON auditoria (fecha_hora DESC, usuario_id, accion)",
		"DETACH PARTITION",
	} {
		if !strings.Contains(sql, fragmento) {
			t.Errorf("la migración no contiene %q", fragmento)
		}
	}
}

func TestMigracionParticionadoAuditoria_InsercionesYPruning(t *testing.T) {
	db := conectarDBTest(t)
	esquema := "test_particionado_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	err := db.Connection(func(conn *gorm.DB) error {
		var zonaHorariaOriginal string
		if err := conn.Raw(`SHOW TIME ZONE`).Scan(&zonaHorariaOriginal).Error; err != nil {
			t.Fatalf("no se pudo leer la zona horaria original: %v", err)
		}
		// El esquema aislado permite ejecutar el DDL real sin modificar las
		// tablas de desarrollo ni depender del estado previo de sus particiones.
		if err := conn.Exec(fmt.Sprintf(`CREATE SCHEMA %s`, esquema)).Error; err != nil {
			t.Fatalf("no se pudo crear el esquema aislado: %v", err)
		}
		defer func() {
			_ = conn.Exec("SET search_path TO public").Error
			_ = conn.Exec(fmt.Sprintf(`DROP SCHEMA %s CASCADE`, esquema)).Error
			_ = conn.Exec(`SELECT set_config('TimeZone', ?, false)`, zonaHorariaOriginal).Error
		}()
		if err := conn.Exec(fmt.Sprintf(`SET search_path TO %s, public`, esquema)).Error; err != nil {
			t.Fatalf("no se pudo seleccionar el esquema aislado: %v", err)
		}
		if err := conn.Exec(`SET TIME ZONE 'UTC'`).Error; err != nil {
			t.Fatalf("no se pudo fijar UTC en la conexión de prueba: %v", err)
		}
		if err := conn.Exec(`
			CREATE TABLE auditoria (
				id UUID NOT NULL,
				usuario_id UUID,
				accion VARCHAR(100) NOT NULL,
				resultado VARCHAR(50) NOT NULL,
				fecha_hora TIMESTAMPTZ NOT NULL,
				PRIMARY KEY (id, fecha_hora)
			) PARTITION BY RANGE (fecha_hora)
		`).Error; err != nil {
			t.Fatalf("no se pudo crear la tabla madre: %v", err)
		}

		migracion := leerMigracionParticionadoAuditoria(t)
		if err := conn.Exec(migracion).Error; err != nil {
			t.Fatalf("la migración inicial no pudo ejecutarse: %v", err)
		}
		// La segunda ejecución demuestra que todos los objetos son idempotentes.
		if err := conn.Exec(migracion).Error; err != nil {
			t.Fatalf("la migración no es idempotente: %v", err)
		}

		var particiones []string
		if err := conn.Raw(`
		SELECT hija.relname
		FROM pg_inherits
		JOIN pg_class madre ON madre.oid = inhparent
		JOIN pg_class hija ON hija.oid = inhrelid
		JOIN pg_namespace ns ON ns.oid = madre.relnamespace
		WHERE ns.nspname = current_schema() AND madre.relname = 'auditoria'
		ORDER BY hija.relname
		`).Scan(&particiones).Error; err != nil {
			t.Fatalf("no se pudieron consultar las particiones: %v", err)
		}
		esperadas := []string{"auditoria_2026_q3", "auditoria_2026_q4", "auditoria_2027_q1"}
		if strings.Join(particiones, ",") != strings.Join(esperadas, ",") {
			t.Fatalf("particiones inesperadas: %v", particiones)
		}

		usuarioID := uuid.New()
		casos := []struct {
			fecha     string
			particion string
		}{
			{"2026-07-15 12:00:00+00", "auditoria_2026_q3"},
			{"2026-10-15 12:00:00+00", "auditoria_2026_q4"},
			{"2027-01-15 12:00:00+00", "auditoria_2027_q1"},
		}
		for _, caso := range casos {
			id := uuid.New()
			if err := conn.Exec(`
			INSERT INTO auditoria (id, usuario_id, accion, resultado, fecha_hora)
			VALUES (?, ?, 'TEST_PARTITION', 'SUCCESS', ?)
			`, id, usuarioID, caso.fecha).Error; err != nil {
				t.Fatalf("falló la inserción de %s: %v", caso.fecha, err)
			}
			var particionReal string
			if err := conn.Raw(`SELECT tableoid::regclass::text FROM auditoria WHERE id = ?`, id).Scan(&particionReal).Error; err != nil {
				t.Fatalf("no se pudo determinar la partición de %s: %v", caso.fecha, err)
			}
			if particionReal != caso.particion {
				t.Errorf("%s cayó en %s; se esperaba %s", caso.fecha, particionReal, caso.particion)
			}
		}

		// Reproduce las particiones creadas originalmente en una sesión con
		// zona horaria UTC-03 y conserva una fila en la franja de transición.
		for _, particion := range esperadas {
			if err := conn.Exec(fmt.Sprintf(`ALTER TABLE auditoria DETACH PARTITION %s`, particion)).Error; err != nil {
				t.Fatalf("no se pudo separar %s para simular el esquema anterior: %v", particion, err)
			}
			if err := conn.Exec(fmt.Sprintf(`DROP TABLE %s`, particion)).Error; err != nil {
				t.Fatalf("no se pudo retirar %s en la simulación: %v", particion, err)
			}
		}
		if err := conn.Exec(`
			CREATE TABLE auditoria_default PARTITION OF auditoria DEFAULT;
			CREATE TABLE auditoria_2026_q3 PARTITION OF auditoria
				FOR VALUES FROM ('2026-07-01 03:00:00+00') TO ('2026-10-01 03:00:00+00');
			CREATE TABLE auditoria_2026_q4 PARTITION OF auditoria
				FOR VALUES FROM ('2026-10-01 03:00:00+00') TO ('2027-01-01 03:00:00+00');
			CREATE TABLE auditoria_2027_q1 PARTITION OF auditoria
				FOR VALUES FROM ('2027-01-01 03:00:00+00') TO ('2027-04-01 03:00:00+00')
		`).Error; err != nil {
			t.Fatalf("no se pudo crear el estado previo de particiones: %v", err)
		}

		filasPrevias := []struct {
			fecha string
			en    string
		}{
			{"2026-07-15 12:00:00+00", "auditoria_2026_q3"},
			{"2026-10-01 01:00:00+00", "auditoria_2026_q3"},
			{"2026-10-15 12:00:00+00", "auditoria_2026_q4"},
			{"2027-01-01 01:00:00+00", "auditoria_2026_q4"},
			{"2027-01-15 12:00:00+00", "auditoria_2027_q1"},
		}
		idsPrevios := make([]uuid.UUID, 0, len(filasPrevias))
		for _, fila := range filasPrevias {
			id := uuid.New()
			idsPrevios = append(idsPrevios, id)
			if err := conn.Exec(`
				INSERT INTO auditoria (id, usuario_id, accion, resultado, fecha_hora)
				VALUES (?, ?, 'PREEXISTING', 'SUCCESS', ?)
			`, id, usuarioID, fila.fecha).Error; err != nil {
				t.Fatalf("no se pudo insertar fila previa de %s: %v", fila.fecha, err)
			}
			var particionReal string
			if err := conn.Raw(`SELECT tableoid::regclass::text FROM auditoria WHERE id = ?`, id).Scan(&particionReal).Error; err != nil {
				t.Fatalf("no se pudo determinar la partición previa de %s: %v", fila.fecha, err)
			}
			if particionReal != fila.en {
				t.Fatalf("%s cayó en %s antes de migrar; se esperaba %s", fila.fecha, particionReal, fila.en)
			}
		}
		if err := conn.Exec(migracion).Error; err != nil {
			t.Fatalf("la migración no corrigió los límites previos: %v", err)
		}
		for i, fila := range filasPrevias {
			var particionReal string
			if err := conn.Raw(`SELECT tableoid::regclass::text FROM auditoria WHERE id = ?`, idsPrevios[i]).Scan(&particionReal).Error; err != nil {
				t.Fatalf("no se pudo consultar la fila migrada de %s: %v", fila.fecha, err)
			}
			particionEsperada := "auditoria_2026_q3"
			if fila.fecha >= "2026-10-01" && fila.fecha < "2027-01-01" {
				particionEsperada = "auditoria_2026_q4"
			} else if fila.fecha >= "2027-01-01" {
				particionEsperada = "auditoria_2027_q1"
			}
			if particionReal != particionEsperada {
				t.Errorf("la fila de %s terminó en %s; se esperaba %s", fila.fecha, particionReal, particionEsperada)
			}
		}

		var limites []struct {
			Nombre string `gorm:"column:relname"`
			Limite string `gorm:"column:limite"`
		}
		if err := conn.Raw(`
			SELECT c.relname, pg_get_expr(c.relpartbound, c.oid) AS limite
			FROM pg_inherits i
			JOIN pg_class p ON p.oid = i.inhparent
			JOIN pg_namespace n ON n.oid = p.relnamespace
			JOIN pg_class c ON c.oid = i.inhrelid
			WHERE n.nspname = current_schema()
			  AND p.relname = 'auditoria'
			  AND c.relname IN ('auditoria_2026_q3', 'auditoria_2026_q4', 'auditoria_2027_q1')
			ORDER BY c.relname
		`).Scan(&limites).Error; err != nil {
			t.Fatalf("no se pudieron consultar los límites migrados: %v", err)
		}
		esperados := map[string]string{
			"auditoria_2026_q3": "FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-10-01 00:00:00+00')",
			"auditoria_2026_q4": "FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2027-01-01 00:00:00+00')",
			"auditoria_2027_q1": "FOR VALUES FROM ('2027-01-01 00:00:00+00') TO ('2027-04-01 00:00:00+00')",
		}
		for _, limite := range limites {
			if limite.Limite != esperados[limite.Nombre] {
				t.Errorf("%s tiene límites %q; se esperaba %q", limite.Nombre, limite.Limite, esperados[limite.Nombre])
			}
		}

		var indiceExiste int64
		if err := conn.Raw(`
		SELECT count(*) FROM pg_indexes
		WHERE schemaname = current_schema()
		  AND tablename = 'auditoria'
		  AND indexname = 'idx_auditoria_query'
		  AND indexdef LIKE '%(fecha_hora DESC, usuario_id, accion)%'
		`).Scan(&indiceExiste).Error; err != nil {
			t.Fatalf("no se pudo verificar el índice compuesto: %v", err)
		}
		if indiceExiste != 1 {
			t.Fatal("idx_auditoria_query no existe con las columnas esperadas")
		}

		plan := explicarConsulta(t, conn, `
		EXPLAIN (ANALYZE, COSTS OFF, FORMAT TEXT)
		SELECT * FROM auditoria
		WHERE fecha_hora >= '2026-10-15'
		  AND fecha_hora <  '2026-10-20'
		`)
		t.Logf("plan de pruning Q4 2026:\n%s", plan)
		if !strings.Contains(plan, "auditoria_2026_q4") {
			t.Fatalf("el plan no escaneó Q4 2026:\n%s", plan)
		}
		for _, omitida := range []string{"auditoria_2026_q3", "auditoria_2027_q1", "auditoria_default"} {
			if strings.Contains(plan, omitida) {
				t.Fatalf("partition pruning no omitió %s:\n%s", omitida, plan)
			}
		}
		if err := conn.Exec(`SELECT set_config('TimeZone', ?, false)`, zonaHorariaOriginal).Error; err != nil {
			t.Fatalf("no se pudo restaurar la zona horaria original: %v", err)
		}
		var zonaHorariaRestaurada string
		if err := conn.Raw(`SHOW TIME ZONE`).Scan(&zonaHorariaRestaurada).Error; err != nil {
			t.Fatalf("no se pudo verificar la zona horaria restaurada: %v", err)
		}
		if zonaHorariaRestaurada != zonaHorariaOriginal {
			t.Fatalf("la conexión conservó la zona %q; se esperaba restaurar %q", zonaHorariaRestaurada, zonaHorariaOriginal)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("falló la conexión PostgreSQL dedicada a la prueba: %v", err)
	}
}

func explicarConsulta(t *testing.T, db *gorm.DB, consulta string) string {
	t.Helper()
	filas, err := db.Raw(consulta).Rows()
	if err != nil {
		t.Fatalf("EXPLAIN ANALYZE falló: %v", err)
	}
	defer filas.Close()

	var lineas []string
	for filas.Next() {
		var linea string
		if err := filas.Scan(&linea); err != nil {
			t.Fatalf("no se pudo leer EXPLAIN ANALYZE: %v", err)
		}
		lineas = append(lineas, linea)
	}
	if err := filas.Err(); err != nil {
		t.Fatalf("error al recorrer EXPLAIN ANALYZE: %v", err)
	}
	return strings.Join(lineas, "\n")
}
