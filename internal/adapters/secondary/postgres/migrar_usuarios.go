package postgres

import (
	"fmt"
	"log"
	"strings"

	"gorm.io/gorm"
)

// Unicidad del correo y eliminación lógica de usuarios.
//
// Antes, el correo tenía un índice único incondicional (idx_usuarios_email_usuario,
// creado por el tag uniqueIndex de GORM) y "eliminar" un usuario solo lo
// desactivaba: su fila se conserva por la auditoría, así que el correo quedaba
// bloqueado para siempre. Ahora:
//
//   - eliminado_en distingue la eliminación lógica (DELETE, irreversible) de la
//     suspensión (activo = false por PUT, reversible).
//   - El correo es único solo entre los no eliminados, sin distinguir mayúsculas
//     ni espacios: índice parcial uq_usuarios_email_activo_lower. Un suspendido
//     sigue reservando su correo; un eliminado lo libera.
//   - ck_usuarios_eliminado_inactivo: todo eliminado está inactivo.
//
// Todo es idempotente: en una base ya migrada no cambia nada ni escribe en el log.
// La auditoría solo se lee, nunca se modifica.

const (
	indiceEmailParcial = "uq_usuarios_email_activo_lower"
	checkEliminado     = "ck_usuarios_eliminado_inactivo"
)

func migrarUnicidadEmail(db *gorm.DB) error {
	if err := db.Exec(`ALTER TABLE usuarios ADD COLUMN IF NOT EXISTS eliminado_en TIMESTAMPTZ NULL`).Error; err != nil {
		return fmt.Errorf("error al agregar usuarios.eliminado_en: %w", err)
	}
	if err := clasificarEliminados(db); err != nil {
		return err
	}
	if err := asegurarCheckEliminado(db); err != nil {
		return err
	}
	return reemplazarIndiceEmail(db)
}

// clasificarEliminados marca eliminado_en solo en los usuarios inactivos con
// evidencia explícita en la auditoría: un ELIMINAR_USUARIO exitoso con
// detalles.usuarioEliminado, sin una reactivación posterior (ACTUALIZAR_USUARIO
// con cambios.activo = true). Usa la fecha de esa eliminación. Los demás
// inactivos quedan como suspendidos (eliminado_en NULL): ante la duda, el correo
// sigue reservado y un admin lo resuelve a mano.
func clasificarEliminados(db *gorm.DB) error {
	eventos := `SELECT accion, resultado, detalles, fecha_hora FROM auditoria`
	if existeTabla(db, "auditoria_legacy") {
		eventos += ` UNION ALL SELECT accion, resultado, detalles, fecha_hora FROM auditoria_legacy`
	}
	resultado := db.Exec(`
		WITH eventos AS (` + eventos + `),
		eliminaciones AS (
			SELECT detalles->>'usuarioEliminado' AS usuario_id, max(fecha_hora) AS fecha
			FROM eventos
			WHERE accion = 'ELIMINAR_USUARIO' AND resultado = 'EXITO' AND detalles->>'usuarioEliminado' IS NOT NULL
			GROUP BY 1
		)
		UPDATE usuarios u SET eliminado_en = e.fecha
		FROM eliminaciones e
		WHERE u.id::text = e.usuario_id
		  AND u.eliminado_en IS NULL
		  AND u.activo = false
		  AND NOT EXISTS (
			SELECT 1 FROM eventos r
			WHERE r.accion = 'ACTUALIZAR_USUARIO'
			  AND r.detalles->>'usuarioAfectado' = e.usuario_id
			  AND r.detalles->'cambios'->>'activo' = 'true'
			  AND r.fecha_hora > e.fecha
		  )`)
	if resultado.Error != nil {
		return fmt.Errorf("error al clasificar usuarios eliminados: %w", resultado.Error)
	}
	if resultado.RowsAffected > 0 {
		log.Printf("🗂️  usuarios: %d marcado(s) como eliminados según la auditoría (ELIMINAR_USUARIO sin reactivación posterior); el resto de los inactivos quedan suspendidos", resultado.RowsAffected)
	}
	return nil
}

// asegurarCheckEliminado agrega el constraint eliminado ⇒ inactivo. Se crea NOT
// VALID y después se valida: si hubiera filas viejas que no lo cumplen, el
// arranque sigue (con un aviso) y el constraint igual rige para toda escritura nueva.
func asegurarCheckEliminado(db *gorm.DB) error {
	var existe int64
	if err := db.Raw(`SELECT count(*) FROM pg_constraint WHERE conname = ?`, checkEliminado).Scan(&existe).Error; err != nil {
		return fmt.Errorf("error al inspeccionar %s: %w", checkEliminado, err)
	}
	if existe > 0 {
		return nil
	}
	if err := db.Exec(`ALTER TABLE usuarios ADD CONSTRAINT ` + checkEliminado +
		` CHECK (eliminado_en IS NULL OR activo = false) NOT VALID`).Error; err != nil {
		return fmt.Errorf("error al crear %s: %w", checkEliminado, err)
	}
	if err := db.Exec(`ALTER TABLE usuarios VALIDATE CONSTRAINT ` + checkEliminado).Error; err != nil {
		log.Printf("⚠️  usuarios: hay filas eliminadas y activas a la vez; %s rige solo para cambios nuevos hasta corregirlas: %v", checkEliminado, err)
	}
	return nil
}

// reemplazarIndiceEmail borra todo índice o constraint único incondicional sobre
// email_usuario (el idx_usuarios_email_usuario de GORM y los nombres alternativos
// uq_usuarios_email, idx_usuarios_email, uq_usuarios_email_lower) y crea el
// índice parcial, todo en una transacción.
//
// Si entre los no eliminados hay correos repetidos (por ejemplo, "Ana@x" y
// "ana@x", que el índice viejo permitía), el índice parcial no se puede crear:
// se deja el índice viejo, se avisa en el log cuáles son y el arranque sigue.
func reemplazarIndiceEmail(db *gorm.DB) error {
	viejos, err := indicesEmailIncondicionales(db)
	if err != nil {
		return err
	}
	if len(viejos) == 0 && existeIndice(db, indiceEmailParcial) {
		return nil // ya migrado
	}

	var repetidos []string
	if err := db.Raw(`
		SELECT lower(btrim(email_usuario)) FROM usuarios
		WHERE eliminado_en IS NULL
		GROUP BY 1 HAVING count(*) > 1
		ORDER BY 1`).Scan(&repetidos).Error; err != nil {
		return fmt.Errorf("error al buscar correos repetidos: %w", err)
	}
	if len(repetidos) > 0 {
		log.Printf("⚠️  usuarios: no se creó %s porque estos correos se repiten entre usuarios no eliminados: %s. "+
			"Se mantiene la unicidad anterior hasta que un admin los resuelva (cambiar el correo o eliminar la cuenta sobrante).",
			indiceEmailParcial, strings.Join(repetidos, ", "))
		return nil
	}

	err = db.Transaction(func(tx *gorm.DB) error {
		for _, viejo := range viejos {
			sentencia := `DROP INDEX IF EXISTS ` + viejo.indice
			if viejo.constraint != "" {
				sentencia = `ALTER TABLE usuarios DROP CONSTRAINT IF EXISTS ` + viejo.constraint
			}
			if err := tx.Exec(sentencia).Error; err != nil {
				return fmt.Errorf("error al borrar %s: %w", viejo.indice, err)
			}
		}
		if err := tx.Exec(`
			CREATE UNIQUE INDEX IF NOT EXISTS ` + indiceEmailParcial + `
			ON usuarios (lower(btrim(email_usuario)))
			WHERE eliminado_en IS NULL`).Error; err != nil {
			return fmt.Errorf("error al crear %s: %w", indiceEmailParcial, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	nombres := make([]string, len(viejos))
	for i, v := range viejos {
		nombres[i] = v.indice
	}
	log.Printf("🔑 usuarios: unicidad del correo migrada a %s (solo no eliminados, sin distinguir mayúsculas ni espacios); se quitó: %s",
		indiceEmailParcial, strings.Join(nombres, ", "))
	return nil
}

type indiceViejo struct {
	indice     string
	constraint string // si el índice respalda un constraint UNIQUE, se borra el constraint
}

// indicesEmailIncondicionales lista los índices únicos de usuarios que cubren
// email_usuario sin predicado WHERE, más los nombres alternativos conocidos.
func indicesEmailIncondicionales(db *gorm.DB) ([]indiceViejo, error) {
	var filas []struct {
		Indice     string
		Constraint *string
	}
	err := db.Raw(`
		SELECT i.relname AS indice, c.conname AS constraint
		FROM pg_index x
		JOIN pg_class i ON i.oid = x.indexrelid
		JOIN pg_class t ON t.oid = x.indrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		LEFT JOIN pg_constraint c ON c.conindid = x.indexrelid AND c.contype = 'u'
		WHERE t.relname = 'usuarios' AND n.nspname = current_schema()
		  AND x.indisunique AND NOT x.indisprimary
		  AND x.indpred IS NULL
		  AND (pg_get_indexdef(x.indexrelid) LIKE '%email_usuario%'
		       OR i.relname IN ('uq_usuarios_email', 'idx_usuarios_email', 'uq_usuarios_email_lower'))
		ORDER BY 1`).Scan(&filas).Error
	if err != nil {
		return nil, fmt.Errorf("error al inspeccionar índices de usuarios: %w", err)
	}
	viejos := make([]indiceViejo, 0, len(filas))
	for _, f := range filas {
		v := indiceViejo{indice: f.Indice}
		if f.Constraint != nil {
			v.constraint = *f.Constraint
		}
		viejos = append(viejos, v)
	}
	return viejos, nil
}

func existeIndice(db *gorm.DB, nombre string) bool {
	var n int64
	db.Raw(`SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`, nombre).Scan(&n)
	return n > 0
}

func existeTabla(db *gorm.DB, nombre string) bool {
	var n int64
	db.Raw(`SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = ? AND n.nspname = current_schema()`, nombre).Scan(&n)
	return n > 0
}
