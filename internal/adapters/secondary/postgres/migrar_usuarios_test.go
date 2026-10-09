package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// Todo corre dentro de una transacción que se descarta: la base local no cambia.
func transaccionDescartable(t *testing.T) *gorm.DB {
	t.Helper()
	tx := conectarDBTest(t).Begin()
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

// estadoPreMigracion deja usuarios como antes de este FIX: un índice único
// incondicional sobre email_usuario y sin el índice parcial ni el check.
//
// El índice viejo se simula sobre (email_usuario, id): la base local puede tener
// ya, legítimamente, varias filas eliminadas con el mismo correo, y entonces el
// UNIQUE(email_usuario) original no se podría crear. Para la migración es
// equivalente: es único, sin WHERE, y cubre email_usuario.
func estadoPreMigracion(t *testing.T, tx *gorm.DB) {
	t.Helper()
	for _, sql := range []string{
		`DROP INDEX IF EXISTS ` + indiceEmailParcial,
		`ALTER TABLE usuarios DROP CONSTRAINT IF EXISTS ` + checkEliminado,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_usuarios_email_usuario ON usuarios (email_usuario, id)`,
	} {
		if err := tx.Exec(sql).Error; err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
}

type usuarioPrueba struct {
	id    uuid.UUID
	email string
}

func insertarUsuario(tx *gorm.DB, email string, activo bool, eliminadoEn *time.Time) (usuarioPrueba, error) {
	id := uuid.New()
	err := tx.Exec(`
		INSERT INTO usuarios (id, organizacion_id, nombre_completo, nombre_usuario, email_usuario, contrasena_hash, rol, activo, eliminado_en)
		VALUES (?, (SELECT id FROM organizaciones LIMIT 1), 'Prueba', ?, ?, 'x', 'OPERATOR', ?, ?)`,
		id, "u-"+id.String()[:12], email, activo, eliminadoEn).Error
	return usuarioPrueba{id, email}, err
}

// intentar ejecuta fn dentro de un savepoint: una violación de unicidad no
// aborta el resto de la transacción del test.
func intentar(tx *gorm.DB, fn func() error) error {
	tx.SavePoint("intento")
	err := fn()
	if err != nil {
		tx.RollbackTo("intento")
	}
	return err
}

func esViolacionUnicidad(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == indiceEmailParcial
}

func huellaAuditoria(t *testing.T, tx *gorm.DB) string {
	t.Helper()
	var huella string
	tx.Raw(`SELECT count(*)::text || ':' || coalesce(md5(string_agg(id::text || accion || resultado || coalesce(detalles::text, '') || fecha_hora::text, ',' ORDER BY id)), '')
		FROM auditoria`).Scan(&huella)
	return huella
}

func registrarAuditoria(t *testing.T, tx *gorm.DB, accion string, detalles string, hace time.Duration) {
	t.Helper()
	if err := tx.Exec(`INSERT INTO auditoria (accion, resultado, detalles, fecha_hora) VALUES (?, 'EXITO', ?::jsonb, ?)`,
		accion, detalles, time.Now().Add(-hace)).Error; err != nil {
		t.Fatalf("insertar auditoría %s: %v", accion, err)
	}
}

func estadoDe(t *testing.T, tx *gorm.DB, id uuid.UUID) (activo bool, eliminado bool) {
	t.Helper()
	var fila struct {
		Activo      bool
		EliminadoEn *time.Time
	}
	tx.Raw(`SELECT activo, eliminado_en FROM usuarios WHERE id = ?`, id).Scan(&fila)
	return fila.Activo, fila.EliminadoEn != nil
}

// Plan de clasificación: solo pasa a eliminado el inactivo con un
// ELIMINAR_USUARIO sin reactivación posterior. Ante la duda, suspendido.
func TestMigrarUnicidadEmail_ClasificaSoloConEvidencia(t *testing.T) {
	tx := transaccionDescartable(t)
	estadoPreMigracion(t, tx)

	eliminado, _ := insertarUsuario(tx, "eliminado-"+uuid.NewString()+"@prueba.local", false, nil)
	reactivadoYSuspendido, _ := insertarUsuario(tx, "reactivado-"+uuid.NewString()+"@prueba.local", false, nil)
	suspendido, _ := insertarUsuario(tx, "suspendido-"+uuid.NewString()+"@prueba.local", false, nil)
	activoConEliminacion, _ := insertarUsuario(tx, "activo-"+uuid.NewString()+"@prueba.local", true, nil)

	registrarAuditoria(t, tx, "ELIMINAR_USUARIO", `{"usuarioEliminado":"`+eliminado.id.String()+`"}`, time.Hour)
	registrarAuditoria(t, tx, "ELIMINAR_USUARIO", `{"usuarioEliminado":"`+reactivadoYSuspendido.id.String()+`"}`, 3*time.Hour)
	registrarAuditoria(t, tx, "ACTUALIZAR_USUARIO", `{"usuarioAfectado":"`+reactivadoYSuspendido.id.String()+`","cambios":{"activo":true}}`, 2*time.Hour)
	registrarAuditoria(t, tx, "ACTUALIZAR_USUARIO", `{"usuarioAfectado":"`+reactivadoYSuspendido.id.String()+`","cambios":{"activo":false}}`, time.Hour)
	registrarAuditoria(t, tx, "ACTUALIZAR_USUARIO", `{"usuarioAfectado":"`+suspendido.id.String()+`","cambios":{"activo":false}}`, time.Hour)
	registrarAuditoria(t, tx, "ELIMINAR_USUARIO", `{"usuarioEliminado":"`+activoConEliminacion.id.String()+`"}`, time.Hour)

	huella := huellaAuditoria(t, tx)
	if err := migrarUnicidadEmail(tx); err != nil {
		t.Fatalf("migrarUnicidadEmail: %v", err)
	}

	casos := []struct {
		nombre            string
		usuario           usuarioPrueba
		eliminadoEsperado bool
	}{
		{"inactivo con ELIMINAR_USUARIO", eliminado, true},
		{"eliminado, reactivado y vuelto a suspender", reactivadoYSuspendido, false},
		{"suspendido por PUT, sin eliminación", suspendido, false},
		{"activo aunque tenga un ELIMINAR_USUARIO", activoConEliminacion, false},
	}
	for _, c := range casos {
		if _, eliminado := estadoDe(t, tx, c.usuario.id); eliminado != c.eliminadoEsperado {
			t.Errorf("%s: eliminado = %v, se esperaba %v", c.nombre, eliminado, c.eliminadoEsperado)
		}
	}
	if huellaAuditoria(t, tx) != huella {
		t.Error("La migración no debe modificar la auditoría")
	}
}

// DoD: el índice parcial deja repetir el correo entre eliminados, rechaza un
// duplicado entre no eliminados (aunque cambien mayúsculas o espacios) y un
// suspendido conserva la reserva de su correo.
func TestMigrarUnicidadEmail_IndiceParcial(t *testing.T) {
	tx := transaccionDescartable(t)
	estadoPreMigracion(t, tx)
	if err := migrarUnicidadEmail(tx); err != nil {
		t.Fatalf("migrarUnicidadEmail: %v", err)
	}
	ahora := time.Now()
	sufijo := uuid.NewString()[:8]

	// Varias generaciones eliminadas con el mismo correo, más una vigente.
	correo := "ana." + sufijo + "@prueba.local"
	for i := 0; i < 3; i++ {
		if err := intentar(tx, func() error { _, err := insertarUsuario(tx, correo, false, &ahora); return err }); err != nil {
			t.Fatalf("Generación eliminada %d con el mismo correo: %v", i+1, err)
		}
	}
	if err := intentar(tx, func() error { _, err := insertarUsuario(tx, correo, true, nil); return err }); err != nil {
		t.Fatalf("Una cuenta nueva debe poder reutilizar el correo de las eliminadas: %v", err)
	}
	for _, variante := range []string{correo, "ANA." + sufijo + "@PRUEBA.LOCAL", "  Ana." + sufijo + "@prueba.local  "} {
		err := intentar(tx, func() error { _, err := insertarUsuario(tx, variante, true, nil); return err })
		if !esViolacionUnicidad(err) {
			t.Errorf("%q duplica a una cuenta vigente: se esperaba violación de %s, vino %v", variante, indiceEmailParcial, err)
		}
	}

	// Un suspendido (activo = false, sin eliminado_en) sigue reservando su correo.
	suspendido := "suspendido." + sufijo + "@prueba.local"
	if _, err := insertarUsuario(tx, suspendido, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := intentar(tx, func() error { _, err := insertarUsuario(tx, " "+suspendido, true, nil); return err }); !esViolacionUnicidad(err) {
		t.Errorf("El correo de un suspendido debe seguir reservado: %v", err)
	}

	// Invariante: un eliminado no puede estar activo.
	err := intentar(tx, func() error { _, err := insertarUsuario(tx, "inv."+sufijo+"@prueba.local", true, &ahora); return err })
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != checkEliminado {
		t.Errorf("Un usuario eliminado y activo debe violar %s: %v", checkEliminado, err)
	}
}

// DoD: correr la migración otra vez (o reiniciar la API) no recrea el índice
// incondicional ni toca la auditoría.
func TestMigrarUnicidadEmail_Idempotente(t *testing.T) {
	tx := transaccionDescartable(t)
	estadoPreMigracion(t, tx)
	huella := huellaAuditoria(t, tx)
	for i := 0; i < 3; i++ {
		if err := migrarUnicidadEmail(tx); err != nil {
			t.Fatalf("corrida %d: %v", i+1, err)
		}
	}
	if viejos, err := indicesEmailIncondicionales(tx); err != nil || len(viejos) != 0 {
		t.Errorf("No debe quedar ningún índice único incondicional sobre el correo: %v %v", viejos, err)
	}
	if !existeIndice(tx, indiceEmailParcial) {
		t.Errorf("Falta %s", indiceEmailParcial)
	}
	if huellaAuditoria(t, tx) != huella {
		t.Error("La migración no debe modificar la auditoría")
	}
}

// Si hay correos repetidos entre no eliminados (el índice viejo distinguía
// mayúsculas), no se rompe el arranque: se conserva la unicidad anterior.
func TestMigrarUnicidadEmail_RepetidosConservanElIndiceViejo(t *testing.T) {
	tx := transaccionDescartable(t)
	estadoPreMigracion(t, tx)
	sufijo := uuid.NewString()[:8]
	// Como antes del FIX: el índice viejo distinguía mayúsculas.
	if _, err := insertarUsuario(tx, "Dup."+sufijo+"@prueba.local", true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := insertarUsuario(tx, "dup."+sufijo+"@prueba.local", true, nil); err != nil {
		t.Fatal(err)
	}
	if err := migrarUnicidadEmail(tx); err != nil {
		t.Fatalf("Con correos repetidos el arranque debe seguir: %v", err)
	}
	if existeIndice(tx, indiceEmailParcial) || !existeIndice(tx, "idx_usuarios_email_usuario") {
		t.Error("Con correos repetidos debe conservarse el índice viejo y no crearse el parcial")
	}
}
