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
		dsn = "host=localhost user=centinela_admin password=centinela_password dbname=centinela_db port=5433 sslmode=disable TimeZone=America/Argentina/Buenos_Aires"
		log.Println("⚠️  DB_DSN no encontrado en el entorno, usando configuración local por defecto")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("error al conectar con PostgreSQL: %w", err)
	}

	log.Println("✅ Conexión exitosa a PostgreSQL")
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

	seedAdminPorDefecto(db)

	return db, nil
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
