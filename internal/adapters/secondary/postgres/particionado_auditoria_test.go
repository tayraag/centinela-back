package postgres

import (
	"el-centinela/internal/core/ports"
	pg "gorm.io/driver/postgres"
	"time"

	"context"
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

func explicarConsulta(t *testing.T, db *gorm.DB, consulta string, args ...any) string {
	t.Helper()
	filas, err := db.Raw(consulta, args...).Rows()
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

func TestRF08FiltrosSQLPruning(t *testing.T) {
	db, err := gorm.Open(pg.New(pg.Config{DSN: "host=localhost user=test dbname=test sslmode=disable"}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	desde := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	hasta := time.Date(2026, 10, 20, 10, 0, 0, 123, time.UTC)
	for _, f := range []ports.FiltrosAuditoria{{}, {Desde: &desde, Hasta: &hasta, RecursoTipo: "VM", RecursoID: "105", Accion: "SNAPSHOT_CREATE", Resultado: "SUCCESS"}} {
		q := aplicarFiltros(db.Table("auditoria a"), f).Find(&[]map[string]any{})
		sql := q.Statement.SQL.String()
		if !strings.Contains(sql, "a.fecha_hora >= $") {
			t.Fatalf("sin pruning: %s", sql)
		}
		if f.Hasta != nil {
			for _, col := range []string{"a.fecha_hora <= $", "a.resource_type = $", "a.resource_id = $", "a.accion = $", "a.resultado = $"} {
				if !strings.Contains(sql, col) {
					t.Fatalf("falta %s en %s", col, sql)
				}
			}
			encontrado := false
			for _, v := range q.Statement.Vars {
				if fecha, ok := v.(*time.Time); ok && fecha.Equal(hasta) {
					encontrado = true
				}
			}
			if !encontrado {
				t.Fatalf("se alteró el instante hasta: %v", q.Statement.Vars)
			}
		}
	}
}

// Activación explícita: AUDIT_PERF_TEST=1 y DB_DSN de una base de pruebas.
// Todo el fixture vive en un esquema transaccional que se revierte.
func TestRF08VolumenPaginacionYPruning(t *testing.T) {
	if os.Getenv("AUDIT_PERF_TEST") != "1" {
		t.Skip("requiere AUDIT_PERF_TEST=1 para generar un millón de eventos")
	}
	db := conectarDBTest(t)
	err := db.Connection(func(conn *gorm.DB) error {
		tx := conn.Begin()
		if tx.Error != nil {
			return tx.Error
		}
		defer tx.Rollback()
		esquema := "rf08_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		if err := tx.Exec("CREATE SCHEMA " + esquema).Error; err != nil {
			return err
		}
		if err := tx.Exec("SET LOCAL search_path TO " + esquema).Error; err != nil {
			return err
		}
		ddl := `CREATE TABLE usuarios (id uuid PRIMARY KEY, organizacion_id uuid, nombre_completo text);
  INSERT INTO usuarios VALUES ('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002','admin');
  CREATE TABLE auditoria (organizacion_id uuid,id uuid,fecha_hora timestamptz,usuario_id uuid,accion text,resource_type text,resource_id text,resultado text,detalles jsonb) PARTITION BY RANGE(fecha_hora);
  CREATE TABLE auditoria_2026_q3 PARTITION OF auditoria FOR VALUES FROM ('2026-07-01T00:00:00Z') TO ('2026-10-01T00:00:00Z');
  CREATE TABLE auditoria_2026_q4 PARTITION OF auditoria FOR VALUES FROM ('2026-10-01T00:00:00Z') TO ('2027-01-01T00:00:00Z');
  CREATE INDEX idx_auditoria_query ON auditoria(fecha_hora DESC,usuario_id,accion);
  INSERT INTO auditoria (id,fecha_hora,usuario_id,accion,resource_type,resource_id,resultado,detalles) SELECT md5(n::text)::uuid,'2026-07-01T00:00:00Z'::timestamptz+(n%184)*interval '1 day'+(n%86400)*interval '1 second',
  '00000000-0000-0000-0000-000000000001'::uuid,CASE WHEN n%4=0 THEN 'SNAPSHOT_CREATE' ELSE 'CONFIG_UPDATED' END,'VM',(n%200)::text,'SUCCESS','{}'::jsonb
  FROM generate_series(1,1000000) n;
  ANALYZE auditoria; ANALYZE usuarios;`
		if err := tx.Exec(ddl).Error; err != nil {
			return err
		}
		desde := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		hasta := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC).Add(-time.Microsecond)
		usuario := uuid.MustParse("00000000-0000-0000-0000-000000000001")
		org := uuid.MustParse("00000000-0000-0000-0000-000000000002")
		filtros := ports.FiltrosAuditoria{Desde: &desde, Hasta: &hasta, UsuarioID: &usuario, Accion: ports.AccionCrearSnapshot, RecursoTipo: "VM", RecursoID: "100", Resultado: ports.ResultadoSuccess}
		opciones := ports.OpcionesAuditoria{Pagina: 2, TamanoPag: 25, OrdenarPor: "fechaHora", Direccion: "desc"}
		repo := NewAuditRepository(tx)
		// Calentar caches antes de medir; la medición incluye COUNT y SELECT reales.
		if _, err := repo.Listar(context.Background(), org, filtros, opciones); err != nil {
			return err
		}
		inicio := time.Now()
		pagina, err := repo.Listar(context.Background(), org, filtros, opciones)
		duracion := time.Since(inicio)
		if err != nil {
			return err
		}
		if pagina.Total == 0 || len(pagina.Items) != 25 {
			return fmt.Errorf("fixture vacío o página inválida: %+v", pagina)
		}
		// Conservar parámetros y precisión temporal: ToSQL interpola las fechas
		// para logging y puede perder la zona al volver a ejecutar ese texto.
		consulta := aplicarFiltros(tx.Session(&gorm.Session{DryRun: true}).Table("auditoria a").
			Select("a.*, CASE WHEN a.usuario_id IS NULL THEN '[sistema]' ELSE u.nombre_completo END AS nombre_completo").
			Joins("LEFT JOIN usuarios u ON u.id = a.usuario_id").
			Where("(a.organizacion_id = ? OR (a.organizacion_id IS NULL AND u.organizacion_id = ?))", org, org), filtros).
			Order("a.fecha_hora DESC, a.id DESC").Limit(25).Offset(25).Find(&[]map[string]any{}).Statement
		plan := explicarConsulta(t, tx, "EXPLAIN (ANALYZE, BUFFERS) "+consulta.SQL.String(), consulta.Vars...)
		t.Logf("COUNT + SELECT: %s; plan: %s", duracion, plan)
		if !strings.Contains(plan, "auditoria_2026_q4") || strings.Contains(plan, "auditoria_2026_q3") {
			return fmt.Errorf("pruning incorrecto: %s", plan)
		}
		if duracion >= 150*time.Millisecond {
			return fmt.Errorf("consulta excede 150 ms: %s", duracion)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
