package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	apphttp "el-centinela/internal/adapters/primary/http"
	"el-centinela/internal/adapters/secondary/memory"
	"el-centinela/internal/adapters/secondary/postgres"
	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"
	"el-centinela/internal/security"
	"github.com/google/uuid"
)

func main() {
	log.Println("¡El Centinela está en línea!")

	var users servicesUserRepository
	var permissions servicesPermissionRepository
	var sessions servicesSessionRepository
	var audits servicesAuditRepository
	mode := envOrDefault("AUTH_MODE", "memory")
	if mode == "memory" {
		users, permissions, sessions, audits = newMemoryRepositories()
		log.Println("⚠️ AUTH_MODE=memory: los usuarios y sesiones se perderán al detener el proceso")
	} else if mode == "postgres" {
		db, err := postgres.InitDB()
		if err != nil {
			log.Fatalf("❌ Error fatal al iniciar la base de datos: %v", err)
		}
		users = postgres.NewUserRepository(db)
		permissions = postgres.NewPermissionRepository(db)
		sessions = postgres.NewSessionRepository(db)
		audits = postgres.NewAuditRepository(db)
	} else {
		log.Fatalf("AUTH_MODE inválido: %s (usa memory o postgres)", mode)
	}

	passwords := security.BcryptHasher{}
	twoFactor := security.TOTPProvider{Issuer: envOrDefault("TOTP_ISSUER", "El Centinela")}
	tokens, err := security.NewJWTProvider(envOrDefault("JWT_SECRET", "development-only-jwt-secret-change-me-32"))
	if err != nil {
		log.Fatalf("error en JWT_SECRET: %v", err)
	}
	secrets, err := security.NewSecretProtector(envOrDefault("TOTP_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"))
	if err != nil {
		log.Fatalf("error en TOTP_ENCRYPTION_KEY: %v", err)
	}
	auth := services.NewAuthService(users, permissions, sessions, passwords, twoFactor, secrets, tokens, audits, services.AuthConfig{
		ChallengeTTL:       envDuration("AUTH_CHALLENGE_TTL", 5*time.Minute),
		AccessTTL:          envDuration("AUTH_ACCESS_TTL", 15*time.Minute),
		RefreshTTL:         envDuration("AUTH_REFRESH_TTL", 24*time.Hour),
		RememberRefreshTTL: envDuration("AUTH_REMEMBER_REFRESH_TTL", 30*24*time.Hour),
	})

	mux := http.NewServeMux()
	apphttp.RegisterAuthRoutes(mux, apphttp.NewAuthHandler(auth))
	server := &http.Server{Addr: envOrDefault("HTTP_ADDR", ":8080"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	log.Println("🛡️ Sistema inicializado correctamente. Esperando conexiones...")
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("error en servidor HTTP: %v", err)
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		log.Fatalf("%s inválido: %v", key, err)
	}
	return duration
}

type servicesUserRepository interface {
	servicesUserPort
}

type servicesUserPort interface {
	FindByEmail(context.Context, string) (*domain.Usuario, error)
	FindByID(context.Context, uuid.UUID) (*domain.Usuario, error)
	UpdateLastAccess(context.Context, uuid.UUID, time.Time) error
	SetTwoFactorSecret(context.Context, uuid.UUID, string) error
	EnableTwoFactor(context.Context, uuid.UUID) error
}

type servicesPermissionRepository interface {
	ListInstanceIDs(context.Context, uuid.UUID) ([]int, error)
}

type servicesSessionRepository interface {
	CreateChallenge(context.Context, ports.Session) error
	ConsumeChallenge(context.Context, string, time.Time) (*ports.Session, error)
	FindActiveRefresh(context.Context, string, time.Time) (*ports.Session, error)
	CreateRefreshSession(context.Context, ports.Session) error
	RotateRefreshSession(context.Context, string, time.Time, ports.Session) error
	RevokeRefreshSession(context.Context, string, time.Time) error
}

type servicesAuditRepository interface {
	Record(context.Context, *domain.Auditoria) error
}

func newMemoryRepositories() (servicesUserRepository, servicesPermissionRepository, servicesSessionRepository, servicesAuditRepository) {
	hasher := security.BcryptHasher{}
	password := envOrDefault("DEMO_PASSWORD", "Centinela123!")
	hash, err := hasher.Hash(password)
	if err != nil {
		log.Fatalf("no se pudo crear el usuario demo: %v", err)
	}
	user := domain.Usuario{ID: uuid.New(), OrganizacionID: uuid.New(), NombreCompleto: "Usuario Demo", EmailUsuario: envOrDefault("DEMO_EMAIL", "demo@centinela.local"), NombreUsuario: "demo", ContrasenaHash: hash, Rol: "ADMIN", Activo: true, DebeCambiarContrasena: false}
	log.Printf("[memory] usuario demo: email=%s password=%s", user.EmailUsuario, password)
	return memory.NewUserRepository(user), memory.NewPermissionRepository(user.ID, []int{100, 101}), memory.NewSessionRepository(), memory.AuditRepository{}
}