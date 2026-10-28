package postgres_test

import (
	"context"
	handlers "el-centinela/internal/adapters/primary/http"
	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/infrastructure/crypto"
	"encoding/csv"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	db, err := gorm.Open(pg.Open(dsn+" search_path="+schema+",public"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); sqlDB.Close() })
	for _, ddl := range []string{
		`CREATE TABLE usuarios (id uuid PRIMARY KEY, organizacion_id uuid NOT NULL, nombre_completo text)`,
		`CREATE TABLE permisos_instancia (id uuid PRIMARY KEY DEFAULT public.uuid_generate_v7(), usuario_id uuid NOT NULL REFERENCES usuarios(id), vmid_proxmox integer NOT NULL, nivel_acceso varchar(30) NOT NULL, UNIQUE (usuario_id, vmid_proxmox))`,
		`CREATE TABLE tareas_asincronas (id uuid PRIMARY KEY, usuario_id uuid NOT NULL REFERENCES usuarios(id), upid_proxmox varchar(255) UNIQUE NOT NULL, instancia_id varchar(100), accion varchar(100), estado varchar(50) NOT NULL, metadatos jsonb, fecha_creacion timestamptz NOT NULL)`,
		`CREATE TABLE auditoria (organizacion_id uuid, id uuid NOT NULL DEFAULT public.uuid_generate_v7(), usuario_id uuid REFERENCES usuarios(id), accion varchar(100) NOT NULL, instancia_id varchar(100), instancia_nombre varchar(255), resource_type varchar(30), resource_id varchar(100), upid varchar(255), resultado varchar(50) NOT NULL, detalles jsonb, fecha_hora timestamptz NOT NULL, PRIMARY KEY (id, fecha_hora)) PARTITION BY RANGE (fecha_hora)`,
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
					if _, err := repo.ActualizarEstado(ctx, tarea.ID, ports.TareaCompleted, map[string]any{"exitstatus": "OK", "motivo": nil}, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, map[string]any{"exitstatus": "OK", "motivo": nil}, time.Now())); err != nil {
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
				if detalles["tareaId"] != tarea.ID.String() || reg.UsuarioID == nil || *reg.UsuarioID != usuario || reg.InstanciaNombre != "db-110" {
					t.Fatalf("correlación incorrecta: %+v", reg)
				}
				if _, expuesto := detalles["upid"]; expuesto {
					t.Fatalf("el UPID privado no debe exponerse en detalles: %v", detalles)
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
	if _, err := repo.ActualizarEstado(ctx, tarea.ID, ports.TareaCompleted, cierre, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, cierre, time.Now())); err == nil {
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
	if _, err := repo.ActualizarEstado(ctx, tarea.ID, ports.TareaCompleted, cierre, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, cierre, time.Now())); err != nil {
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
	if _, err := repo.ActualizarEstado(ctx, tarea.ID, ports.TareaFailed, map[string]any{"exitstatus": nil, "motivo": "TIMEOUT"}, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, map[string]any{"exitstatus": nil, "motivo": "TIMEOUT"}, time.Now())); err != nil {
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
	if detalles["motivo"] != "TIMEOUT" || detalles["exitstatus"] != nil || fin.Upid != tarea.UpidProxmox {
		t.Fatalf("timeout incorrecto: %v", detalles)
	}
	if _, expuesto := detalles["upid"]; expuesto {
		t.Fatalf("el UPID privado no debe exponerse en detalles: %v", detalles)
	}
}

func TestCreacionExitosaAutoasignaFullAccess(t *testing.T) {
	db, usuario, _ := baseRF08(t)
	repo, ctx := postgres.NewTareaRepository(db), context.Background()

	casos := []struct {
		accion string
		vmid   string
	}{
		{ports.AccionCrearVM, "210"},
		{ports.AccionProxmoxCrearLXC, "211"},
	}
	for _, caso := range casos {
		tarea := nuevaTareaRF08(usuario, caso.accion)
		tarea.InstanciaID = caso.vmid
		if err := repo.Crear(ctx, tarea, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, nil, time.Now())); err != nil {
			t.Fatal(err)
		}
		cierre := map[string]any{"exitstatus": "OK", "motivo": nil, "error": nil}
		if _, err := repo.ActualizarEstado(ctx, tarea.ID, ports.TareaCompleted, cierre, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, cierre, time.Now())); err != nil {
			t.Fatal(err)
		}

		var permiso domain.PermisoInstancia
		if err := db.Where("usuario_id = ? AND vmid_proxmox = ?", usuario, caso.vmid).First(&permiso).Error; err != nil {
			t.Fatalf("no se autoasignó el permiso para %s: %v", caso.accion, err)
		}
		if permiso.NivelAcceso != ports.NivelAccesoFullAccess {
			t.Fatalf("nivel de acceso = %q", permiso.NivelAcceso)
		}
	}
}

func TestCreacionGeneraDosAuditoriasCorrelacionadasPorUPID(t *testing.T) {
	db, usuario, _ := baseRF08(t)
	repo, ctx := postgres.NewTareaRepository(db), context.Background()
	meta := `{"instanciaNombre":"web-230","resource_type":"qemu","cores":4,"ramMb":4096,"discoGb":40}`
	tarea := nuevaTareaRF08(usuario, ports.AccionProxmoxCrearVM)
	tarea.InstanciaID, tarea.Metadatos = "230", &meta
	audit := services.NewAuditService(nil)
	if err := repo.Crear(ctx, tarea, audit.PrepararAuditoriaTarea(tarea, nil, time.Now())); err != nil {
		t.Fatal(err)
	}
	cierre := map[string]any{"exitstatus": "OK", "motivo": nil, "error": nil}
	if _, err := repo.ActualizarEstado(ctx, tarea.ID, ports.TareaCompleted, cierre, audit.PrepararAuditoriaTarea(tarea, cierre, time.Now())); err != nil {
		t.Fatal(err)
	}
	var registros []domain.Auditoria
	if err := db.Where("accion = ? AND upid = ?", ports.AccionCrearInstancia, tarea.UpidProxmox).Order("fecha_hora ASC").Find(&registros).Error; err != nil {
		t.Fatal(err)
	}
	if len(registros) != 2 || registros[0].Resultado != ports.ResultadoPendiente || registros[1].Resultado != ports.ResultadoSuccess {
		t.Fatalf("se esperaban exactamente despacho y desenlace: %+v", registros)
	}
}

func TestCreacionNoAsignaPermisoCuandoFalla(t *testing.T) {
	db, usuario, _ := baseRF08(t)
	repo, ctx := postgres.NewTareaRepository(db), context.Background()
	tarea := nuevaTareaRF08(usuario, ports.AccionProxmoxCrearVM)
	tarea.InstanciaID = strconv.Itoa(220)
	if err := repo.Crear(ctx, tarea, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, nil, time.Now())); err != nil {
		t.Fatal(err)
	}
	cierre := map[string]any{"exitstatus": "unable to create VM", "motivo": ports.MotivoProxmoxError}
	if _, err := repo.ActualizarEstado(ctx, tarea.ID, ports.TareaFailed, cierre, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, cierre, time.Now())); err != nil {
		t.Fatal(err)
	}
	var cantidad int64
	db.Model(&domain.PermisoInstancia{}).Where("usuario_id = ? AND vmid_proxmox = ?", usuario, tarea.InstanciaID).Count(&cantidad)
	if cantidad != 0 {
		t.Fatal("se creó un permiso para una creación fallida")
	}
}

func TestCreacionConAdvertenciasNoAutoasignaPermiso(t *testing.T) {
	db, usuario, _ := baseRF08(t)
	repo, ctx := postgres.NewTareaRepository(db), context.Background()
	tarea := nuevaTareaRF08(usuario, ports.AccionProxmoxCrearLXC)
	tarea.InstanciaID = "221"
	if err := repo.Crear(ctx, tarea, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, nil, time.Now())); err != nil {
		t.Fatal(err)
	}
	cierre := map[string]any{"exitstatus": "WARNINGS: 1", "motivo": nil, "error": nil}
	if _, err := repo.ActualizarEstado(ctx, tarea.ID, ports.TareaCompleted, cierre, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, cierre, time.Now())); err != nil {
		t.Fatal(err)
	}
	var cantidad int64
	if err := db.Model(&domain.PermisoInstancia{}).Where("usuario_id = ? AND vmid_proxmox = ?", usuario, 221).Count(&cantidad).Error; err != nil {
		t.Fatal(err)
	}
	if cantidad != 0 {
		t.Fatal("una creación sin exitstatus OK no debe recibir permisos")
	}
}

func TestCreacionHaceRollbackSiNoPuedeAsignarPermiso(t *testing.T) {
	db, usuario, _ := baseRF08(t)
	repo, ctx := postgres.NewTareaRepository(db), context.Background()
	tarea := nuevaTareaRF08(usuario, ports.AccionProxmoxCrearVM)
	if err := repo.Crear(ctx, tarea, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, nil, time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DROP TABLE permisos_instancia").Error; err != nil {
		t.Fatal(err)
	}
	cierre := map[string]any{"exitstatus": "OK", "motivo": nil, "error": nil}
	if _, err := repo.ActualizarEstado(ctx, tarea.ID, ports.TareaCompleted, cierre, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, cierre, time.Now())); err == nil {
		t.Fatal("el cierre debía fallar si no se puede autoasignar el permiso")
	}

	var persistida domain.TareaAsincrona
	if err := db.First(&persistida, "id = ?", tarea.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persistida.Estado != ports.TareaRunning {
		t.Fatalf("el rollback no dejó la tarea RUNNING: %s", persistida.Estado)
	}
	var auditorias int64
	db.Model(&domain.Auditoria{}).Where("instancia_id = ?", tarea.InstanciaID).Count(&auditorias)
	if auditorias != 1 {
		t.Fatalf("el rollback dejó auditoría final: total=%d", auditorias)
	}
}

func TestDestruccionExitosaPurgaPermisosYAuditaEnElMismoCommit(t *testing.T) {
	db, usuario, _ := baseRF08(t)
	repo, ctx := postgres.NewTareaRepository(db), context.Background()
	tarea := nuevaTareaRF08(usuario, "qmdestroy")
	if err := db.Create(&domain.PermisoInstancia{
		UsuarioID: usuario, VmidProxmox: 110, NivelAcceso: ports.NivelAccesoFullAccess,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.Crear(ctx, tarea, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, nil, time.Now())); err != nil {
		t.Fatal(err)
	}
	cierre := map[string]any{"exitstatus": "OK", "motivo": nil, "error": nil}
	afectados, err := repo.ActualizarEstado(ctx, tarea.ID, ports.TareaCompleted, cierre,
		services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, cierre, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if len(afectados) != 1 || afectados[0] != usuario {
		t.Fatalf("usuarios afectados = %v", afectados)
	}
	var permisos int64
	if err := db.Model(&domain.PermisoInstancia{}).Where("vmid_proxmox = ?", 110).Count(&permisos).Error; err != nil {
		t.Fatal(err)
	}
	if permisos != 0 {
		t.Fatalf("quedaron %d permisos huérfanos para el vmid 110", permisos)
	}
	var purga domain.Auditoria
	if err := db.Where("accion = ? AND instancia_id = ?", ports.AccionInstancePermissionsPurged, "110").First(&purga).Error; err != nil {
		t.Fatal(err)
	}
	if purga.Resultado != ports.ResultadoSuccess || purga.Detalles == nil {
		t.Fatalf("auditoría de purga incompleta: %+v", purga)
	}
	var detalles map[string]any
	if err := json.Unmarshal([]byte(*purga.Detalles), &detalles); err != nil {
		t.Fatal(err)
	}
	usuarios, ok := detalles["usuariosAfectados"].([]any)
	if detalles["vmid"] != float64(110) || !ok || len(usuarios) != 1 || usuarios[0] != usuario.String() {
		t.Fatalf("detalles de purga incorrectos: %v", detalles)
	}
}

func TestDestruccionFallidaNoPurgaPermisos(t *testing.T) {
	db, usuario, _ := baseRF08(t)
	repo, ctx := postgres.NewTareaRepository(db), context.Background()
	tarea := nuevaTareaRF08(usuario, "vzdestroy")
	if err := db.Create(&domain.PermisoInstancia{
		UsuarioID: usuario, VmidProxmox: 110, NivelAcceso: ports.NivelAccesoFullAccess,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.Crear(ctx, tarea, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, nil, time.Now())); err != nil {
		t.Fatal(err)
	}
	cierre := map[string]any{"exitstatus": "TASK ERROR: locked", "motivo": ports.MotivoProxmoxError}
	if afectados, err := repo.ActualizarEstado(ctx, tarea.ID, ports.TareaFailed, cierre,
		services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, cierre, time.Now())); err != nil || len(afectados) != 0 {
		t.Fatalf("cierre fallido inesperado: afectados=%v err=%v", afectados, err)
	}
	var permisos, purgas int64
	db.Model(&domain.PermisoInstancia{}).Where("vmid_proxmox = ?", 110).Count(&permisos)
	db.Model(&domain.Auditoria{}).Where("accion = ?", ports.AccionInstancePermissionsPurged).Count(&purgas)
	if permisos != 1 || purgas != 0 {
		t.Fatalf("un fallo alteró permisos o auditoría: permisos=%d purgas=%d", permisos, purgas)
	}
}

func TestDestruccionHaceRollbackSiFallaAuditoriaDePurga(t *testing.T) {
	db, usuario, _ := baseRF08(t)
	repo, ctx := postgres.NewTareaRepository(db), context.Background()
	tarea := nuevaTareaRF08(usuario, "delete")
	if err := db.Create(&domain.PermisoInstancia{
		UsuarioID: usuario, VmidProxmox: 110, NivelAcceso: ports.NivelAccesoFullAccess,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.Crear(ctx, tarea, services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, nil, time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`ALTER TABLE auditoria ADD CONSTRAINT simular_fallo_purga CHECK (accion <> 'INSTANCE_PERMISSIONS_PURGED')`).Error; err != nil {
		t.Fatal(err)
	}
	cierre := map[string]any{"exitstatus": "OK", "motivo": nil, "error": nil}
	if _, err := repo.ActualizarEstado(ctx, tarea.ID, ports.TareaCompleted, cierre,
		services.NewAuditService(nil).PrepararAuditoriaTarea(tarea, cierre, time.Now())); err == nil {
		t.Fatal("el cierre debía fallar junto con la auditoría de purga")
	}
	var persistida domain.TareaAsincrona
	if err := db.First(&persistida, "id = ?", tarea.ID).Error; err != nil {
		t.Fatal(err)
	}
	var permisos int64
	db.Model(&domain.PermisoInstancia{}).Where("vmid_proxmox = ?", 110).Count(&permisos)
	if persistida.Estado != ports.TareaRunning || permisos != 1 {
		t.Fatalf("rollback incompleto: estado=%s permisos=%d", persistida.Estado, permisos)
	}
}

// Exporta más de 100000 filas (también detecta el límite histórico), con
// PostgreSQL y HTTP reales, sin almacenar el archivo en el cliente de prueba.
// RF08_TEST_DSN apunta a Docker local; baseRF08 aísla y elimina su esquema.
func TestRF08ExportacionStreamingVolumenYCancelacion(t *testing.T) {
	db, usuario, org := baseRF08(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	ahora := time.Now().UTC()
	const total = 100501
	if err := db.Exec(`INSERT INTO auditoria (usuario_id,accion,instancia_id,resource_type,resource_id,resultado,detalles,fecha_hora)
 SELECT ?, 'EXPORT_STREAM','110','VM','110','SUCCESS',jsonb_build_object('mensaje','área,"prueba"','payload',repeat('x',384)), ?::timestamptz - n * interval '1 microsecond'
 FROM generate_series(1,100501) n`, usuario, ahora).Error; err != nil {
		t.Fatal(err)
	}
	refs, err := crypto.NuevoOfuscadorVMID("salt-streaming-pruebas-al-menos-32-caracteres")
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := refs.OfuscarVMID(110)
	svc := services.NewAuditService(postgres.NewAuditRepository(db), refs)
	router := gin.New()
	router.Use(middleware.RequestLogger())
	router.Use(func(c *gin.Context) { c.Set(middleware.ContextKeyRol, "ADMIN"); c.Set(middleware.ContextKeyOrgID, org) })
	router.GET("/api/admin/audit/export", middleware.RequireRole("ADMIN"), handlers.NewAuditHandler(svc).ExportarAuditoria)
	servidor := httptest.NewServer(router)
	defer servidor.Close()
	cliente := &http.Client{Timeout: 2 * time.Minute}
	runtime.GC()
	var peak atomic.Uint64
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				for prev := peak.Load(); m.HeapAlloc > prev; prev = peak.Load() {
					if peak.CompareAndSwap(prev, m.HeapAlloc) {
						break
					}
				}
			}
		}
	}()
	defer func() { close(stop); <-done }()
	query := "&accion=EXPORT_STREAM&usuarioId=" + usuario.String() + "&recursoTipo=VM&recursoId=" + ref + "&desde=" + url.QueryEscape(ahora.Add(-time.Hour).Format(time.RFC3339Nano))
	for _, formato := range []string{"csv", "json"} {
		respuesta, err := cliente.Get(servidor.URL + "/api/admin/audit/export?format=" + formato + query)
		if err != nil {
			t.Fatal(err)
		}
		defer respuesta.Body.Close()
		if respuesta.StatusCode != 200 {
			respuesta.Body.Close()
			t.Fatalf("status=%d", respuesta.StatusCode)
		}
		cantidad := 0
		if formato == "csv" {
			lector := csv.NewReader(respuesta.Body)
			lector.ReuseRecord = true
			header, err := lector.Read()
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimPrefix(header[0], "\xef\xbb\xbf") != "id" || len(header) != 8 {
				t.Fatal(header)
			}
			for {
				fila, err := lector.Read()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				cantidad++
				if len(fila) != 8 || fila[5] != ref || !strings.Contains(fila[7], "área") {
					t.Fatalf("CSV privado/corrupto: %v", fila)
				}
			}
		} else {
			lector := json.NewDecoder(respuesta.Body)
			token, err := lector.Token()
			if err != nil || token != json.Delim('[') {
				t.Fatalf("inicio JSON: %v %v", token, err)
			}
			for lector.More() {
				var fila handlers.ItemAuditoriaResponse
				if err := lector.Decode(&fila); err != nil {
					t.Fatal(err)
				}
				cantidad++
				if fila.RecursoID != ref || !strings.Contains(string(fila.Detalle), "área") {
					t.Fatal("JSON privado/corrupto")
				}
			}
			token, err = lector.Token()
			if err != nil || token != json.Delim(']') {
				t.Fatalf("cierre JSON: %v %v", token, err)
			}
			var sobrante any
			if err := lector.Decode(&sobrante); err != io.EOF {
				t.Fatal("datos extra tras JSON")
			}
		}
		respuesta.Body.Close()
		if cantidad != total {
			t.Fatalf("%s: se truncó el dataset: %d", formato, cantidad)
		}
		t.Logf("%s: %d filas completas", formato, cantidad)
	}
	// Cancelar una descarga real; un pool de una sola conexión detecta cursores
	// retenidos porque el siguiente Ping quedaría bloqueado.
	respuesta, err := cliente.Get(servidor.URL + "/api/admin/audit/export?format=json" + query)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(io.Discard, respuesta.Body, 64); err != nil {
		t.Fatal(err)
	}
	respuesta.Body.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		t.Fatalf("la cancelación retuvo la conexión: %v", err)
	}
	if sqlDB.Stats().InUse != 0 {
		t.Fatal("quedó una conexión en uso")
	}
	if peak.Load() > 50_000_000 {
		t.Fatalf("heap superior a 50 MB: %d", peak.Load())
	}
	t.Logf("heap máximo muestreado: %.2f MB", float64(peak.Load())/1e6)
	// En el contenedor de prueba este valor mide memoria del cgroup, no sólo
	// heap Go. El límite de Docker detecta además un OOM durante la descarga.
	if dato, err := os.ReadFile("/sys/fs/cgroup/memory.peak"); err == nil {
		memoria, err := strconv.ParseUint(strings.TrimSpace(string(dato)), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("memoria máxima del contenedor: %.2f MB", float64(memoria)/1e6)
		if memoria > 50_000_000 {
			t.Fatalf("memoria de contenedor superior a 50 MB: %d", memoria)
		}
	}
}
