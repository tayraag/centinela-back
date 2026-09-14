package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"el-centinela/internal/adapters/secondary/postgres"
	"el-centinela/internal/core/domain"
	"el-centinela/internal/infrastructure/crypto"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

func main() {
	log.Println("🌱 El Centinela — Script de Seed (Primer Usuario Admin)")
	log.Println("─────────────────────────────────────────────────────────")

	// 1. Cargar variables de entorno desde la raíz
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️  Sin archivo .env, usando variables del sistema")
	} else {
		log.Println("✅ Variables de entorno cargadas")
	}

	// 2. Conectar a la base de datos
	db, err := postgres.InitDB()
	if err != nil {
		log.Fatalf("❌ Error al conectar con la base de datos: %v", err)
	}

	reader := bufio.NewReader(os.Stdin)

	// 3. Pedir datos del usuario admin
	fmt.Println()
	nombreOrg := leer(reader, "Nombre de la organización", "El Centinela")
	nombreCompleto := leer(reader, "Nombre completo del admin", "Administrador")
	email := leer(reader, "Email del admin", "admin@elcentinela.local")
	nombreUsuario := leer(reader, "Nombre de usuario", "admin")
	contrasena := leerContrasena(reader, "Contraseña (mínimo 8 caracteres)")

	fmt.Println()
	log.Println("🔒 Hasheando contraseña con bcrypt (cost 12)...")

	// 4. Hashear la contraseña
	hash, err := crypto.HashContrasena(contrasena)
	if err != nil {
		log.Fatalf("❌ Error al hashear contraseña: %v", err)
	}

	// 5. Crear la organización
	orgID := uuid.New()
	org := domain.Organizacion{
		ID:        orgID,
		NombreOrg: nombreOrg,
		CreadoPor: uuid.Nil, // Se actualiza después con el ID del admin
	}

	// Verificar si ya existe una organización con ese nombre
	var orgExistente domain.Organizacion
	if result := db.Where("nombre_org = ?", nombreOrg).First(&orgExistente); result.Error == nil {
		log.Printf("ℹ️  Ya existe la organización '%s', usando la existente (ID: %s)", nombreOrg, orgExistente.ID)
		org = orgExistente
	} else {
		if result := db.Create(&org); result.Error != nil {
			log.Fatalf("❌ Error al crear organización: %v", result.Error)
		}
		log.Printf("✅ Organización '%s' creada (ID: %s)", org.NombreOrg, org.ID)
	}

	// 6. Verificar si el email ya existe
	var usuarioExistente domain.Usuario
	if result := db.Where("email_usuario = ?", email).First(&usuarioExistente); result.Error == nil {
		log.Printf("⚠️  Ya existe un usuario con el email '%s'. Abortando para no duplicar.", email)
		log.Printf("   ID del usuario existente: %s", usuarioExistente.ID)
		os.Exit(0)
	}

	// 7. Crear el usuario administrador
	ahora := time.Now()
	adminID := uuid.New()
	admin := domain.Usuario{
		ID:                adminID,
		OrganizacionID:    org.ID,
		NombreCompleto:    nombreCompleto,
		NombreUsuario:     nombreUsuario,
		EmailUsuario:      email,
		ContrasenaHash:    hash,
		Rol:               "ADMIN",
		Activo:            true,
		CambioContrasena:  false, // El admin inicial no necesita cambio obligatorio
		TotpVinculado:     false,
		FechaUltimoAcceso: nil,
		FechaCreacion:     ahora,
	}

	if result := db.Create(&admin); result.Error != nil {
		log.Fatalf("❌ Error al crear usuario admin: %v", result.Error)
	}

	// 8. Actualizar el campo CreadoPor de la organización con el ID del admin
	db.Model(&org).Update("creado_por", adminID)

	// 9. Resumen final
	fmt.Println()
	log.Println("─────────────────────────────────────────────────────────")
	log.Println("✅ Seed completado exitosamente")
	log.Printf("   Organización : %s (%s)", org.NombreOrg, org.ID)
	log.Printf("   Usuario Admin : %s (%s)", admin.NombreCompleto, admin.ID)
	log.Printf("   Email         : %s", admin.EmailUsuario)
	log.Printf("   Rol           : %s", admin.Rol)
	log.Printf("   2FA           : pendiente de vincular (primer login)")
	log.Println("─────────────────────────────────────────────────────────")
	log.Println("🚀 Podés hacer login en: POST /api/auth/login")
	log.Printf(`   Body: {"email":"%s","contrasena":"<tu contraseña>"}`, admin.EmailUsuario)
}

// leer lee una línea de stdin con un valor por defecto si se deja vacío.
func leer(reader *bufio.Reader, prompt, defecto string) string {
	fmt.Printf("  %s [%s]: ", prompt, defecto)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input == "" {
		return defecto
	}
	return input
}

// leerContrasena lee la contraseña validando longitud mínima.
func leerContrasena(reader *bufio.Reader, prompt string) string {
	for {
		fmt.Printf("  %s: ", prompt)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)
		if len(input) >= 8 {
			return input
		}
		fmt.Println("  ⚠️  La contraseña debe tener al menos 8 caracteres. Intentá de nuevo.")
	}
}
