package main

import (
	"log"

	httpHandlers "el-centinela/internal/adapters/primary/http"
	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/adapters/secondary/postgres"
	"el-centinela/internal/core/services"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

func main() {
	// Configurar logger conciso: solo hora, sin fecha
	log.SetFlags(log.Ltime)
	log.Println("🚀 Iniciando El Centinela Backend...")

	// 1. Cargar variables de entorno desde .env
	// Intentamos múltiples rutas para que funcione tanto con `go run ./cmd/api/`
	// (desde la raíz) como ejecutando el binario desde cualquier directorio.
	envCargado := false
	for _, ruta := range []string{".env", "cmd/api/.env"} {
		if err := godotenv.Load(ruta); err == nil {
			log.Printf("ℹ️  Variables de entorno cargadas desde: %s", ruta)
			envCargado = true
			break
		}
	}
	if !envCargado {
		log.Println("ℹ️  Sin .env, usando variables de entorno del sistema")
	}

	// 2. Inicializar la base de datos
	db, err := postgres.InitDB()
	if err != nil {
		log.Fatalf("❌ Error fatal al iniciar la base de datos: %v", err)
	}

	// 3. Inicializar adaptadores secundarios (repositorios)
	authRepo := postgres.NewAuthRepository(db)

	// 4. Inicializar servicios de dominio (inyección de dependencias)
	authService := services.NewAuthService(authRepo)

	// 5. Inicializar handlers HTTP
	authHandler := httpHandlers.NewAuthHandler(authService)

	// 6. Configurar el Router HTTP (Gin)
	// Usamos gin.New() para tener control total sobre los middlewares.
	gin.SetMode(gin.ReleaseMode) // Silencia el banner y logs de debug de Gin
	router := gin.New()
	router.Use(gin.Recovery())             // Recupera de panics sin caer el servidor
	router.Use(middleware.RequestLogger()) // Logger conciso personalizado

	// 7. Definir las rutas
	api := router.Group("/api")
	{
		auth := api.Group("/auth")
		{
			// Rutas públicas (sin autenticación)
			auth.POST("/login", authHandler.Login)
			auth.POST("/refresh", authHandler.RefrescarToken)

			// Rutas del flujo 2FA (requieren JWT temporal pre-auth)
			twoFA := auth.Group("/2fa", middleware.RequirePreAuth())
			{
				twoFA.GET("/qr", authHandler.ObtenerQR)
				twoFA.POST("/verify", authHandler.VerificarTotp)
			}

			// Ruta de administración (requiere access token + rol ADMIN)
			auth.POST("/2fa/relink",
				middleware.RequireAuth(),
				middleware.RequireRole("ADMIN"),
				authHandler.SolicitarRevinculacion,
			)
		}
	}

	// 8. Encender el servidor en el puerto 8080
	log.Println("🛡️ Servidor HTTP escuchando en el puerto 8080...")
	if err := router.Run(":8080"); err != nil {
		log.Fatalf("❌ Error al arrancar el servidor: %v", err)
	}
}