package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
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
		Upid:            "UPID:privado:no-exponer",
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
	if len(recuperado.Detalles) == 0 {
		t.Errorf("Se esperaban detalles como objeto JSON, se obtuvo un valor vacío")
	}
	var detallesRecuperados map[string]any
	if err := json.Unmarshal(recuperado.Detalles, &detallesRecuperados); err != nil {
		t.Fatalf("detalles no es JSON válido: %v", err)
	}
	if detallesRecuperados["origen"] != "test_unitario" || detallesRecuperados["campo"] != "valor_prueba" {
		t.Errorf("el objeto detalles perdió su contenido: %v", detallesRecuperados)
	}
	serializado, err := json.Marshal(recuperado)
	if err != nil {
		t.Fatalf("no se pudo serializar el DTO público: %v", err)
	}
	if bytes.Contains(serializado, []byte("UPID:privado:no-exponer")) || bytes.Contains(serializado, []byte(`"upid"`)) {
		t.Fatalf("el DTO público expuso el UPID privado: %s", serializado)
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
	if len(pagina.Items[0].Detalles) != 0 {
		t.Errorf("Se esperaba Detalles vacío en el DTO, se obtuvo: %s", pagina.Items[0].Detalles)
	}
}

// TestAuditRepository_ExcluirAcciones verifica que el filtro de exclusión saque
// del listado las acciones indicadas (el ruido de conexión del canal SSE), sin
// tocar la tabla: la auditoría es append-only y este test solo lee.
func TestAuditRepository_ExcluirAcciones(t *testing.T) {
	db := conectarDBTest(t)
	repo := postgres.NewAuditRepository(db)
	ctx := context.Background()

	// Organización con más registros de auditoría, para que la prueba sea real.
	fila := db.Raw(`
		SELECT u.organizacion_id
		FROM auditoria a
		JOIN usuarios u ON u.id = a.usuario_id
		WHERE u.organizacion_id IS NOT NULL
		GROUP BY u.organizacion_id
		ORDER BY count(*) DESC
		LIMIT 1
	`).Row()
	var orgID uuid.UUID
	if fila == nil {
		t.Skip("no hay registros de auditoría con organización para probar")
	}
	if err := fila.Scan(&orgID); err != nil || orgID == uuid.Nil {
		t.Skipf("no hay registros de auditoría con organización para probar: %v", err)
	}

	opciones := ports.OpcionesAuditoria{Pagina: 1, TamanoPag: 200, OrdenarPor: "fechaHora", Direccion: "desc"}

	sinExclusion, err := repo.Listar(ctx, orgID, ports.FiltrosAuditoria{}, opciones)
	if err != nil {
		t.Fatalf("listado sin exclusión: %v", err)
	}

	conExclusion, err := repo.Listar(ctx, orgID, ports.FiltrosAuditoria{
		ExcluirAcciones: []string{ports.AccionEventosConexion},
	}, opciones)
	if err != nil {
		t.Fatalf("listado con exclusión: %v", err)
	}

	for _, item := range conExclusion.Items {
		if item.Accion == ports.AccionEventosConexion {
			t.Fatalf("la exclusión no filtró %s", item.Accion)
		}
	}

	if conExclusion.Total >= sinExclusion.Total {
		t.Fatalf("Total con exclusión (%d) debería ser menor que sin exclusión (%d)",
			conExclusion.Total, sinExclusion.Total)
	}
}

// Verifica alcance persistido, separación de destinatarios y fail-closed
// para registros históricos sin actor ni organización.
func TestAuditoriaAlertasSistemaAisladasPorOrganizacion(t *testing.T) {
	db := conectarDBTest(t)
	err := db.Connection(func(conn *gorm.DB) error {
		tx := conn.Begin()
		if tx.Error != nil {
			return tx.Error
		}
		defer tx.Rollback()
		esquema := "aud_scope_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		if err := tx.Exec("CREATE SCHEMA " + esquema).Error; err != nil {
			return err
		}
		if err := tx.Exec("SET LOCAL search_path TO " + esquema).Error; err != nil {
			return err
		}
		if err := tx.Exec(`CREATE TABLE usuarios (id uuid PRIMARY KEY,organizacion_id uuid,nombre_completo text,email_usuario text,rol text,activo boolean,eliminado_en timestamptz);
   CREATE TABLE permisos_instancia (id uuid,usuario_id uuid,vmid_proxmox integer,nivel_acceso text);
   CREATE TABLE auditoria (id uuid DEFAULT md5(random()::text || clock_timestamp()::text)::uuid,organizacion_id uuid,usuario_id uuid,accion text,instancia_id text,instancia_nombre text,resource_type text,resource_id text,upid text,resultado text,detalles jsonb,fecha_hora timestamptz);`).Error; err != nil {
			return err
		}
		orgs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()}
		actor := uuid.New()
		for i, org := range orgs {
			id := uuid.New()
			if i == 0 {
				id = actor
			}
			rol := "ADMIN"
			if i == 2 || i == 3 {
				rol = "OPERATOR"
			}
			if err := tx.Exec("INSERT INTO usuarios VALUES (?,?,?,?,?,?,NULL)", id, org, fmt.Sprintf("u%d", i), fmt.Sprintf("u%d@test", i), rol, i != 4).Error; err != nil {
				return err
			}
			if i == 2 || i == 3 {
				nivel := "FULL_ACCESS"
				if i == 3 {
					nivel = "READ_ONLY"
				}
				if err := tx.Exec("INSERT INTO permisos_instancia VALUES (?,?,110,?)", uuid.New(), id, nivel).Error; err != nil {
					return err
				}
			}
		}
		repo := postgres.NewAuditRepository(tx)
		for _, input := range []ports.RegistrarAuditoriaInput{
			{Accion: ports.AccionAlertaDisparada, RecursoTipo: ports.RecursoHost, RecursoID: "host", Resultado: ports.ResultadoSuccess, Detalles: map[string]any{"alertId": "host", "destinatarios": []string{"foreign@test"}}},
			{Accion: ports.AccionAlertaDisparada, RecursoTipo: ports.RecursoVM, RecursoID: "110", Resultado: ports.ResultadoSuccess, Detalles: map[string]any{"alertId": "vm"}},
			{Accion: ports.AccionAlertaDisparada, RecursoTipo: ports.RecursoLXC, RecursoID: "110", Resultado: ports.ResultadoSuccess, Detalles: map[string]any{"alertId": "lxc"}},
			{Accion: ports.AccionAlertaResuelta, RecursoTipo: ports.RecursoVM, RecursoID: "110", Resultado: ports.ResultadoSuccess},
			{UsuarioID: actor, Accion: ports.AccionLogin, Resultado: ports.ResultadoExito},
		} {
			if err := repo.Registrar(context.Background(), input); err != nil {
				return err
			}
		}
		if err := tx.Exec("INSERT INTO auditoria (accion,resultado,fecha_hora) VALUES ('ALERT_TRIGGERED','SUCCESS',now())").Error; err != nil {
			return err
		}
		// Cambiar permisos actuales no debe borrar la atribución histórica.
		if err := tx.Exec("DELETE FROM permisos_instancia").Error; err != nil {
			return err
		}
		for i, org := range orgs {
			esperadas := []int{5, 4, 3, 0, 0}[i]
			pagina, err := repo.Listar(context.Background(), org, ports.FiltrosAuditoria{}, ports.OpcionesAuditoria{Pagina: 1, TamanoPag: 25, Direccion: "desc"})
			if err != nil {
				return err
			}
			if int(pagina.Total) != esperadas || len(pagina.Items) != esperadas {
				return fmt.Errorf("org %d: total=%d items=%d esperado=%d", i, pagina.Total, len(pagina.Items), esperadas)
			}
			rows, err := repo.ExportarRegistros(context.Background(), org, ports.FiltrosAuditoria{})
			if err != nil {
				return err
			}
			if len(rows) != esperadas {
				return fmt.Errorf("exportación org %d: %d", i, len(rows))
			}
			for _, row := range rows {
				if row.UsuarioID != nil {
					continue
				}
				if row.NombreUsuario != "[sistema]" {
					return fmt.Errorf("actor inventado: %s", row.NombreUsuario)
				}
				if row.Accion == ports.AccionAlertaDisparada {
					var d struct{ Destinatarios []string }
					if err := json.Unmarshal(row.Detalles, &d); err != nil {
						return err
					}
					if len(d.Destinatarios) != 1 || d.Destinatarios[0] != fmt.Sprintf("u%d@test", i) {
						return fmt.Errorf("destinatarios ajenos para org %d: %v", i, d.Destinatarios)
					}
				}
			}
			vacia, err := repo.Listar(context.Background(), org, ports.FiltrosAuditoria{Accion: "NO_EXISTE"}, ports.OpcionesAuditoria{Pagina: 1, TamanoPag: 25})
			if err != nil {
				return err
			}
			if vacia.Total != 0 {
				return fmt.Errorf("el alcance omitió el filtro de acción")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
