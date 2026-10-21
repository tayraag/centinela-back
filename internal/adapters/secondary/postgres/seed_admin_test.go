package postgres

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/infrastructure/crypto"
)

func TestCredencialesIniciales(t *testing.T) {
	casos := []struct {
		nombre, appEnv, email, clave string
		ok, generada                 bool
	}{
		{"producción sin variables", "production", "", "", false, false},
		{"producción con variables", "production", "ops@empresa.com", "ClaveSegura2026!", true, false},
		{"desarrollo sin variables", "development", "", "", true, true},
		{"sin APP_ENV y sin variables", "", "", "", true, true},
		{"desarrollo con variables", "development", "dev@empresa.com", "ClaveSegura2026!", true, false},
		{"solo el email", "production", "ops@empresa.com", "", false, false},
		{"solo la clave", "development", "", "ClaveSegura2026!", false, false},
		{"clave corta", "production", "ops@empresa.com", "corta", false, false},
		{"email inválido", "production", "ops", "ClaveSegura2026!", false, false},
	}
	for _, c := range casos {
		email, clave, generada, ok, motivo := credencialesIniciales(c.appEnv, c.email, c.clave)
		if ok != c.ok || generada != c.generada {
			t.Errorf("%s: ok=%v generada=%v (%s), se esperaba ok=%v generada=%v", c.nombre, ok, generada, motivo, c.ok, c.generada)
			continue
		}
		if clave == "Admin123!" {
			t.Errorf("%s: nunca debe usarse la clave fija Admin123!", c.nombre)
		}
		if ok && !generada && (email != c.email || clave != c.clave) {
			t.Errorf("%s: debe usar las variables: %q %q", c.nombre, email, clave)
		}
		if !ok && (email != "" || clave != "" || motivo == "") {
			t.Errorf("%s: sin sembrado no hay credenciales y sí un motivo: %q %q %q", c.nombre, email, clave, motivo)
		}
	}
	_, a, _, _, _ := credencialesIniciales("development", "", "")
	_, b, _, _, _ := credencialesIniciales("development", "", "")
	if a == b || len(a) < 8 {
		t.Errorf("La clave de desarrollo debe ser aleatoria: %q %q", a, b)
	}
}

// arrancar levanta InitDB sobre una base vacía con las variables dadas y
// devuelve los usuarios creados y lo que se escribió en el log.
func arrancar(t *testing.T, appEnv, email, clave string) ([]domain.Usuario, string) {
	t.Helper()
	t.Setenv("DB_DSN", baseVacia(t))
	t.Setenv("APP_ENV", appEnv)
	t.Setenv("INITIAL_ADMIN_EMAIL", email)
	t.Setenv("INITIAL_ADMIN_PASSWORD", clave)
	var salida bytes.Buffer
	anterior := log.Writer()
	log.SetOutput(&salida)
	db, err := InitDB()
	log.SetOutput(anterior)
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer cerrar(db)
	var usuarios []domain.Usuario
	db.Find(&usuarios)
	return usuarios, salida.String()
}

// DoD: en producción sin variables no se crea ningún usuario ni se escribe
// una clave en el log.
func TestSeedAdmin_ProduccionSinVariablesNoCreaNada(t *testing.T) {
	usuarios, salida := arrancar(t, "production", "", "")
	if len(usuarios) != 0 {
		t.Fatalf("No debe crearse ningún usuario: %d", len(usuarios))
	}
	if strings.Contains(salida, "Admin123!") || strings.Contains(salida, "Clave") || !strings.Contains(salida, "cmd/seed") {
		t.Errorf("El log debe avisar sin mostrar claves:\n%s", salida)
	}
}

func TestSeedAdmin_ProduccionConVariables(t *testing.T) {
	const clave = "ClaveSegura2026!"
	usuarios, salida := arrancar(t, "production", "ops@empresa.com", clave)
	if len(usuarios) != 1 {
		t.Fatalf("Se esperaba un administrador, hay %d", len(usuarios))
	}
	u := usuarios[0]
	if u.EmailUsuario != "ops@empresa.com" || u.Rol != "ADMIN" || !u.CambioContrasena || !crypto.VerificarContrasena(u.ContrasenaHash, clave) {
		t.Errorf("Administrador inesperado: %+v", u)
	}
	if strings.Contains(salida, clave) || strings.Contains(salida, "Admin123!") {
		t.Errorf("La clave no debe aparecer en el log:\n%s", salida)
	}
}

func TestSeedAdmin_DesarrolloConClaveAleatoria(t *testing.T) {
	usuarios, salida := arrancar(t, "development", "", "")
	if len(usuarios) != 1 {
		t.Fatalf("Se esperaba un administrador, hay %d", len(usuarios))
	}
	u := usuarios[0]
	if crypto.VerificarContrasena(u.ContrasenaHash, "Admin123!") {
		t.Error("No debe usarse la clave fija Admin123!")
	}
	_, despues, _ := strings.Cut(salida, "Clave temporal (solo desarrollo): ")
	clave := strings.TrimSpace(strings.SplitN(despues, "\n", 2)[0])
	if clave == "" || !crypto.VerificarContrasena(u.ContrasenaHash, clave) || !u.CambioContrasena {
		t.Errorf("La clave del log debe ser la del usuario y exigir cambio: %q\n%s", clave, salida)
	}
}
