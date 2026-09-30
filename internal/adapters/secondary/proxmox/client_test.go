package proxmox_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/proxmox"
	"el-centinela/internal/core/ports"
)

const (
	tokenIDValido     = "centinela-api@pve!backend-token"
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

func TestClient_ListarInstancias_Timeout(t *testing.T) {
	// Servidor que tarda más que el deadline del contexto
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	client := proxmox.NewClient(server.URL, "pve1", tokenIDValido, tokenSecretValido, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := client.ListarInstancias(ctx)
	if !errors.Is(err, ports.ErrProxmoxTimeout) {
		t.Fatalf("Se esperaba ErrProxmoxTimeout, pero se obtuvo: %v", err)
	}
	if !errors.Is(err, ports.ErrProxmoxNoDisponible) {
		t.Errorf("El timeout debe envolver también ErrProxmoxNoDisponible: %v", err)
	}
}

// proxmoxQueRechaza responde a cualquier request con un 500. Si enLineaDeEstado
// es true, pone el mensaje en la línea de estado HTTP (como el Proxmox real) y
// el cuerpo {"data":null}; si no, lo pone en el cuerpo (como el simulador).
func proxmoxQueRechaza(t *testing.T, mensaje string, enLineaDeEstado bool) *proxmox.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !enLineaDeEstado {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "message": mensaje + "\n"})
			return
		}
		conn, buf, _ := w.(http.Hijacker).Hijack()
		defer conn.Close()
		cuerpo := `{"data":null}`
		fmt.Fprintf(buf, "HTTP/1.1 500 %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", mensaje, len(cuerpo), cuerpo)
		_ = buf.Flush()
	}))
	t.Cleanup(server.Close)
	return proxmox.NewClient(server.URL, "pve1", tokenIDValido, tokenSecretValido, nil)
}

func TestClient_BloqueoDeProxmoxEsInstanciaOcupada(t *testing.T) {
	mensajes := []string{
		"can't lock file '/var/lock/qemu-server/lock-110.conf' - got timeout",
		"VM 100 is locked (snapshot)",
		"CT 101 is locked (backup)",
	}
	for _, mensaje := range mensajes {
		for _, enLinea := range []bool{false, true} {
			_, err := proxmoxQueRechaza(t, mensaje, enLinea).ListarInstancias(context.Background())
			if !errors.Is(err, ports.ErrInstanciaOcupada) {
				t.Errorf("%q (en línea de estado: %v): se esperaba ErrInstanciaOcupada, vino %v", mensaje, enLinea, err)
			}
			if errors.Is(err, ports.ErrProxmoxNoDisponible) {
				t.Errorf("%q: un bloqueo NO es Proxmox caído (no debe terminar en 502)", mensaje)
			}
		}
	}
}

func TestClient_Otro500SigueSiendoNoDisponible(t *testing.T) {
	for _, enLinea := range []bool{false, true} {
		_, err := proxmoxQueRechaza(t, "unable to read config", enLinea).ListarInstancias(context.Background())
		if !errors.Is(err, ports.ErrProxmoxNoDisponible) || errors.Is(err, ports.ErrInstanciaOcupada) {
			t.Errorf("Un 500 que no es de bloqueo debe seguir siendo ErrProxmoxNoDisponible: %v", err)
		}
	}
}
