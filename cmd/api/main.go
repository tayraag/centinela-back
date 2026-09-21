// Package main es el punto de entrada de El Centinela Backend.
package main

import (
	"log"
	"net/http"

	_ "el-centinela/docs"
	httpHandlers "el-centinela/internal/adapters/primary/http"
	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/adapters/secondary/postgres"
	"el-centinela/internal/core/services"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

// @title          El Centinela API
// @version        1.0
// @description    API REST para el sistema de monitoreo de instancias Proxmox El Centinela.
// @description    **Flujo de autenticación**: Login → `/auth/login` → obtener `jwtTemporal` → `/auth/2fa/verify` → obtener `accessToken` → usar en el botón **Authorize** de esta UI.
//
// @contact.name   Soporte El Centinela
//
// A proposito NO se declara @host. Si se declara (por ejemplo "localhost:8080"),
// Swagger UI manda los "Try it out" a ESE host, es decir a la maquina de quien
// esta mirando la documentacion, y todos los endpoints fallan por error de red.
// Sin @host, Swagger resuelve contra el origen que sirve la doc: funciona igual
// desde localhost, desde el FQDN y detras del proxy, sin config por entorno.
//
// @BasePath       /api
//
// @securityDefinitions.apikey BearerAuth
// @in             header
// @name           Authorization
// @description    Token de acceso definitivo. Formato: **Bearer &lt;accessToken&gt;**. Obtené el token completando el flujo: `POST /auth/login` → `POST /auth/2fa/verify`.
//
// @securityDefinitions.apikey BearerPreAuth
// @in             header
// @name           Authorization
// @description    JWT temporal pre-2FA. Formato: **Bearer &lt;jwtTemporal&gt;**. Obtené el token de `POST /auth/login`.

// ============================================================================
// VERIFICACION DE INTEGRIDAD DE INFRAESTRUCTURA (TEMPORAL)
// ----------------------------------------------------------------------------
// Datos de build que inyecta el workflow de deploy con -ldflags. Alimentan el
// endpoint GET /api/version, que permite comprobar desde afuera que el deploy
// del back llego al servidor.
// Lo agrega INFRAESTRUCTURA para verificar integridad; NO es parte del producto.
// Se puede borrar sin afectar nada (ver instrucciones en el endpoint /api/version).
// ============================================================================
var (
	buildCommit = "dev"
	buildTime   = "dev"
)

func main() {
	// Configurar logger conciso: solo hora, sin fecha
	log.SetFlags(log.Ltime)
	log.Println("🚀 Iniciando El Centinela Backend...")

	// 1. Cargar variables de entorno desde .env en la raíz
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️  Sin .env en la raíz, usando variables de entorno del sistema")
	} else {
		log.Println("✅ Variables de entorno cargadas desde el archivo .env")
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
		// ========================================================================
		// VERIFICACION DE INTEGRIDAD DE INFRAESTRUCTURA (TEMPORAL)
		// ------------------------------------------------------------------------
		// Devuelve la version desplegada del back (commit + fecha/hora del build)
		// para comprobar desde afuera que el deploy llego al servidor:
		//     curl http://centinela/api/version
		// Lo agrega INFRAESTRUCTURA; NO es parte del producto.
		// PARA BORRARLO: eliminar este bloque, las variables buildCommit/buildTime
		// de arriba, y los -ldflags del workflow .github/workflows/deploy-back-test.yml
		//
		// Prueba de deploy: este comentario se agrego a proposito para verificar que
		// un push a main del back actualiza la linea "Back:" del login.
		// ========================================================================
		api.GET("/version", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{
				"commit":  buildCommit,
				"builtAt": buildTime,
			})
		})

		// ==========================================
		// Rutas de Autenticación (RF-01)
		// ==========================================
		auth := api.Group("/auth")
		{
			// Rutas públicas (sin autenticación)
			auth.POST("/login", authHandler.Login)
			auth.POST("/refresh", authHandler.RefrescarToken)
			auth.POST("/logout", authHandler.Logout)

			// Rutas del flujo 2FA (requieren JWT temporal pre-auth)
			twoFA := auth.Group("/2fa", middleware.RequirePreAuth())
			{
				twoFA.GET("/qr", authHandler.ObtenerQR)
				twoFA.POST("/verify", authHandler.VerificarTotp)
			}
		}

		// Roles disponibles (para el selector del formulario)
		api.GET("/roles", middleware.RequireAuth(), middleware.RequireRole("ADMIN"), userHandler.ObtenerRoles)

		// ==========================================
		// Rutas de Gestión de Usuarios (RF-09) — solo ADMIN
		// ==========================================
		admin := api.Group("/admin", middleware.RequireAuth(), middleware.RequireRole("ADMIN"))
		{
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

					}
		// ==========================================
		// Swagger UI (solo en desarrollo)
		// ==========================================
		router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	}

	// 8. Encender el servidor en el puerto 8080
	log.Println("🛡️ Servidor HTTP escuchando en el puerto 8080...")
	log.Println("📖 Swagger UI disponible en: http://localhost:8080/swagger/index.html")
	if err := router.Run(":8080"); err != nil {
		log.Fatalf("❌ Error al arrancar el servidor: %v", err)
	}
}
