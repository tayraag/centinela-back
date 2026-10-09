package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// usuarioConAccesos crea, dentro de tx, un usuario activo con una sesión
// activa, un código de recuperación vigente y un permiso de instancia.
func usuarioConAccesos(t *testing.T, tx *gorm.DB) (usuarioPrueba, uuid.UUID) {
	t.Helper()
	u, err := insertarUsuario(tx, "baja-"+uuid.NewString()+"@prueba.local", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	codigo, vence := "123456", time.Now().Add(10*time.Minute)
	if err := tx.Model(&domain.Usuario{}).Where("id = ?", u.id).
		Updates(map[string]any{"codigo_recuperacion": codigo, "expiracion_codigo": vence, "intentos_recuperacion": 2}).Error; err != nil {
		t.Fatal(err)
	}
	sesion := domain.SesionActiva{ID: uuid.New(), UsuarioID: u.id, JtiAccess: uuid.NewString(), JtiRefresh: uuid.NewString(),
		Activa: true, FechaExpiracion: time.Now().Add(time.Hour)}
	if err := tx.Create(&sesion).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Create(&domain.PermisoInstancia{UsuarioID: u.id, VmidProxmox: 110, NivelAcceso: "FULL_ACCESS"}).Error; err != nil {
		t.Fatal(err)
	}
	return u, sesion.ID
}

type estadoBaja struct {
	Activo             bool
	EliminadoEn        *time.Time
	CodigoRecuperacion *string
	SesionActiva       bool
	Permisos           int64
	Email              string
}

func leerEstadoBaja(tx *gorm.DB, id, sesionID uuid.UUID) estadoBaja {
	var e estadoBaja
	tx.Raw(`SELECT u.activo, u.eliminado_en, u.codigo_recuperacion, u.email_usuario AS email,
		(SELECT activa FROM sesiones_activas WHERE id = ?) AS sesion_activa,
		(SELECT count(*) FROM permisos_instancia WHERE usuario_id = u.id) AS permisos
		FROM usuarios u WHERE u.id = ?`, sesionID, id).Scan(&e)
	return e
}

// DELETE exitoso: eliminado_en en UTC, activo = false, OTP borrado y sesión
// invalidada en una transacción; revocar recibe la sesión. Se conservan el
// email histórico y los permisos (no hay borrado en cascada).
func TestEliminarLogicamente_Exitoso(t *testing.T) {
	tx := transaccionDescartable(t)
	repo := NewUserRepository(tx)
	u, sesionID := usuarioConAccesos(t, tx)

	var revocadas []uuid.UUID
	antes := time.Now()
	if err := repo.EliminarLogicamente(context.Background(), u.id, func(ids []uuid.UUID) error {
		revocadas = ids
		return nil
	}); err != nil {
		t.Fatalf("EliminarLogicamente: %v", err)
	}
	e := leerEstadoBaja(tx, u.id, sesionID)
	if e.Activo || e.EliminadoEn == nil || e.EliminadoEn.Before(antes.Add(-time.Second)) {
		t.Errorf("Debe quedar inactivo y con eliminado_en: %+v", e)
	}
	if e.CodigoRecuperacion != nil {
		t.Error("Debe borrarse el código de recuperación vigente")
	}
	if e.SesionActiva {
		t.Error("La sesión debe quedar invalidada")
	}
	if len(revocadas) != 1 || revocadas[0] != sesionID {
		t.Errorf("revocar debe recibir la sesión invalidada: %v", revocadas)
	}
	if e.Email != u.email || e.Permisos != 1 {
		t.Errorf("Se conservan el email histórico y los permisos: %+v", e)
	}

	// Una cuenta eliminada es inmutable.
	if err := repo.EliminarLogicamente(context.Background(), u.id, func([]uuid.UUID) error { return nil }); !errors.Is(err, ports.ErrUsuarioEliminado) {
		t.Errorf("Eliminarla de nuevo: se esperaba ErrUsuarioEliminado, vino %v", err)
	}
	if err := repo.ActualizarUsuario(context.Background(), u.id, map[string]any{"activo": true}); !errors.Is(err, ports.ErrUsuarioEliminado) {
		t.Errorf("Reactivarla: se esperaba ErrUsuarioEliminado, vino %v", err)
	}
	if e := leerEstadoBaja(tx, u.id, sesionID); e.Activo {
		t.Error("La cuenta eliminada no debe poder reactivarse")
	}
}

// Fail-closed: si revocar falla (Redis caído), no cambia nada.
func TestEliminarLogicamente_FalloDeRevocacionDeshaceTodo(t *testing.T) {
	tx := transaccionDescartable(t)
	repo := NewUserRepository(tx)
	u, sesionID := usuarioConAccesos(t, tx)

	err := repo.EliminarLogicamente(context.Background(), u.id, func([]uuid.UUID) error {
		return errors.New("redis: connection refused")
	})
	if !errors.Is(err, ports.ErrRevocacionFallida) {
		t.Fatalf("Se esperaba ErrRevocacionFallida, vino %v", err)
	}
	e := leerEstadoBaja(tx, u.id, sesionID)
	if !e.Activo || e.EliminadoEn != nil || e.CodigoRecuperacion == nil || !e.SesionActiva {
		t.Errorf("Con la revocación fallida el usuario debe quedar intacto: %+v", e)
	}
}

// Carrera en el alta: si dos INSERT con el mismo correo pasan la verificación
// previa, el segundo vuelve como ErrEmailYaRegistrado, no como error crudo de SQL.
func TestCrearUsuario_ColisionDeCorreoEsErrorDeNegocio(t *testing.T) {
	tx := transaccionDescartable(t)
	repo := NewUserRepository(tx)
	var orgTexto string
	tx.Raw(`SELECT id::text FROM organizaciones LIMIT 1`).Scan(&orgTexto)
	org := uuid.MustParse(orgTexto)
	correo := "carrera-" + uuid.NewString()[:8] + "@prueba.local"
	nuevo := func(email, username string) *domain.Usuario {
		return &domain.Usuario{ID: uuid.New(), OrganizacionID: org, NombreCompleto: "Carrera", NombreUsuario: username,
			EmailUsuario: email, ContrasenaHash: "x", Rol: "OPERATOR", Activo: true}
	}
	if err := repo.CrearUsuario(context.Background(), nuevo(correo, "c1-"+uuid.NewString()[:8])); err != nil {
		t.Fatal(err)
	}
	var err error
	intentar(tx, func() error {
		err = repo.CrearUsuario(context.Background(), nuevo(" "+correo, "c2-"+uuid.NewString()[:8]))
		return err
	})
	if !errors.Is(err, ports.ErrEmailYaRegistrado) {
		t.Errorf("Se esperaba ErrEmailYaRegistrado, vino %v", err)
	}
}
