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
		&domain.Auditoria{},
		&domain.TareaAsincrona{},
		&domain.Notificacion{},
	)
	if err != nil {
		return nil, fmt.Errorf("error al migrar la base de datos: %w", err)
	}
	log.Println("✅ Migración completada exitosamente")

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
			IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'trg_auditoria_inmutable') THEN
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
