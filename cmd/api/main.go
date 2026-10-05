// Package main es el punto de entrada de El Centinela Backend.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

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

	// Contexto global del proceso: se cancela ante SIGINT/SIGTERM para que
	// los workers en background se detengan limpiamente.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Worker de purga de sesiones (goroutine en segundo plano).
	// Se ejecuta cada hora y elimina sesiones expiradas o cerradas por el usuario.
	purgaWorker := postgres.NuevoPurgaWorker(db, 1*time.Hour)
	go purgaWorker.Iniciar(ctx)

	// 3. Inicializar adaptadores secundarios (repositorios)
	authRepo := postgres.NewAuthRepository(db)
	userRepo := postgres.NewUserRepository(db)
	var emailService ports.EmailService
	if os.Getenv("EMAIL_PROVIDER") == "smtp" {
		svc, err := email.NewSMTPEmailService()
		if err != nil {
			log.Fatalf("❌ Configuración SMTP inválida: %v", err)
		}
		emailService = svc
		log.Println("📧 Email Provider: SMTP (STARTTLS/TLS)")
	} else {
		emailService = email.NewMockEmailService()
		log.Println("📧 Email Provider: MOCK (consola/tests)")
	}
	auditRepo := postgres.NewAuditRepository(db)
	instanceRepo := postgres.NewInstanceRepository(db)
	kvStore := conectarRedis()
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
	authService := services.NewAuthService(authRepo, kvStore, emailService, auditService)
	userService := services.NewUserService(userRepo, authRepo, kvStore, auditService, emailService)

	// 5. Inicializar handlers HTTP
	authHandler := httpHandlers.NewAuthHandler(authService)
	userHandler := httpHandlers.NewUserHandler(userService)
	accountHandler := httpHandlers.NewAccountHandler(userService)
	auditHandler := httpHandlers.NewAuditHandler(auditService)
	eventosService := services.NewEventosService(kvStore, authRepo, instanceRepo, auditService)
	tareaRepo := postgres.NewTareaRepository(db)
	seguimientoTareas := services.NewSeguimientoTareas(proxmoxClient, tareaRepo, eventosService, auditService,
		services.ConfigSeguimiento{Workers: enteroDeEntorno("UPID_WORKERS", 8)})
	seguimientoTareas.Iniciar(ctx)
	instanceHandler := httpHandlers.NewInstanceHandler(proxmoxClient, userRepo, seguimientoTareas, tareaRepo, auditService,
		services.NewInventarioService(proxmoxClient))
	eventsHandler := httpHandlers.NewEventsHandler(eventosService)
	nodeHandler := httpHandlers.NewNodeHandler(services.NewNodoService(proxmoxClient, kvStore, valorODefecto(os.Getenv("PROXMOX_NODE"), "proxmox")))

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
		// Canal de eventos en tiempo real (RF-11) — SSE
		// El ticket se pide con el access token; el stream se abre con el ticket
		// (de un solo uso), así el JWT nunca viaja en la URL.
		// ==========================================
		api.POST("/events/ticket", middleware.RequireAuth(authService), eventsHandler.EmitirTicket)

		// Estado consolidado del nodo (ADMIN y OPERATOR), con caché en Redis
		api.GET("/node/status", middleware.RequireAuth(authService), nodeHandler.ObtenerEstado)
		api.GET("/events", eventsHandler.Stream)

		// ==========================================
		// Rutas de Instancias Proxmox
		// ==========================================
		instances := api.Group("/instances", middleware.RequireAuth(authService))
		{
			instances.GET("", instanceHandler.ListarInstancias)
			instances.GET("/:vmid", middleware.RequireInstanceAccess(instanceRepo, "vmid", ports.NivelAccesoReadOnly), instanceHandler.ObtenerInstancia)
			instances.POST("/:vmid/start", middleware.RequireInstanceAccess(instanceRepo, "vmid", ports.NivelAccesoFullAccess), middleware.RejectProtectedInstance(vmidsProtegidos, "vmid"), instanceHandler.IniciarInstancia)
			// Las acciones destructivas llevan además RejectProtectedInstance (VMIDs de infraestructura).
			instances.POST("/:vmid/stop", middleware.RequireInstanceAccess(instanceRepo, "vmid", ports.NivelAccesoFullAccess), middleware.RejectProtectedInstance(vmidsProtegidos, "vmid"), instanceHandler.DetenerInstancia)
			instances.POST("/:vmid/status/:action", middleware.RequireInstanceAccess(instanceRepo, "vmid", ports.NivelAccesoFullAccess), middleware.RejectProtectedInstance(vmidsProtegidos, "vmid"), instanceHandler.CambiarEstado)
			instances.DELETE("/:vmid", middleware.RequireRole("ADMIN"), middleware.RejectProtectedInstance(vmidsProtegidos, "vmid"), instanceHandler.EliminarInstancia)
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
	srv := &http.Server{Addr: ":8080", Handler: router}
	srv.RegisterOnShutdown(eventsHandler.CerrarStreams) // los streams SSE no terminan solos
	go func() {
		log.Println("🛡️ Servidor HTTP escuchando en el puerto 8080...")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("❌ Error al arrancar el servidor: %v", err)
		}
	}()

	// 10. Apagado ordenado ante SIGINT (Ctrl+C) o SIGTERM (systemctl stop / deploy):
	//     se dejan de aceptar requests, se cortan los streams SSE, se terminan los
	//     requests en curso y se espera a que los workers terminen su consulta.
	//     Las tareas sin terminar quedan RUNNING y el reconciliador las retoma al arrancar.
	<-ctx.Done()
	stop() // un segundo Ctrl+C mata el proceso al instante
	log.Println("🛑 Señal de apagado recibida: cerrando la API...")
	ctxApagado, cancelar := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelar()
	if err := srv.Shutdown(ctxApagado); err != nil {
		log.Printf("⚠️  El servidor HTTP no cerró a tiempo: %v", err)
	}
	if err := seguimientoTareas.Esperar(ctxApagado); err != nil {
		log.Printf("⚠️  El seguimiento de tareas no terminó a tiempo: %v", err)
	}
	log.Println("👋 API detenida")
}

// enteroDeEntorno lee una variable de entorno entera y positiva, con un default.
func enteroDeEntorno(nombre string, defecto int) int {
	v := os.Getenv(nombre)
	if v == "" {
		return defecto
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		log.Fatalf("❌ %s inválida (%q): debe ser un entero mayor a 0", nombre, v)
	}
	return n
}

// conectarRedis inicializa el almacén clave-valor y Pub/Sub del backend
// (ports.KeyValueStore). Se crea una sola vez acá y se inyecta en los servicios.
//
// Si Redis no responde, la API NO aborta: arranca en MODO DEGRADADO con el
// adaptador en memoria (sesiones y Pub/Sub dentro de este proceso), para no
// dejar sin login a un entorno que todavía no tiene Redis. Ver docs/redis.md.
func conectarRedis() ports.KeyValueStore {
	cfg, err := redis.ConfigDesdeEntorno()
	if err != nil {
		log.Fatalf("[FATAL] %v", err)
	}
	store, err := redis.Conectar(context.Background(), cfg)
	if err != nil {
		log.Printf("[WARN] %v", err)
		log.Println("[WARN] MODO DEGRADADO: Redis no disponible. Sesiones y Pub/Sub en la memoria de este proceso: se pierden al reiniciar la API y no se comparten entre instancias (ver docs/redis.md).")
		return memoria.Nuevo()
	}
	log.Println("[INFO] Conexión con Redis establecida exitosamente.")
	return store
}

func valorODefecto(v, defecto string) string {
	if v == "" {
		return defecto
	}
	return v
}
