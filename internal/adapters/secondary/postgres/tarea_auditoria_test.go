package postgres_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/postgres"
	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"
	"github.com/google/uuid"
	pg "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Usa exclusivamente RF08_TEST_DSN (formato keyword), nunca el .env del proyecto.
// Las tablas de prueba se crean en un esquema aislado y se eliminan al terminar.
func baseRF08(t *testing.T) (*gorm.DB, uuid.UUID, uuid.UUID) {
	t.Helper()
	dsn := os.Getenv("RF08_TEST_DSN")
	if dsn == "" {
		t.Skip("RF08_TEST_DSN no configurado")
	}
	admin, err := gorm.Open(pg.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	schema := "rf08_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec("DROP SCHEMA " + schema + " CASCADE")
		sqlDB, _ := admin.DB()
		sqlDB.Close()
	})
	db, err := gorm.Open(pg.Open(dsn+" search_path="+schema), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); sqlDB.Close() })
	for _, ddl := range []string{
		`CREATE TABLE usuarios (id uuid PRIMARY KEY, organizacion_id uuid NOT NULL, nombre_completo text)`,
		`CREATE TABLE tareas_asincronas (id uuid PRIMARY KEY, usuario_id uuid NOT NULL REFERENCES usuarios(id), upid_proxmox varchar(255) UNIQUE NOT NULL, instancia_id varchar(100), accion varchar(100), estado varchar(50) NOT NULL, metadatos jsonb, fecha_creacion timestamptz NOT NULL)`,
		`CREATE TABLE auditoria (id uuid NOT NULL DEFAULT uuidv7(), usuario_id uuid REFERENCES usuarios(id), accion varchar(100) NOT NULL, instancia_id varchar(100), instancia_nombre varchar(255), resultado varchar(50) NOT NULL, detalles jsonb, fecha_hora timestamptz NOT NULL, PRIMARY KEY (id, fecha_hora)) PARTITION BY RANGE (fecha_hora)`,
		`CREATE TABLE auditoria_actual PARTITION OF auditoria FOR VALUES FROM ('2020-01-01') TO ('2100-01-01')`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	usuario, org := uuid.New(), uuid.New()
	if err := db.Exec("INSERT INTO usuarios VALUES (?, ?, 'Operador RF08')", usuario, org).Error; err != nil {
		t.Fatal(err)
	}
	return db, usuario, org
}

func nuevaTareaRF08(usuario uuid.UUID, accion string) *domain.TareaAsincrona {
	meta := `{"instanciaNombre":"db-110","resource_type":"lxc"}`
	return &domain.TareaAsincrona{ID: uuid.New(), UsuarioID: usuario, UpidProxmox: "UPID:" + uuid.NewString(), InstanciaID: "110", Accion: accion, Estado: ports.TareaRunning, Metadatos: &meta, FechaCreacion: time.Now().Add(-2 * time.Second)}
}

func TestRF08ParticionesCorrelacionFiltrosYCierreConcurrente(t *testing.T) {
	db, usuario, org := baseRF08(t)
	repo, audit := postgres.NewTareaRepository(db), postgres.NewAuditRepository(db)
	ctx := context.Background()
	for accion, codigo := range map[string]string{"start": "INICIAR_VM", "stop": "DETENER_VM", "shutdown": "APAGAR_VM", "reboot": "REINICIAR_VM", "delete": "ELIMINAR_VM"} {
		t.Run(accion, func(t *testing.T) {
			tarea := nuevaTareaRF08(usuario, accion)
			if err := repo.Crear(ctx, tarea, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, nil, time.Now())); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			for i := 0; i < 12; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := repo.ActualizarEstado(ctx, tarea.ID, ports.TareaCompleted, map[string]any{"exitstatus": "OK", "motivo": nil}, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, map[string]any{"exitstatus": "OK", "motivo": nil}, time.Now())); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			pagina, err := audit.Listar(ctx, org, ports.FiltrosAuditoria{Accion: codigo}, ports.OpcionesAuditoria{Pagina: 1, TamanoPag: 50, OrdenarPor: "fechaHora", Direccion: "asc"})
			if err != nil {
				t.Fatal(err)
			}
			if pagina.Total != 2 || len(pagina.Items) != 2 {
				t.Fatalf("se esperaban exactamente dos registros: %+v", pagina)
			}
			if pagina.Items[0].Resultado != "PENDIENTE" || pagina.Items[1].Resultado != "SUCCESS" {
				t.Fatalf("resultados incorrectos: %+v", pagina.Items)
			}
			for _, reg := range pagina.Items {
				var detalles map[string]any
				if err := json.Unmarshal([]byte(reg.Detalles), &detalles); err != nil {
					t.Fatal(err)
				}
				if detalles["upid"] != tarea.UpidProxmox || detalles["tareaId"] != tarea.ID.String() || reg.UsuarioID == nil || *reg.UsuarioID != usuario || reg.InstanciaNombre != "db-110" {
					t.Fatalf("correlación incorrecta: %+v", reg)
				}
				if reg.Resultado == "SUCCESS" && detalles["duracionSegundos"].(float64) < 2 {
					t.Fatalf("duración incorrecta: %v", detalles)
				}
			}
			filtrada, err := audit.Listar(ctx, org, ports.FiltrosAuditoria{Accion: codigo, Resultado: "SUCCESS"}, ports.OpcionesAuditoria{Pagina: 1, TamanoPag: 50, Direccion: "asc"})
			if err != nil || filtrada.Total != 1 {
				t.Fatalf("filtro resultado: %+v, %v", filtrada, err)
			}
		})
	}
}

func TestRF08RollbackYReintento(t *testing.T) {
	db, usuario, _ := baseRF08(t)
	repo, ctx := postgres.NewTareaRepository(db), context.Background()
	tarea := nuevaTareaRF08(usuario, "start")
	if err := db.Exec(`ALTER TABLE auditoria ADD CONSTRAINT simular_fallo CHECK (resultado <> 'PENDIENTE')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.Crear(ctx, tarea, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, nil, time.Now())); err == nil {
		t.Fatal("debió fallar el despacho")
	}
	var count int64
	db.Model(&domain.TareaAsincrona{}).Count(&count)
	if count != 0 {
		t.Fatal("la tarea quedó sin auditoría inicial")
	}
	db.Exec(`ALTER TABLE auditoria DROP CONSTRAINT simular_fallo`)
	if err := repo.Crear(ctx, tarea, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, nil, time.Now())); err != nil {
		t.Fatal(err)
	}
	db.Exec(`ALTER TABLE auditoria ADD CONSTRAINT simular_fallo CHECK (resultado <> 'SUCCESS')`)
	cierre := map[string]any{"exitstatus": "OK", "motivo": nil}
	if err := repo.ActualizarEstado(ctx, tarea.ID, ports.TareaCompleted, cierre, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, cierre, time.Now())); err == nil {
		t.Fatal("debió fallar el cierre")
	}
	var persistida domain.TareaAsincrona
	if err := db.First(&persistida, "id = ?", tarea.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persistida.Estado != ports.TareaRunning {
		t.Fatal("el cierre fallido no dejó RUNNING para reintento")
	}
	db.Exec(`ALTER TABLE auditoria DROP CONSTRAINT simular_fallo`)
	if err := repo.ActualizarEstado(ctx, tarea.ID, ports.TareaCompleted, cierre, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, cierre, time.Now())); err != nil {
		t.Fatal(err)
	}
	db.Model(&domain.Auditoria{}).Count(&count)
	if count != 2 {
		t.Fatalf("registros luego del reintento: %d", count)
	}
}

func TestRF08TimeoutPersistido(t *testing.T) {
	db, usuario, _ := baseRF08(t)
	repo, ctx := postgres.NewTareaRepository(db), context.Background()
	tarea := nuevaTareaRF08(usuario, "delete")
	if err := repo.Crear(ctx, tarea, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, nil, time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := repo.ActualizarEstado(ctx, tarea.ID, ports.TareaFailed, map[string]any{"exitstatus": nil, "motivo": "TIMEOUT"}, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, map[string]any{"exitstatus": nil, "motivo": "TIMEOUT"}, time.Now())); err != nil {
		t.Fatal(err)
	}
	var fin domain.Auditoria
	if err := db.Where("resultado = ?", "FAILED").First(&fin).Error; err != nil {
		t.Fatal(err)
	}
	var detalles map[string]any
	if fin.Detalles == nil {
		t.Fatal("sin detalles")
	}
	if err := json.Unmarshal([]byte(*fin.Detalles), &detalles); err != nil {
		t.Fatal(err)
	}
	if detalles["motivo"] != "TIMEOUT" || detalles["exitstatus"] != nil || detalles["upid"] != tarea.UpidProxmox {
		t.Fatalf("timeout incorrecto: %v", detalles)
	}
}
