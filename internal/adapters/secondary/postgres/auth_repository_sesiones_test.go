package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/memoria"
	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"
	"el-centinela/internal/infrastructure/crypto"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/pquerna/otp/totp"
	pg "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type auditoriaNula struct{}

func (auditoriaNula) Registrar(context.Context, ports.RegistrarAuditoriaInput) {}
func (auditoriaNula) ListarAuditoria(context.Context, uuid.UUID, ports.FiltrosAuditoria, ports.OpcionesAuditoria) (*ports.PaginaAuditoria, error) {
	return nil, nil
}
func (auditoriaNula) ExportarCSV(context.Context, uuid.UUID, ports.FiltrosAuditoria) ([]byte, error) {
	return nil, nil
}
func (auditoriaNula) ExportarJSON(context.Context, uuid.UUID, ports.FiltrosAuditoria) ([]byte, error) {
	return nil, nil
}

// Test de integración del DoD contra el PostgreSQL local (se saltea si no hay base):
// login + 2FA + 10 renovaciones deja exactamente 1 fila en sesiones_activas.
func TestIntegracion_UnaSesionUnaFila(t *testing.T) {
	_ = godotenv.Load("../../../../.env", "../../../.env", "../../.env", ".env")
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		t.Skip("Saltando test de integración: DB_DSN no está configurada en .env")
	}
	db, err := gorm.Open(pg.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil || db.Exec("SELECT 1").Error != nil {
		t.Skipf("Saltando test de integración: no se pudo conectar a PostgreSQL: %v", err)
	}
	if err := migrarSesionesAUnaFilaPorSesion(db); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&domain.SesionActiva{}); err != nil {
		t.Fatal(err)
	}

	claveCifrado := "0123456789abcdef0123456789abcdef"
	t.Setenv("JWT_SECRET", "secreto-jwt-de-prueba-con-mas-de-32-caracteres")
	t.Setenv("TOTP_ENCRYPTION_KEY", claveCifrado)

	// Usuario de prueba propio, que se borra al final (sus sesiones caen en cascada).
	sufijo := uuid.NewString()[:8]
	org := domain.Organizacion{ID: uuid.New(), NombreOrg: "Test sesiones " + sufijo, CreadoPor: uuid.Nil}
	clave, _ := totp.Generate(totp.GenerateOpts{Issuer: "El Centinela", AccountName: "t"})
	cifrado, _ := crypto.CifrarSecreto(clave.Secret(), claveCifrado)
	hash, _ := crypto.HashContrasena("Clave123!")
	usuario := domain.Usuario{
		ID: uuid.New(), OrganizacionID: org.ID, NombreCompleto: "Test Sesiones", NombreUsuario: "test-sesiones-" + sufijo,
		EmailUsuario: "test-sesiones-" + sufijo + "@elcentinela.local", ContrasenaHash: hash, Rol: "OPERATOR",
		Activo: true, TotpVinculado: true, SecretoTotpCifrado: cifrado,
	}
	if err := db.Create(&org).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&usuario).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Delete(&domain.Usuario{}, "id = ?", usuario.ID)
		db.Delete(&domain.Organizacion{}, "id = ?", org.ID)
	})

	ctx := context.Background()
	svc := services.NewAuthService(NewAuthRepository(db), memoria.Nuevo(), nil, auditoriaNula{})
	contar := func() (total, activas int64) {
		db.Model(&domain.SesionActiva{}).Where("usuario_id = ?", usuario.ID).Count(&total)
		db.Model(&domain.SesionActiva{}).Where("usuario_id = ? AND activa = true", usuario.ID).Count(&activas)
		return
	}

	// Login: el pre-2FA no deja filas.
	res, err := svc.Login(ctx, usuario.EmailUsuario, "Clave123!")
	if err != nil {
		t.Fatal(err)
	}
	if total, _ := contar(); total != 0 {
		t.Fatalf("El login no debe crear filas en sesiones_activas, hay %d", total)
	}

	// 2FA: exactamente una fila y fecha_ultimo_acceso registrada.
	pre, _ := crypto.VerificarToken(res.JWTTemporal, os.Getenv("JWT_SECRET"))
	codigo, _ := totp.GenerateCode(clave.Secret(), time.Now())
	tokens, err := svc.VerificarTotp(ctx, pre.ID, codigo)
	if err != nil {
		t.Fatal(err)
	}
	var enBase domain.Usuario
	db.First(&enBase, "id = ?", usuario.ID)
	if enBase.FechaUltimoAcceso == nil {
		t.Error("fecha_ultimo_acceso debe quedar registrada al superar el 2FA")
	}

	// 10 renovaciones: siguen siendo 1 fila.
	for i := 0; i < 10; i++ {
		if _, err := svc.RefrescarToken(ctx, tokens.RefreshToken); err != nil {
			t.Fatalf("Renovación %d: %v", i+1, err)
		}
	}
	if total, activas := contar(); total != 1 || activas != 1 {
		t.Fatalf("DoD: después de login + 2FA + 10 renovaciones debe haber 1 fila activa, hay %d (%d activas)", total, activas)
	}

	// Logout: la misma fila queda inactiva.
	if err := svc.CerrarSesion(ctx, tokens.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if total, activas := contar(); total != 1 || activas != 0 {
		t.Fatalf("Después del logout debe quedar 1 fila inactiva, hay %d (%d activas)", total, activas)
	}
}
