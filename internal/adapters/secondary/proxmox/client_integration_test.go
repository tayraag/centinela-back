package proxmox_test

import (
	"context"
	"os"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/proxmox"

	"github.com/joho/godotenv"
)

// TestIntegracionProxmoxReal prueba la conexión real contra Proxmox VE
// utilizando las credenciales cargadas en tu archivo .env.
// Si no hay credenciales reales o está el placeholder, se saltea automáticamente.
func TestIntegracionProxmoxReal(t *testing.T) {
	_ = godotenv.Load("../../../../.env", "../../../.env", "../../.env", ".env")

	url := os.Getenv("PROXMOX_URL")
	if url == "" {
		url = os.Getenv("PROXMOX_BASE_URL")
	}
	tokenID := os.Getenv("PROXMOX_TOKEN_ID")
	tokenSecret := os.Getenv("PROXMOX_TOKEN_SECRET")

	if url == "" || tokenSecret == "" || tokenSecret == "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx" {
		t.Skip("Saltando test de integración: PROXMOX_TOKEN_SECRET aún no tiene un valor real en el archivo .env")
	}

	client := proxmox.NewClient(
		url,
		os.Getenv("PROXMOX_NODE"),
		tokenID,
		tokenSecret,
		os.Getenv("PROXMOX_USERNAME"),
		os.Getenv("PROXMOX_PASSWORD"),
		proxmox.BuildTLSConfig(),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	instancias, err := client.ListarInstancias(ctx)
	if err != nil {
		t.Fatalf("❌ Error al conectar a Proxmox VE: %v", err)
	}

	t.Logf("✅ ¡Conexión exitosa a Proxmox VE! Se detectaron %d instancias:", len(instancias))
	for _, inst := range instancias {
		t.Logf("   • [%s] ID: %d | Nombre: %s | Nodo: %s | Estado: %s", inst.Tipo, inst.Vmid, inst.Nombre, inst.Nodo, inst.Estado)
	}
}
