package postgres

import (
	"fmt"
	"log"
	"os"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/infrastructure/crypto"
	"github.com/google/uuid"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// InitDB abre la conexión a PostgreSQL y crea las tablas automáticamente
func InitDB() (*gorm.DB, error) {
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		// Compatibilidad con la configuracion anterior del despliegue, que
		// definia la cadena de conexion en DATABASE_URL.
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		dsn = "host=localhost user=centinela_admin password=centinela_password dbname=centinela_db port=5433 sslmode=disable TimeZone=America/Argentina/Buenos_Aires"
		log.Println("⚠️  Ni DB_DSN ni DATABASE_URL en el entorno: usando configuración local por defecto")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("error al conectar con PostgreSQL: %w", err)
	}

	log.Println("✅ Conexión exitosa a PostgreSQL")

	if err := migrarSesionesAUnaFilaPorSesion(db); err != nil {
		return nil, err
	}

	log.Println("🔄 Ejecutando auto-migración de tablas...")

	err = db.AutoMigrate(
		&domain.Organizacion{},
		&domain.Usuario{},
		&domain.SesionActiva{},
		&domain.PermisoInstancia{},
		// domain.Auditoria NO va en AutoMigrate: la tabla es particionada por
		// rango temporal. Se crea con SQL raw en migrarAuditoriaParticionada().
		&domain.TareaAsincrona{},
		&domain.Notificacion{},
	)
	if err != nil {
		return nil, fmt.Errorf("error al migrar la base de datos: %w", err)
	}
	log.Println("✅ Migración completada exitosamente")

	// Crear tabla auditoria particionada e índices locales (idempotente)
	if err := migrarAuditoriaParticionada(db); err != nil {
		return nil, err
	}

	// Crear índice parcial sobre sesiones_activas (idempotente)
	if err := crearIndicesParciales(db); err != nil {
		return nil, err
	}

	// Aplicar función y trigger de inmutabilidad a la tabla de auditoria
	err = db.Exec(`
		CREATE OR REPLACE FUNCTION audit_inmutabilidad()
		RETURNS TRIGGER AS $$
		BEGIN
			RAISE EXCEPTION 'Operación destructiva denegada: El registro de auditoría es inmutable. No se permiten UPDATE, DELETE o TRUNCATE.';
			RETURN NULL;
		END;
		$$ LANGUAGE plpgsql;
		
		DO $$
		BEGIN
			IF NOT EXISTS (
				SELECT 1 FROM pg_trigger t
				JOIN pg_class c ON c.oid = t.tgrelid
				JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE t.tgname = 'trg_auditoria_inmutable'
				  AND c.relname = 'auditoria'
				  AND n.nspname = current_schema()
			) THEN
				CREATE TRIGGER trg_auditoria_inmutable
				BEFORE UPDATE OR DELETE OR TRUNCATE ON auditoria
				FOR EACH STATEMENT
				EXECUTE FUNCTION audit_inmutabilidad();
			END IF;
		END
		$$;
	`).Error
	if err != nil {
		return nil, fmt.Errorf("error al aplicar trigger de inmutabilidad: %w", err)
	}
	log.Println("🔒 Inmutabilidad de auditoría garantizada mediante trigger")

	// Revocar privilegios destructivos al usuario actual (defensa en profundidad)
	if err := db.Exec(`REVOKE UPDATE, DELETE, TRUNCATE ON auditoria FROM CURRENT_USER;`).Error; err != nil {
		log.Printf("⚠️ No se pudieron revocar los privilegios sobre auditoria: %v", err)
	} else {
		log.Println("🛡️ Privilegios destructivos revocados sobre la tabla de auditoría")
	}

	seedAdminPorDefecto(db)

	return db, nil
}

// migrarSesionesAUnaFilaPorSesion reemplaza el esquema viejo de sesiones_activas
// (una fila por cada JTI, columna jti_token) por el de "1 sesión = 1 fila".
// Las filas viejas no se pueden convertir, así que la tabla se elimina y
// AutoMigrate la vuelve a crear: todos los usuarios tienen que loguearse de
// nuevo una vez. Ninguna otra tabla la referencia. En una base ya migrada no hace nada.
func migrarSesionesAUnaFilaPorSesion(db *gorm.DB) error {
	var esquemaViejo int64
	if err := db.Raw(`SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'sesiones_activas' AND column_name = 'jti_token'`).
		Scan(&esquemaViejo).Error; err != nil {
		return fmt.Errorf("error al inspeccionar sesiones_activas: %w", err)
	}
	if esquemaViejo == 0 {
		return nil
	}
	var filas int64
	db.Table("sesiones_activas").Count(&filas)
	if err := db.Migrator().DropTable("sesiones_activas"); err != nil {
		return fmt.Errorf("error al migrar sesiones_activas: %w", err)
	}
	log.Printf("🔁 sesiones_activas migrada a \"1 sesión = 1 fila\": se descartaron %d filas del esquema viejo (los usuarios deben volver a loguearse)", filas)
	return nil
}

// crearIndicesParciales crea el índice parcial idx_sesiones_activas_vigentes sobre
// sesiones_activas filtrando solo las sesiones activas (activa = true).
// Al excluir las sesiones expiradas/cerradas, el índice es significativamente
// más pequeño y las búsquedas por jti_access o jti_refresh en el flujo de
// autenticación son sustancialmente más rápidas.
// La creación es idempotente gracias a IF NOT EXISTS.
func crearIndicesParciales(db *gorm.DB) error {
	err := db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_sesiones_activas_vigentes
		ON sesiones_activas (usuario_id, fecha_expiracion)
		WHERE activa = true;
	`).Error
	if err != nil {
		return fmt.Errorf("error al crear idx_sesiones_activas_vigentes: %w", err)
	}
	log.Println("📊 Índice parcial idx_sesiones_activas_vigentes garantizado")
	return nil
}

// migrarAuditoriaParticionada crea la tabla auditoria con particionamiento
// declarativo por rango de fecha_hora (PARTITION BY RANGE). GORM AutoMigrate
// no entiende particionamiento nativo de PostgreSQL, por eso se gestiona con
// SQL raw puro. Toda la función es idempotente: si la tabla ya existe con
// la estructura correcta no hace nada.
func migrarAuditoriaParticionada(db *gorm.DB) error {
	// 1. Verificar si la tabla auditoria ya existe como tabla particionada.
	//    relkind='p' es el código de PostgreSQL para "partitioned table".
	var esParticionada int64
	if err := db.Raw(`
		SELECT count(*) FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'auditoria'
		  AND n.nspname = current_schema()
		  AND c.relkind = 'p'
	`).Scan(&esParticionada).Error; err != nil {
		return fmt.Errorf("error al inspeccionar tabla auditoria: %w", err)
	}

	if esParticionada == 0 {
		// La tabla no existe o existe como tabla normal (sin particionamiento).
		// Si existe como tabla normal, la renombramos para preservar los datos
		// históricos antes de recrearla como particionada.
		var existeNormal int64
		if err := db.Raw(`
			SELECT count(*) FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relname = 'auditoria'
			  AND n.nspname = current_schema()
		`).Scan(&existeNormal).Error; err != nil {
			return fmt.Errorf("error al verificar existencia de auditoria: %w", err)
		}

		if existeNormal > 0 {
			// Antes de renombrar, verificar que auditoria_legacy no exista ya
			// (puede ocurrir si el servidor se cayó en una migración anterior
			// justo entre el rename y la creación de la tabla particionada).
			var legacyExiste int64
			if err := db.Raw(`
				SELECT count(*) FROM pg_class c
				JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE c.relname = 'auditoria_legacy'
				  AND n.nspname = current_schema()
			`).Scan(&legacyExiste).Error; err != nil {
				return fmt.Errorf("error al verificar existencia de auditoria_legacy: %w", err)
			}
			if legacyExiste > 0 {
				// Ya existe una legacy de una migración anterior parcial.
				// La tabla 'auditoria' actual es una remanente anómala: la
				// dropeamos (sus datos son redundantes con legacy).
				log.Println("⚠️  auditoria_legacy ya existe. Eliminando tabla auditoria remanente antes de recriar como particionada.")
				if err := db.Exec(`DROP TABLE auditoria`).Error; err != nil {
					return fmt.Errorf("error al eliminar tabla auditoria remanente: %w", err)
				}
			} else {
				var filas int64
				db.Raw("SELECT count(*) FROM auditoria").Scan(&filas)
				log.Printf("🔁 auditoria: tabla plana existente (%d filas). Renombrando a auditoria_legacy antes de recriar como particionada...", filas)
				if err := db.Exec(`ALTER TABLE auditoria RENAME TO auditoria_legacy`).Error; err != nil {
					return fmt.Errorf("error al renombrar auditoria legacy: %w", err)
				}
			}
		}

		// Crear tabla madre particionada por rango de fecha_hora.
		// NOTA: en PostgreSQL las tablas particionadas requieren que el PK incluya
		// la clave de partición. Por eso el PK es (id, fecha_hora) y no solo (id).
		// GORM respeta este PK compuesto en los INSERTs gracias al RETURNING.
		// La FK usuario_id → usuarios(id) mantiene la integridad referencial
		// equivalente al esquema original generado por GORM AutoMigrate.
		if err := db.Exec(`
			CREATE TABLE IF NOT EXISTS auditoria (
				id               UUID         NOT NULL DEFAULT uuid_generate_v7(),
				usuario_id       UUID         REFERENCES usuarios(id) ON UPDATE CASCADE ON DELETE RESTRICT,
				accion           VARCHAR(100) NOT NULL,
				instancia_id     VARCHAR(100),
				instancia_nombre VARCHAR(255),
				resultado        VARCHAR(50)  NOT NULL,
				detalles         JSONB,
				fecha_hora       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
				PRIMARY KEY (id, fecha_hora)
			) PARTITION BY RANGE (fecha_hora);
		`).Error; err != nil {
			return fmt.Errorf("error al crear tabla auditoria particionada: %w", err)
		}
		log.Println("🗂️  Tabla auditoria creada con particionamiento declarativo RANGE (fecha_hora)")
	}

	// 2. Crear particiones trimestrales e idempotentes.
	if err := crearParticionesAuditoria(db); err != nil {
		return err
	}

	// 3. Crear índices locales en las particiones (idempotentes).
	indices := []struct {
		nombre string
		sql    string
	}{
		{
			nombre: "idx_auditoria_fecha_accion_resultado",
			sql: `CREATE INDEX IF NOT EXISTS idx_auditoria_fecha_accion_resultado
				  ON auditoria (fecha_hora DESC, accion, resultado)`,
		},
		{
			nombre: "idx_auditoria_usuario_fecha",
			sql: `CREATE INDEX IF NOT EXISTS idx_auditoria_usuario_fecha
				  ON auditoria (usuario_id, fecha_hora DESC)`,
		},
	}
	for _, idx := range indices {
		if err := db.Exec(idx.sql).Error; err != nil {
			return fmt.Errorf("error al crear índice %s: %w", idx.nombre, err)
		}
	}
	log.Println("📊 Índices locales de auditoria garantizados")
	return nil
}

// crearParticionesAuditoria crea las particiones trimestrales e iniciales de la
// tabla auditoria. Cada CREATE TABLE ... PARTITION OF es idempotente gracias a
// IF NOT EXISTS. La partición _default captura cualquier fecha fuera de rango
// sin lanzar un error de inserción.
func crearParticionesAuditoria(db *gorm.DB) error {
	particiones := []struct {
		nombre string
		sql    string
	}{
		{
			nombre: "auditoria_2026_q3",
			sql: `CREATE TABLE IF NOT EXISTS auditoria_2026_q3
				  PARTITION OF auditoria
				  FOR VALUES FROM ('2026-07-01') TO ('2026-10-01')`,
		},
		{
			nombre: "auditoria_2026_q4",
			sql: `CREATE TABLE IF NOT EXISTS auditoria_2026_q4
				  PARTITION OF auditoria
				  FOR VALUES FROM ('2026-10-01') TO ('2027-01-01')`,
		},
		{
			nombre: "auditoria_2027_q1",
			sql: `CREATE TABLE IF NOT EXISTS auditoria_2027_q1
				  PARTITION OF auditoria
				  FOR VALUES FROM ('2027-01-01') TO ('2027-04-01')`,
		},
		{
			nombre: "auditoria_default",
			sql: `CREATE TABLE IF NOT EXISTS auditoria_default
				  PARTITION OF auditoria DEFAULT`,
		},
	}

	for _, p := range particiones {
		if err := db.Exec(p.sql).Error; err != nil {
			return fmt.Errorf("error al crear partición %s: %w", p.nombre, err)
		}
	}
	log.Println("📅 Particiones trimestrales de auditoria garantizadas (2026_q3, 2026_q4, 2027_q1, default)")
	return nil
}

// seedAdminPorDefecto crea un administrador por defecto si la tabla de usuarios está vacía.
func seedAdminPorDefecto(db *gorm.DB) {
	var count int64
	db.Model(&domain.Usuario{}).Count(&count)
	if count > 0 {
		return // Ya existen usuarios, no hacemos nada
	}

	log.Println("⚠️  Base de datos vacía. Generando Administrador por defecto...")

	// 1. Crear Organización por defecto
	org := domain.Organizacion{
		ID:        uuid.New(),
		NombreOrg: "El Centinela Default",
		CreadoPor: uuid.Nil,
	}
	if err := db.Create(&org).Error; err != nil {
		log.Printf("❌ Error creando organización por defecto: %v", err)
		return
	}

	// 2. Crear Administrador
	hash, _ := crypto.HashContrasena("Admin123!")
	admin := domain.Usuario{
		ID:               uuid.New(),
		OrganizacionID:   org.ID,
		NombreCompleto:   "Administrador",
		NombreUsuario:    "admin",
		EmailUsuario:     "admin@elcentinela.com",
		ContrasenaHash:   hash,
		Rol:              "ADMIN",
		Activo:           true,
		CambioContrasena: true, // Obligamos a que cambie la clave en el primer login
		TotpVinculado:    false,
	}
	if err := db.Create(&admin).Error; err != nil {
		log.Printf("❌ Error creando administrador por defecto: %v", err)
		return
	}

	// 3. Actualizar CreadoPor en la organización
	db.Model(&org).Update("creado_por", admin.ID)

	log.Println("✅ Administrador por defecto creado exitosamente.")
	log.Println("   👉 Email: admin@elcentinela.com")
	log.Println("   👉 Clave: Admin123!")
}
