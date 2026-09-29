package proxmox_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"el-centinela/internal/adapters/secondary/proxmox"
	"el-centinela/internal/core/ports"
)

const (
	tokenIDValido     = "centi-api@pve!back-token"
	tokenSecretValido = "secret-12345"
)

// nuevoProxmoxSimulado levanta un Proxmox falso que, igual que el real, exige
// el header de API Token y responde 401 si no coincide. recursos es lo que
// devuelve cluster/resources.
func nuevoProxmoxSimulado(t *testing.T, recursos []map[string]any) *httptest.Server {
	t.Helper()
	esperado := "PVEAPIToken=" + tokenIDValido + "=" + tokenSecretValido

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != esperado {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"data":null,"message":"invalid token value!"}`))
			return
		}
		if r.URL.Path != "/api2/json/cluster/resources" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": recursos})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestClient_ListarInstancias_ConToken(t *testing.T) {
	// Respuesta cruda de Proxmox: incluye VM (qemu), contenedor (lxc), nodo y storage
	server := nuevoProxmoxSimulado(t, []map[string]any{
		{"vmid": 100, "type": "qemu", "name": "vm-prod-1", "node": "pve1", "status": "running"},
		{"vmid": 101, "type": "lxc", "name": "ct-dns", "node": "pve1", "status": "stopped"},
		{"id": "node/pve1", "type": "node", "node": "pve1", "status": "online"},
		{"id": "storage/local-lvm", "type": "storage", "node": "pve1", "status": "available"},
	})

	// Con el sufijo /api2/json, como viene en la config real: no debe duplicarse.
	client := proxmox.NewClient(server.URL+"/api2/json", "pve1", tokenIDValido, tokenSecretValido, nil)

	instancias, err := client.ListarInstancias(context.Background())
	if err != nil {
		t.Fatalf("Error inesperado al listar instancias: %v", err)
	}

	// Debe haber conservado únicamente las 2 instancias (qemu y lxc), descartando node y storage
	if len(instancias) != 2 {
		t.Fatalf("Se esperaban 2 instancias, pero se obtuvieron %d", len(instancias))
	}
	if instancias[0].Vmid != 100 || instancias[0].Tipo != "qemu" {
		t.Errorf("Instancia 0 incorrecta: %+v", instancias[0])
	}
	if instancias[1].Vmid != 101 || instancias[1].Tipo != "lxc" {
		t.Errorf("Instancia 1 incorrecta: %+v", instancias[1])
	}
}

func TestClient_ListarInstancias_TokenRechazado(t *testing.T) {
	server := nuevoProxmoxSimulado(t, nil)
	client := proxmox.NewClient(server.URL, "pve1", tokenIDValido, "secret-incorrecto", nil)

	_, err := client.ListarInstancias(context.Background())
	if !errors.Is(err, ports.ErrProxmoxCredenciales) {
		t.Fatalf("Se esperaba ErrProxmoxCredenciales, pero se obtuvo: %v", err)
	}
	// Hacia el cliente HTTP tiene que seguir mapeando a 502 PROXMOX_UNAVAILABLE.
	if !errors.Is(err, ports.ErrProxmoxNoDisponible) {
		t.Errorf("El error de credenciales debe envolver también ErrProxmoxNoDisponible: %v", err)
	}
}

func TestClient_ListarInstancias_SinTokenNoLlamaAProxmox(t *testing.T) {
	llamado := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llamado = true
	}))
	defer server.Close()

	client := proxmox.NewClient(server.URL, "pve1", "", "", nil)
	if client.TokenConfigurado() {
		t.Fatal("TokenConfigurado debe ser false sin token")
	}

	_, err := client.ListarInstancias(context.Background())
	if !errors.Is(err, ports.ErrProxmoxCredenciales) {
		t.Fatalf("Se esperaba ErrProxmoxCredenciales, pero se obtuvo: %v", err)
	}
	if llamado {
		t.Error("Sin token configurado no se debe llamar a Proxmox")
	}
}

func TestClient_ListarInstancias_URLMalConfigurada(t *testing.T) {
	server := nuevoProxmoxSimulado(t, nil)
	// Una ruta base incorrecta hace que Proxmox responda 404.
	client := proxmox.NewClient(server.URL+"/ruta-incorrecta", "pve1", tokenIDValido, tokenSecretValido, nil)

	_, err := client.ListarInstancias(context.Background())
	if !errors.Is(err, ports.ErrProxmoxNoDisponible) {
		t.Fatalf("Se esperaba ErrProxmoxNoDisponible, pero se obtuvo: %v", err)
	}
	if errors.Is(err, ports.ErrInstanciaNoEncontrada) {
		t.Error("Un 404 por URL mal configurada no debe reportarse como instancia inexistente")
	}
}

func TestClient_ObtenerInstancia_VmidInexistente(t *testing.T) {
	server := nuevoProxmoxSimulado(t, []map[string]any{
		{"vmid": 100, "type": "qemu", "name": "vm-prod-1", "node": "pve1", "status": "running"},
	})
	client := proxmox.NewClient(server.URL, "pve1", tokenIDValido, tokenSecretValido, nil)

	_, err := client.ObtenerInstancia(context.Background(), 999)
	if !errors.Is(err, ports.ErrInstanciaNoEncontrada) {
		t.Fatalf("Se esperaba ErrInstanciaNoEncontrada, pero se obtuvo: %v", err)
	}
}

func TestClient_ListarInstancias_ProxmoxCaido(t *testing.T) {
	// Servidor que responde con error 500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := proxmox.NewClient(server.URL, "pve1", "token", "secret", nil)

	_, err := client.ListarInstancias(context.Background())
	if err == nil {
		t.Fatal("Se esperaba un error cuando Proxmox falla, pero retornó nil")
	}
	if !errors.Is(err, ports.ErrProxmoxNoDisponible) {
		t.Errorf("Se esperaba ErrProxmoxNoDisponible, pero se obtuvo: %v", err)
	}
	if errors.Is(err, ports.ErrProxmoxCredenciales) {
		t.Errorf("Un 500 de Proxmox no debe reportarse como error de credenciales: %v", err)
	}
}
