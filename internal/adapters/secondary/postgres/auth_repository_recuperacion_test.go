package postgres

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	pg "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Verifica el DoD contra PostgreSQL real: aunque muchos requests fallen a la
// vez, FOR UPDATE serializa el contador y el código queda invalidado al tercer
// intento, sin actualizaciones perdidas.
func TestConfirmarRecuperacionConBloqueo_ConcurrenciaNoEludeTresIntentos(t *testing.T) {
	_ = godotenv.Load("../../../../.env", "../../../.env", "../../.env", ".env")
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		t.Skip("Saltando test de integración: DB_DSN no está configurada")
	}
	db, err := gorm.Open(pg.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil || db.Exec("SELECT 1").Error != nil {
		t.Skipf("Saltando test de integración: no se pudo conectar a PostgreSQL: %v", err)
	}

	sufijo := uuid.NewString()[:8]
	org := domain.Organizacion{ID: uuid.New(), NombreOrg: "Test recuperación " + sufijo, CreadoPor: uuid.Nil}
	codigo := "123456"
	expira := time.Now().Add(10 * time.Minute)
	usuario := domain.Usuario{
		ID: uuid.New(), OrganizacionID: org.ID, NombreCompleto: "Test Recuperación",
		NombreUsuario:  "test-recuperacion-" + sufijo,
		EmailUsuario:   "test-recuperacion-" + sufijo + "@elcentinela.local",
		ContrasenaHash: "hash-anterior", Rol: "OPERATOR", Activo: true,
		CodigoRecuperacion: &codigo, ExpiracionCodigo: &expira,
	}
	if err := db.Create(&org).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&usuario).Error; err != nil {
		db.Delete(&domain.Organizacion{}, "id = ?", org.ID)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Delete(&domain.Usuario{}, "id = ?", usuario.ID)
		db.Delete(&domain.Organizacion{}, "id = ?", org.ID)
	})

	repo := NewAuthRepository(db)
	const solicitudes = 20
	errores := make(chan error, solicitudes)
	inicio := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < solicitudes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-inicio
			_, err := repo.ConfirmarRecuperacionConBloqueo(context.Background(), usuario.EmailUsuario, "000000", time.Now(), func(string) (string, error) {
				return "hash-nuevo", nil
			})
			errores <- err
		}()
	}
	close(inicio)
	wg.Wait()
	close(errores)

	for err := range errores {
		if !errors.Is(err, ports.ErrCodigoRecuperacionInvalido) && !errors.Is(err, ports.ErrIntentosRecuperacionAgotados) {
			t.Fatalf("error concurrente inesperado: %v", err)
		}
	}
	var guardado domain.Usuario
	if err := db.First(&guardado, "id = ?", usuario.ID).Error; err != nil {
		t.Fatal(err)
	}
	if guardado.IntentosRecuperacion != 3 {
		t.Fatalf("intentos_recuperacion = %d, se esperaba exactamente 3", guardado.IntentosRecuperacion)
	}
	if guardado.CodigoRecuperacion != nil || guardado.ExpiracionCodigo != nil {
		t.Fatal("el código y su expiración deben quedar invalidados al tercer fallo")
	}
}
