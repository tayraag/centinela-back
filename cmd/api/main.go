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
	userRepo := postgres.NewUserRepository(db)

	// 4. Inicializar servicios de dominio (inyección de dependencias)
	authService := services.NewAuthService(authRepo)
	userService := services.NewUserService(userRepo, authRepo)

	// 5. Inicializar handlers HTTP
	authHandler := httpHandlers.NewAuthHandler(authService)
	userHandler := httpHandlers.NewUserHandler(userService)
	accountHandler := httpHandlers.NewAccountHandler(userService)

	// 6. Configurar el Router HTTP (Gin)
	// Usamos gin.New() para tener control total sobre los middlewares.
	gin.SetMode(gin.ReleaseMode) // Silencia el banner y logs de debug de Gin
	router := gin.New()
	router.Use(gin.Recovery())             // Recupera de panics sin caer el servidor
	router.Use(middleware.RequestLogger()) // Logger conciso personalizado

	// 7. Definir las rutas
	api := router.Group("/api")
	{
		// ==========================================
		// Rutas de Autenticación (RF-01)
		// ==========================================
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

		// ==========================================
		// Rutas de Gestión de Usuarios (RF-09) — solo ADMIN
		// ==========================================
		admin := api.Group("/", middleware.RequireAuth(), middleware.RequireRole("ADMIN"))
		{
			// Roles disponibles (para el selector del formulario)
			admin.GET("/roles", userHandler.ObtenerRoles)

			// CRUD de usuarios
			users := admin.Group("/users")
			{
				users.GET("", userHandler.ListarUsuarios)
				users.POST("", userHandler.CrearUsuario)
				users.GET("/:id", userHandler.ObtenerUsuario)
				users.PUT("/:id", userHandler.ActualizarUsuario)
				users.DELETE("/:id", userHandler.EliminarUsuario)

				// Permisos de instancias del usuario
				users.PUT("/:id/instances", userHandler.AsignarPermisos)

				// Actividad del usuario (auditoría filtrada)
				users.GET("/:id/activity", userHandler.ListarActividad)

				// Acciones de seguridad del usuario
				users.POST("/:id/2fa/reset", userHandler.ResetearTotp)
				users.POST("/:id/password/reset", userHandler.ResetearContrasena)
			}
		}

		// ==========================================
		// Rutas de Perfil Propio (RF-09) — cualquier usuario autenticado
		// ==========================================
		account := api.Group("/account", middleware.RequireAuth())
		{
			account.GET("/profile", accountHandler.ObtenerPerfil)
			account.PUT("/profile", accountHandler.ActualizarPerfil)
			account.PUT("/password", accountHandler.CambiarContrasena)

			// Logout: cierra la sesión actual
			account.DELETE("/sessions/current", accountHandler.CerrarSesionActual)
		}
	}

	// 8. Encender el servidor en el puerto 8080
	log.Println("🛡️ Servidor HTTP escuchando en el puerto 8080...")
	if err := router.Run(":8080"); err != nil {
		log.Fatalf("❌ Error al arrancar el servidor: %v", err)
	}
}