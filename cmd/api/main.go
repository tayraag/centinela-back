// Package main es el punto de entrada de El Centinela Backend.
package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	_ "el-centinela/docs"
	httpHandlers "el-centinela/internal/adapters/primary/http"
	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/adapters/secondary/email"
	"el-centinela/internal/adapters/secondary/memoria"
	"el-centinela/internal/adapters/secondary/postgres"
	"el-centinela/internal/adapters/secondary/proxmox"
	"el-centinela/internal/adapters/secondary/redis"
	"el-centinela/internal/core/ports"
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
	emailService := email.NewMockEmailService()
	auditRepo := postgres.NewAuditRepository(db)
	instanceRepo := postgres.NewInstanceRepository(db)
	sesionCache := conectarAlmacenSesiones()
	proxmoxURL := os.Getenv("PROXMOX_URL")
	if proxmoxURL == "" {
		proxmoxURL = os.Getenv("PROXMOX_BASE_URL")
	}

	proxmoxClient := proxmox.NewClient(
		proxmoxURL,
		os.Getenv("PROXMOX_NODE"),
		os.Getenv("PROXMOX_TOKEN_ID"),
		os.Getenv("PROXMOX_TOKEN_SECRET"),
		proxmox.BuildTLSConfig(),
	)
	if !proxmoxClient.TokenConfigurado() {
		log.Println("⚠️  PROXMOX_TOKEN_ID/PROXMOX_TOKEN_SECRET sin configurar: los endpoints de /api/instances van a responder 502")
	}

	vmidsProtegidos, err := middleware.ParseVmidsProtegidos(os.Getenv("PROXMOX_PROTECTED_VMIDS"))
	if err != nil {
		log.Fatalf("❌ PROXMOX_PROTECTED_VMIDS inválida: %v", err)
	}

	// 4. Inicializar servicios de dominio (inyección de dependencias)
	auditService := services.NewAuditService(auditRepo)
	authService := services.NewAuthService(authRepo, sesionCache, emailService, auditService)
	userService := services.NewUserService(userRepo, authRepo, sesionCache, auditService, emailService)

	// 5. Inicializar handlers HTTP
	authHandler := httpHandlers.NewAuthHandler(authService)
	userHandler := httpHandlers.NewUserHandler(userService)
	accountHandler := httpHandlers.NewAccountHandler(userService)
	auditHandler := httpHandlers.NewAuditHandler(auditService)
	instanceHandler := httpHandlers.NewInstanceHandler(proxmoxClient, userRepo)

	// 6. Configurar el Router HTTP (Gin)
	// Usamos gin.New() para tener control total sobre los middlewares.
	gin.SetMode(gin.ReleaseMode) // Silencia el banner y logs de debug de Gin
	router := gin.New()
	router.Use(gin.Recovery())             // Recupera de panics sin caer el servidor
	router.Use(middleware.RequestLogger()) // Logger conciso personalizado
	router.Use(middleware.CORS())          // Política CORS: lista blanca de orígenes (ver ALLOWED_ORIGINS en .env)
	router.Use(middleware.SecurityHeaders())

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

			// Rutas protegidas (requieren JWT access)
			auth.POST("/logout", middleware.RequireAuth(authService), authHandler.Logout)

			// Recuperación de contraseña (públicas)
			auth.POST("/password/forgot", authHandler.SolicitarRecuperacion)
			auth.POST("/password/reset", authHandler.ConfirmarRecuperacion)

			// Rutas del flujo 2FA (requieren JWT temporal pre-auth)
			twoFA := auth.Group("/2fa", middleware.RequirePreAuth(authService))
			{
				twoFA.GET("/qr", authHandler.ObtenerQR)
				twoFA.POST("/verify", authHandler.VerificarTotp)
			}
		}

		// Roles disponibles (para el selector del formulario)
		api.GET("/roles", middleware.RequireAuth(authService), middleware.RequireRole("ADMIN"), userHandler.ObtenerRoles)

		// ==========================================
		// Rutas de Gestión de Usuarios (RF-09) — solo ADMIN
		// ==========================================
		admin := api.Group("/admin", middleware.RequireAuth(authService), middleware.RequireRole("ADMIN"))
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
				users.GET("/:id/permissions", userHandler.ObtenerPermisos)
				users.PUT("/:id/permissions", userHandler.AsignarPermisos)

				// Actividad del usuario (auditoría filtrada)
				users.GET("/:id/activity", userHandler.ListarActividad)

				// Acciones de seguridad del usuario
				users.POST("/:id/2fa/reset", userHandler.ResetearTotp)
				users.POST("/:id/password/reset", userHandler.ResetearContrasena)
			}

			// ==========================================
			// Rutas de Auditoría (RF-08) — solo ADMIN
			// ==========================================
			admin.GET("/audit", auditHandler.ListarAuditoria)
			admin.GET("/audit/export", auditHandler.ExportarAuditoria)
		}

		// ==========================================
		// Rutas de Perfil Propio (RF-09) — cualquier usuario autenticado
		// ==========================================
		account := api.Group("/account", middleware.RequireAuth(authService))
		{
			account.GET("/profile", accountHandler.ObtenerPerfil)
			account.PUT("/profile", accountHandler.ActualizarPerfil)
			account.PUT("/password", accountHandler.CambiarContrasena)
		}

		// ==========================================
		// Rutas de Instancias Proxmox
		// ==========================================
		instances := api.Group("/instances", middleware.RequireAuth(authService))
		{
			instances.GET("", instanceHandler.ListarInstancias)
			instances.GET("/:vmid", middleware.RequireInstanceAccess(instanceRepo, "vmid", ports.NivelAccesoReadOnly), instanceHandler.ObtenerInstancia)
			instances.POST("/:vmid/start", middleware.RequireInstanceAccess(instanceRepo, "vmid", ports.NivelAccesoFullAccess), instanceHandler.IniciarInstancia)
			// Las acciones destructivas llevan además RejectProtectedInstance (VMIDs de infraestructura).
			instances.POST("/:vmid/stop", middleware.RequireInstanceAccess(instanceRepo, "vmid", ports.NivelAccesoFullAccess), middleware.RejectProtectedInstance(vmidsProtegidos, "vmid"), instanceHandler.DetenerInstancia)
		}
		// ==========================================
		// Swagger UI: apagada por defecto.
		// Estuvo publicada en PRUEBAS, donde cualquiera podía leer el contrato completo
		// de la API. Se habilita solo con ENABLE_SWAGGER=true (pensado para desarrollo local).
		// ==========================================
		if strings.EqualFold(os.Getenv("ENABLE_SWAGGER"), "true") {
			router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
			log.Println("📖 Swagger UI disponible en: http://localhost:8080/swagger/index.html")
		}
	}

	// 8. Mismo contrato de errores para lo que no existe.
	// Por defecto Gin responde "404 page not found" en texto plano, que rompe el
	// sobre JSON ({ errorCode, message }) que usa el resto de la API.
	router.NoRoute(func(c *gin.Context) {
		httpHandlers.SendError(c, http.StatusNotFound, "NOT_FOUND", "El recurso solicitado no existe.")
	})
	router.HandleMethodNotAllowed = true
	router.NoMethod(func(c *gin.Context) {
		httpHandlers.SendError(c, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "El método HTTP no está permitido para este recurso.")
	})

	// 9. Encender el servidor en el puerto 8080
	log.Println("🛡️ Servidor HTTP escuchando en el puerto 8080...")
	if err := router.Run(":8080"); err != nil {
		log.Fatalf("❌ Error al arrancar el servidor: %v", err)
	}
}

// conectarAlmacenSesiones elige dónde viven las sesiones efímeras (pre-2FA y
// réplica de las sesiones activas): Redis si REDIS_ADDR está definida y
// responde; si no, el respaldo en memoria, para que el login siga funcionando
// en un servidor que todavía no tiene Redis.
func conectarAlmacenSesiones() ports.SesionCache {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		log.Println("⚠️  REDIS_ADDR sin configurar: sesiones efímeras en MEMORIA (se pierden al reiniciar la API)")
		return memoria.NewSesionCache()
	}
	db := 0
	if v := os.Getenv("REDIS_DB"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			log.Fatalf("❌ REDIS_DB inválida (%q): debe ser un número de base, ej. 0", v)
		}
		db = n
	}
	cache, err := redis.Conectar(addr, os.Getenv("REDIS_PASSWORD"), db)
	if err != nil {
		log.Printf("⚠️  %v: sesiones efímeras en MEMORIA (se pierden al reiniciar la API)", err)
		return memoria.NewSesionCache()
	}
	log.Printf("✅ Conexión exitosa a Redis (%s, base %d)", addr, db)
	return cache
}
