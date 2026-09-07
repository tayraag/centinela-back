package postgres

import (
	"fmt"
	"log"

	"el-centinela/internal/core/domain" // Importamos nuestros modelos

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// InitDB abre la conexión a PostgreSQL y crea las tablas automáticamente
func InitDB() (*gorm.DB, error) {
	// 1. Definimos los datos de conexión (que coinciden con nuestro docker-compose.yml)
	dsn := "host=localhost user=centinela_admin password=centinela_password dbname=centinela_db port=5432 sslmode=disable TimeZone=America/Argentina/Buenos_Aires"

	// 2. Abrimos la conexión con GORM
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info), // Para ver las consultas SQL en la consola
	})
	if err != nil {
		return nil, fmt.Errorf("error al conectar con PostgreSQL: %w", err)
	}

	log.Println("✅ Conexión exitosa a PostgreSQL")

	// 3. Auto-Migración (La magia de GORM)
	// Aquí le pasamos nuestros Structs y GORM se encarga de convertirlos en tablas reales.
	// Aplicará automáticamente nuestras reglas de seguridad, UUIDv7 y Soft Delete.
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

	return db, nil
}