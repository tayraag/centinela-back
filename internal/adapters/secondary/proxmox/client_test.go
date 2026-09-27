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

func TestClient_ListarInstancias_ConToken(t *testing.T) {
	expectedTokenID := "centinela-api@pve!backend-token"
	expectedSecret := "secret-12345"
	expectedAuthHeader := "PVEAPIToken=" + expectedTokenID + "=" + expectedSecret

	// Mock del servidor Proxmox
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Validar que la cabecera Authorization tenga el formato de API Token
		auth := r.Header.Get("Authorization")
		if auth != expectedAuthHeader {
			t.Errorf("Se esperaba Authorization: %s, pero se recibió: %s", expectedAuthHeader, auth)
		}

		if r.URL.Path != "/api2/json/cluster/resources" {
			t.Errorf("Path inesperado: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// 2. Simular respuesta cruda de Proxmox: incluye VM (qemu), contenedor (lxc), nodo y storage
		resp := map[string]interface{}{
			"data": []map[string]interface{}{
				{"vmid": 100, "type": "qemu", "name": "vm-prod-1", "node": "pve1", "status": "running"},
				{"vmid": 101, "type": "lxc", "name": "ct-dns", "node": "pve1", "status": "stopped"},
				{"id": "node/pve1", "type": "node", "node": "pve1", "status": "online"},
				{"id": "storage/local-lvm", "type": "storage", "node": "pve1", "status": "available"},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := proxmox.NewClient(server.URL, "pve1", expectedTokenID, expectedSecret, "", "", nil)

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

func TestClient_ListarInstancias_ProxmoxCaido(t *testing.T) {
	// Servidor que responde con error 500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := proxmox.NewClient(server.URL, "pve1", "token", "secret", "", "", nil)

	_, err := client.ListarInstancias(context.Background())
	if err == nil {
		t.Fatal("Se esperaba un error cuando Proxmox falla, pero retornó nil")
	}

	if !errors.Is(err, ports.ErrProxmoxNoDisponible) {
		t.Errorf("Se esperaba ErrProxmoxNoDisponible, pero se obtuvo: %v", err)
	}
}
