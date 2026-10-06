package proxmox_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// nuevoProxmoxSimuladoInterfaces levanta un Proxmox falso que, igual que el
// real, exige el header de API Token. Responde siempre con el status y el
// cuerpo dados por el test y registra la ruta efectivamente solicitada, para
// poder afirmar que ObtenerInterfaces pegó en el endpoint canónico.
func nuevoProxmoxSimuladoInterfaces(t *testing.T, status int, cuerpo string) (*httptest.Server, *string) {
	t.Helper()
	esperado := "PVEAPIToken=" + tokenIDValido + "=" + tokenSecretValido
	rutaRecibida := new(string)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*rutaRecibida = r.URL.Path
		if r.Header.Get("Authorization") != esperado {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"data":null,"message":"invalid token value!"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(cuerpo))
	}))
	t.Cleanup(server.Close)
	return server, rutaRecibida
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

func TestClient_ObtenerInterfaces_Qemu(t *testing.T) {
	// Payload real del QEMU Guest Agent: data.result[] con prefix como número
	// entero y tipo ipv4/ipv6.
	const payload = `{
		"data": {
			"result": [
				{
					"name": "lo",
					"hardware-address": "00:00:00:00:00:00",
					"ip-addresses": [
						{"ip-address": "127.0.0.1", "ip-address-type": "ipv4", "prefix": 8},
						{"ip-address": "::1", "ip-address-type": "ipv6", "prefix": 128}
					]
				},
				{
					"name": "eth0",
					"hardware-address": "BC:24:11:00:00:01",
					"ip-addresses": [
						{"ip-address": "10.10.20.50", "ip-address-type": "ipv4", "prefix": 24},
						{"ip-address": "fe80::be24:11ff:fe00:1", "ip-address-type": "ipv6", "prefix": 64}
					]
				}
			]
		}
	}`
	server, ruta := nuevoProxmoxSimuladoInterfaces(t, http.StatusOK, payload)
	client := proxmox.NewClient(server.URL, "pve1", tokenIDValido, tokenSecretValido, nil)

	interfaces, err := client.ObtenerInterfaces(context.Background(), "pve1", ports.TipoInstanciaQemu, 100)
	if err != nil {
		t.Fatalf("Error inesperado al obtener interfaces qemu: %v", err)
	}
	if esperada := "/api2/json/nodes/pve1/qemu/100/agent/network-get-interfaces"; *ruta != esperada {
		t.Errorf("Ruta llamada incorrecta: se esperaba %q, se obtuvo %q", esperada, *ruta)
	}
	if len(interfaces) != 2 {
		t.Fatalf("Se esperaban 2 interfaces, pero se obtuvieron %d", len(interfaces))
	}

	lo := interfaces[0]
	if lo.Nombre != "lo" || lo.MAC != "00:00:00:00:00:00" {
		t.Errorf("Interfaz lo incorrecta: %+v", lo)
	}
	if len(lo.Direcciones) != 2 {
		t.Fatalf("lo: se esperaban 2 direcciones, se obtuvieron %d", len(lo.Direcciones))
	}
	if lo.Direcciones[0] != (ports.DireccionIP{IP: "127.0.0.1", Prefijo: 8, Version: 4}) {
		t.Errorf("lo IPv4 con prefix numérico incorrecta: %+v", lo.Direcciones[0])
	}
	if lo.Direcciones[1] != (ports.DireccionIP{IP: "::1", Prefijo: 128, Version: 6}) {
		t.Errorf("lo IPv6 incorrecta: %+v", lo.Direcciones[1])
	}

	eth0 := interfaces[1]
	if eth0.Nombre != "eth0" {
		t.Errorf("Nombre de eth0 incorrecto: %q", eth0.Nombre)
	}
	// El adaptador normaliza la MAC a minúsculas.
	if eth0.MAC != "bc:24:11:00:00:01" {
		t.Errorf("MAC de eth0 no normalizada a minúsculas: %q", eth0.MAC)
	}
	if len(eth0.Direcciones) != 2 {
		t.Fatalf("eth0: se esperaban 2 direcciones, se obtuvieron %d", len(eth0.Direcciones))
	}
	if eth0.Direcciones[0] != (ports.DireccionIP{IP: "10.10.20.50", Prefijo: 24, Version: 4}) {
		t.Errorf("eth0 IPv4 incorrecta: %+v", eth0.Direcciones[0])
	}
	if eth0.Direcciones[1] != (ports.DireccionIP{IP: "fe80::be24:11ff:fe00:1", Prefijo: 64, Version: 6}) {
		t.Errorf("eth0 IPv6 incorrecta: %+v", eth0.Direcciones[1])
	}
}

func TestClient_ObtenerInterfaces_LXC(t *testing.T) {
	t.Run("con interfaces", func(t *testing.T) {
		// Payload de /nodes/{node}/lxc/{vmid}/interfaces: prefix como string
		// ("24") y como número (64), igual que PVE 9.2.2.
		const payload = `{
			"data": [
				{
					"name": "eth0",
					"hwaddr": "BC:24:11:AA:BB:CC",
					"ip-addresses": [
						{"ip-address": "10.10.20.60", "ip-address-type": "inet", "prefix": "24"},
						{"ip-address": "fe80::be24:11ff:feaa:bbcc", "ip-address-type": "inet6", "prefix": 64}
					]
				},
				{
					"name": "lo",
					"hwaddr": "00:00:00:00:00:00",
					"ip-addresses": [
						{"ip-address": "127.0.0.1", "ip-address-type": "inet", "prefix": "8"}
					]
				}
			]
		}`
		server, ruta := nuevoProxmoxSimuladoInterfaces(t, http.StatusOK, payload)
		client := proxmox.NewClient(server.URL, "pve1", tokenIDValido, tokenSecretValido, nil)

		interfaces, err := client.ObtenerInterfaces(context.Background(), "pve1", ports.TipoInstanciaLXC, 101)
		if err != nil {
			t.Fatalf("Error inesperado al obtener interfaces lxc: %v", err)
		}
		if esperada := "/api2/json/nodes/pve1/lxc/101/interfaces"; *ruta != esperada {
			t.Errorf("Ruta llamada incorrecta: se esperaba %q, se obtuvo %q", esperada, *ruta)
		}
		if len(interfaces) != 2 {
			t.Fatalf("Se esperaban 2 interfaces, pero se obtuvieron %d", len(interfaces))
		}

		eth0 := interfaces[0]
		if eth0.Nombre != "eth0" {
			t.Errorf("Nombre de eth0 incorrecto: %q", eth0.Nombre)
		}
		// El adaptador normaliza la MAC a minúsculas (lxc la expone en hwaddr).
		if eth0.MAC != "bc:24:11:aa:bb:cc" {
			t.Errorf("MAC de eth0 no normalizada a minúsculas: %q", eth0.MAC)
		}
		if len(eth0.Direcciones) != 2 {
			t.Fatalf("eth0: se esperaban 2 direcciones, se obtuvieron %d", len(eth0.Direcciones))
		}
		if eth0.Direcciones[0] != (ports.DireccionIP{IP: "10.10.20.60", Prefijo: 24, Version: 4}) {
			t.Errorf("eth0 IPv4 con prefix string incorrecta: %+v", eth0.Direcciones[0])
		}
		if eth0.Direcciones[1] != (ports.DireccionIP{IP: "fe80::be24:11ff:feaa:bbcc", Prefijo: 64, Version: 6}) {
			t.Errorf("eth0 IPv6 con prefix numérico incorrecta: %+v", eth0.Direcciones[1])
		}
		if interfaces[1].Direcciones[0] != (ports.DireccionIP{IP: "127.0.0.1", Prefijo: 8, Version: 4}) {
			t.Errorf("lo IPv4 incorrecta: %+v", interfaces[1].Direcciones[0])
		}
	})

	t.Run("contenedor detenido", func(t *testing.T) {
		// Proxmox responde {"data": null} cuando el contenedor está apagado.
		server, ruta := nuevoProxmoxSimuladoInterfaces(t, http.StatusOK, `{"data": null}`)
		client := proxmox.NewClient(server.URL, "pve1", tokenIDValido, tokenSecretValido, nil)

		interfaces, err := client.ObtenerInterfaces(context.Background(), "pve1", ports.TipoInstanciaLXC, 101)
		if err != nil {
			t.Fatalf("Un contenedor detenido no debe devolver error: %v", err)
		}
		if esperada := "/api2/json/nodes/pve1/lxc/101/interfaces"; *ruta != esperada {
			t.Errorf("Ruta llamada incorrecta: se esperaba %q, se obtuvo %q", esperada, *ruta)
		}
		if len(interfaces) != 0 {
			t.Errorf("Un contenedor detenido debe devolver una lista vacía, se obtuvo: %+v", interfaces)
		}
	})
}

func TestClient_ObtenerInterfaces_ErrorYTimeout(t *testing.T) {
	t.Run("guest agent no corriendo", func(t *testing.T) {
		// Proxmox responde 500 con este mensaje cuando la VM está apagada o no
		// tiene el QEMU Guest Agent corriendo.
		server, _ := nuevoProxmoxSimuladoInterfaces(t, http.StatusInternalServerError,
			`{"data":null,"message":"QEMU guest agent is not running"}`)
		client := proxmox.NewClient(server.URL, "pve1", tokenIDValido, tokenSecretValido, nil)

		_, err := client.ObtenerInterfaces(context.Background(), "pve1", ports.TipoInstanciaQemu, 100)
		if err == nil {
			t.Fatal("Se esperaba un error cuando el guest agent no corre, pero retornó nil")
		}
		if !errors.Is(err, ports.ErrProxmoxNoDisponible) {
			t.Errorf("Se esperaba ErrProxmoxNoDisponible, pero se obtuvo: %v", err)
		}
		if errors.Is(err, ports.ErrProxmoxCredenciales) {
			t.Errorf("Un 500 del guest agent no debe reportarse como error de credenciales: %v", err)
		}
		if !strings.Contains(err.Error(), "QEMU guest agent is not running") {
			t.Errorf("El error debería conservar el mensaje de Proxmox: %v", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		// Servidor que tarda más que el deadline del contexto.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond)
		}))
		defer server.Close()

		client := proxmox.NewClient(server.URL, "pve1", tokenIDValido, tokenSecretValido, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		_, err := client.ObtenerInterfaces(ctx, "pve1", ports.TipoInstanciaQemu, 100)
		if !errors.Is(err, ports.ErrProxmoxTimeout) {
			t.Fatalf("Se esperaba ErrProxmoxTimeout, pero se obtuvo: %v", err)
		}
		if !errors.Is(err, ports.ErrProxmoxNoDisponible) {
			t.Errorf("El timeout debe envolver también ErrProxmoxNoDisponible: %v", err)
		}
	})
}
