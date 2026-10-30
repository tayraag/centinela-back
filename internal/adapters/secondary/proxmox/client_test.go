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
	client := proxmox.NewClient(server.URL+"/api2/json", tokenIDValido, tokenSecretValido, nil)

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
	client := proxmox.NewClient(server.URL, tokenIDValido, "secret-incorrecto", nil)

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

	client := proxmox.NewClient(server.URL, "", "", nil)
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
	client := proxmox.NewClient(server.URL+"/ruta-incorrecta", tokenIDValido, tokenSecretValido, nil)

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
	client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)

	_, err := client.ObtenerInstancia(context.Background(), 999)
	if !errors.Is(err, ports.ErrInstanciaNoEncontrada) {
		t.Fatalf("Se esperaba ErrInstanciaNoEncontrada, pero se obtuvo: %v", err)
	}
}

func TestClient_AprovisionarInstancia_ReservaVMIDYEnviaConfiguracion(t *testing.T) {
	casos := []struct {
		nombre   string
		input    ports.CrearInstanciaProxmoxInput
		path     string
		accion   string
		upid     string
		campos   map[string]string
		ausentes []string
	}{
		{
			nombre: "VM", path: "/api2/json/nodes/pve1/qemu", accion: ports.AccionProxmoxCrearVM,
			upid:   "UPID:pve1:1:2:3:qmcreate:501:root@pam:",
			input:  ports.CrearInstanciaProxmoxInput{Tipo: ports.TipoInstanciaQemu, Nodo: "pve1", Nombre: "web", Cores: 2, MemoriaMB: 4096, StorageID: "local-lvm", DiscoGB: 30, TemplateID: "local:iso/debian.iso", Bridge: "vmbr0", Firewall: true, IPv4Tipo: "dhcp"},
			campos: map[string]string{"name": "web", "ostype": "l26", "agent": "1", "scsi0": "local-lvm:30", "ide2": "local:iso/debian.iso,media=cdrom", "boot": "order=ide2;scsi0", "net0": "virtio,bridge=vmbr0,firewall=1", "ipconfig0": "ip=dhcp"},
		},
		{
			nombre: "LXC", path: "/api2/json/nodes/pve1/lxc", accion: ports.AccionProxmoxCrearLXC,
			upid:     "UPID:pve1:1:2:3:vzcreate:501:root@pam:",
			input:    ports.CrearInstanciaProxmoxInput{Tipo: ports.TipoInstanciaLXC, Nodo: "pve1", Nombre: "cache", Cores: 1, MemoriaMB: 1024, StorageID: "local-lvm", DiscoGB: 10, TemplateID: "local:vztmpl/debian.tar.zst", Bridge: "vmbr0", IPv4Tipo: "static", IPv4Address: "192.168.1.50", IPv4Mask: "24", IPv4Gateway: "192.168.1.1"},
			campos:   map[string]string{"hostname": "cache", "unprivileged": "1", "features": "nesting=1", "rootfs": "local-lvm:10", "ostemplate": "local:vztmpl/debian.tar.zst", "net0": "name=eth0,bridge=vmbr0,firewall=0,ip=192.168.1.50/24,gw=192.168.1.1"},
			ausentes: []string{"password"},
		},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			var secuencia []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				secuencia = append(secuencia, r.Method+" "+r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api2/json/cluster/nextid":
					_, _ = w.Write([]byte(`{"data":"501"}`))
				case r.Method == http.MethodPost && r.URL.Path == caso.path:
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					for clave, esperado := range caso.campos {
						if recibido := r.Form.Get(clave); recibido != esperado {
							t.Errorf("%s = %q, se esperaba %q", clave, recibido, esperado)
						}
					}
					for _, clave := range caso.ausentes {
						if _, presente := r.Form[clave]; presente {
							t.Errorf("%s no debe enviarse cuando no fue informado: %v", clave, r.Form[clave])
						}
					}
					if r.Form.Get("vmid") != "501" || r.Form.Get("cores") != fmt.Sprint(caso.input.Cores) || r.Form.Get("memory") != fmt.Sprint(caso.input.MemoriaMB) {
						t.Errorf("parámetros base incompletos: %v", r.Form)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": caso.upid})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)

			resultado, err := client.AprovisionarInstancia(context.Background(), caso.input)
			if err != nil {
				t.Fatal(err)
			}
			if resultado.VMID != 501 || resultado.UPID != caso.upid || resultado.Accion != caso.accion {
				t.Fatalf("resultado inesperado: %+v", resultado)
			}
			if len(secuencia) != 2 || secuencia[0] != "GET /api2/json/cluster/nextid" || secuencia[1] != "POST "+caso.path {
				t.Fatalf("secuencia inesperada: %v", secuencia)
			}
		})
	}
}

func TestClient_AprovisionarInstancia_MarcaFalloPrevioAlDespacho(t *testing.T) {
	var posts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
		}
		http.Error(w, "nextid no disponible", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)

	_, err := client.AprovisionarInstancia(context.Background(), ports.CrearInstanciaProxmoxInput{Tipo: ports.TipoInstanciaQemu, Nodo: "pve1"})
	if !errors.Is(err, ports.ErrCreacionNoDespachada) {
		t.Fatalf("se esperaba ErrCreacionNoDespachada, vino %v", err)
	}
	if !errors.Is(err, ports.ErrProxmoxNoDisponible) {
		t.Fatalf("el error debe conservar ErrProxmoxNoDisponible: %v", err)
	}
	if posts != 0 {
		t.Fatalf("no debía enviarse ningún POST, llegaron %d", posts)
	}
}

func TestClient_EliminarInstancia(t *testing.T) {
	casos := []struct {
		nombre string
		tipo   string
		path   string
		upid   string
	}{
		{"QEMU", ports.TipoInstanciaQemu, "/api2/json/nodes/pve1/qemu/501", "UPID:pve1:1:2:3:qmdestroy:501:root@pam:"},
		{"LXC", ports.TipoInstanciaLXC, "/api2/json/nodes/pve1/lxc/501", "UPID:pve1:1:2:3:vzdestroy:501:root@pam:"},
	}

	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete {
					t.Errorf("método = %s, se esperaba DELETE", r.Method)
				}
				if r.URL.Path != caso.path {
					t.Errorf("path = %q, se esperaba %q", r.URL.Path, caso.path)
				}
				query := r.URL.Query()
				if query.Get("purge") != "1" {
					t.Errorf("purge = %q, se esperaba 1", query.Get("purge"))
				}
				if query.Get("destroy-unreferenced-disks") != "1" {
					t.Errorf("destroy-unreferenced-disks = %q, se esperaba 1", query.Get("destroy-unreferenced-disks"))
				}
				if len(query) != 2 {
					t.Errorf("query inesperada: %v", query)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"data": caso.upid})
			}))
			t.Cleanup(server.Close)

			client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)
			upid, err := client.EliminarInstancia(context.Background(), "pve1", 501, caso.tipo)
			if err != nil {
				t.Fatal(err)
			}
			if upid != caso.upid {
				t.Errorf("UPID = %q, se esperaba %q", upid, caso.upid)
			}
		})
	}
}

func TestClient_ListarInstancias_ProxmoxCaido(t *testing.T) {
	// Servidor que responde con error 500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := proxmox.NewClient(server.URL, "token", "secret", nil)

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

	client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)
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
	return proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)
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
	client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)

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
		client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)

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

	t.Run("ip-addresses nulo usa inet", func(t *testing.T) {
		const payload = `{"data":[{"name":"eth0","hwaddr":"BC:24:11:AA:BB:CC","ip-addresses":null,"inet":"192.168.1.50/24"}]}`
		server, _ := nuevoProxmoxSimuladoInterfaces(t, http.StatusOK, payload)
		client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)

		interfaces, err := client.ObtenerInterfaces(context.Background(), "pve1", ports.TipoInstanciaLXC, 101)
		if err != nil {
			t.Fatalf("ip-addresses nulo no debe devolver error: %v", err)
		}
		if len(interfaces) != 1 || len(interfaces[0].Direcciones) != 1 {
			t.Fatalf("Se esperaba la dirección de inet: %+v", interfaces)
		}
		esperada := ports.DireccionIP{IP: "192.168.1.50", Prefijo: 24, Version: 4}
		if interfaces[0].Direcciones[0] != esperada {
			t.Errorf("Dirección CIDR incorrecta: %+v", interfaces[0].Direcciones[0])
		}
	})

	for nombre, payload := range map[string]string{
		"data nulo":    `{"data":null}`,
		"data ausente": `{}`,
		"data vacío":   `{"data":[]}`,
	} {
		t.Run(nombre, func(t *testing.T) {
			server, ruta := nuevoProxmoxSimuladoInterfaces(t, http.StatusOK, payload)
			client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)

			interfaces, err := client.ObtenerInterfaces(context.Background(), "pve1", ports.TipoInstanciaLXC, 101)
			if err != nil {
				t.Fatalf("Una respuesta sin interfaces no debe devolver error: %v", err)
			}
			if esperada := "/api2/json/nodes/pve1/lxc/101/interfaces"; *ruta != esperada {
				t.Errorf("Ruta llamada incorrecta: se esperaba %q, se obtuvo %q", esperada, *ruta)
			}
			if len(interfaces) != 0 {
				t.Errorf("Se esperaba una lista vacía, se obtuvo: %+v", interfaces)
			}
		})
	}

	t.Run("ip-addresses ausente", func(t *testing.T) {
		const payload = `{"data":[{"name":"eth0","hwaddr":"BC:24:11:AA:BB:CC"}]}`
		server, _ := nuevoProxmoxSimuladoInterfaces(t, http.StatusOK, payload)
		client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)

		interfaces, err := client.ObtenerInterfaces(context.Background(), "pve1", ports.TipoInstanciaLXC, 101)
		if err != nil {
			t.Fatalf("ip-addresses ausente no debe devolver error: %v", err)
		}
		if len(interfaces) != 1 || len(interfaces[0].Direcciones) != 0 {
			t.Fatalf("Se esperaba una interfaz sin direcciones: %+v", interfaces)
		}
	})
}

func TestClient_ObtenerInterfaces_ErrorYTimeout(t *testing.T) {
	t.Run("guest agent no corriendo", func(t *testing.T) {
		// Proxmox responde 500 con este mensaje cuando la VM está apagada o no
		// tiene el QEMU Guest Agent corriendo.
		server, _ := nuevoProxmoxSimuladoInterfaces(t, http.StatusInternalServerError,
			`{"data":null,"message":"QEMU guest agent is not running"}`)
		client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)

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

		client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)
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

func TestClient_ObtenerStorages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "PVEAPIToken="+tokenIDValido+"="+tokenSecretValido {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api2/json/nodes/pve1/storage" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"storage": "local-lvm",
					"type":    "lvmthin",
					"total":   500000000000,
					"used":    180000000000,
					"avail":   320000000000,
					"active":  1,
					"content": "images,rootdir",
				},
			},
		})
	}))
	defer server.Close()

	client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)
	storages, err := client.ObtenerStorages(context.Background(), "pve1")
	if err != nil {
		t.Fatalf("ObtenerStorages falló: %v", err)
	}

	if len(storages) != 1 || storages[0].Storage != "local-lvm" || storages[0].Active != 1 {
		t.Errorf("Resultado inesperado: %+v", storages)
	}
}

func TestClient_ObtenerEstadoNodo_ConservaStorageDisponible(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/nodes/pve1/status" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"cpu":0.25,"uptime":100,"cpuinfo":{"cpus":24,"cores":6,"sockets":2},"memory":{"total":17179869184,"used":4294967296},"rootfs":{"total":536870912000,"used":214748364800,"avail":300647710720}}}`))
	}))
	defer server.Close()

	client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)
	estado, err := client.ObtenerEstadoNodo(context.Background(), "pve1")
	if err != nil {
		t.Fatalf("ObtenerEstadoNodo falló: %v", err)
	}
	if estado.DiscoDisponible != 300647710720 {
		t.Errorf("DiscoDisponible = %d, se esperaba rootfs.avail", estado.DiscoDisponible)
	}
	if estado.CPUs != 24 || estado.PhysicalCores != 6 {
		t.Errorf("CPU lógico/físico = %d/%d, se esperaba 24/6; cpuinfo.cores ya incluye ambos sockets", estado.CPUs, estado.PhysicalCores)
	}
}

func TestClient_ObtenerContenidoStorage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "PVEAPIToken="+tokenIDValido+"="+tokenSecretValido {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api2/json/nodes/pve1/storage/local/content" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"volid":   "local:iso/ubuntu-24.04.iso",
					"content": "iso",
					"size":    2100000000,
				},
				{
					"volid":   "local:vztmpl/debian-12.tar.zst",
					"content": "vztmpl",
					"size":    130000000,
				},
			},
		})
	}))
	defer server.Close()

	client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)
	items, err := client.ObtenerContenidoStorage(context.Background(), "pve1", "local")
	if err != nil {
		t.Fatalf("ObtenerContenidoStorage falló: %v", err)
	}

	if len(items) != 2 || items[0].Volid != "local:iso/ubuntu-24.04.iso" || items[1].Content != "vztmpl" {
		t.Errorf("Contenido inesperado: %+v", items)
	}
}

func TestClient_ObtenerMetricasRRD(t *testing.T) {
	t.Run("consumo y parseo correcto de VM (qemu) con campos nulos", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "PVEAPIToken="+tokenIDValido+"="+tokenSecretValido {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Path != "/api2/json/nodes/pve1/qemu/100/rrddata" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.URL.Query().Get("timeframe") != "hour" {
				t.Errorf("timeframe esperado 'hour', recibido %q", r.URL.Query().Get("timeframe"))
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{
						"time":      1787092500,
						"cpu":       0.017948,
						"maxcpu":    1,
						"mem":       241901568,
						"maxmem":    2147483648,
						"netin":     1024.5,
						"netout":    2048.2,
						"diskread":  512.0,
						"diskwrite": 256.0,
					},
					{
						// Punto temporal con VM apagada: sin cpu, mem ni red (valores null/ausentes)
						"time":    1787092560,
						"maxcpu":  1,
						"maxmem":  2147483648,
						"maxdisk": 10737418240,
						"disk":    0,
					},
				},
			})
		}))
		defer server.Close()

		client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)
		puntos, err := client.ObtenerMetricasRRD(context.Background(), "pve1", "qemu", 100, "hour")
		if err != nil {
			t.Fatalf("ObtenerMetricasRRD falló: %v", err)
		}

		if len(puntos) != 2 {
			t.Fatalf("Se esperaban 2 puntos, se obtuvieron %d", len(puntos))
		}

		// Punto 1: con valores
		p1 := puntos[0]
		if p1.Timestamp != 1787092500 {
			t.Errorf("Timestamp inesperado: %d", p1.Timestamp)
		}
		// Proxmox informa 0.017948 (fracción): sale en porcentaje con 2 decimales.
		if p1.CPUPercent == nil || *p1.CPUPercent != 1.79 {
			t.Errorf("CPUPercent inesperado: %v", p1.CPUPercent)
		}
		if p1.RAMUsedBytes == nil || *p1.RAMUsedBytes != 241901568 || p1.RAMTotalBytes != 2147483648 {
			t.Errorf("RAM incorrecta: used=%v total=%d", p1.RAMUsedBytes, p1.RAMTotalBytes)
		}
		if p1.NetInBps != 1024 || p1.NetOutBps != 2048 {
			t.Errorf("Net Bps incorrecto: in=%d out=%d", p1.NetInBps, p1.NetOutBps)
		}
		if p1.DiskReadBps != 512 || p1.DiskWriteBps != 256 {
			t.Errorf("Disk Bps incorrecto: read=%d write=%d", p1.DiskReadBps, p1.DiskWriteBps)
		}

		// Punto 2: sin dato de CPU ni RAM (instancia apagada) quedan en null, no en 0
		p2 := puntos[1]
		if p2.Timestamp != 1787092560 {
			t.Errorf("Timestamp inesperado: %d", p2.Timestamp)
		}
		if p2.CPUPercent != nil {
			t.Errorf("CPUPercent sin dato debe ser null, recibido %v", *p2.CPUPercent)
		}
		if p2.RAMUsedBytes != nil {
			t.Errorf("RAMUsedBytes sin dato debe ser null, recibido %v", *p2.RAMUsedBytes)
		}
		if p2.RAMTotalBytes != 2147483648 {
			t.Errorf("RAMTotalBytes debe ser 2147483648, recibido %d", p2.RAMTotalBytes)
		}
		if p2.NetInBps != 0 || p2.NetOutBps != 0 {
			t.Errorf("Net Bps debe ser 0, recibido in=%d out=%d", p2.NetInBps, p2.NetOutBps)
		}
		if p2.DiskReadBps != 0 || p2.DiskWriteBps != 0 {
			t.Errorf("Disk Bps debe ser 0, recibido read=%d write=%d", p2.DiskReadBps, p2.DiskWriteBps)
		}
	})

	t.Run("consumo y parseo correcto de LXC con múltiples timeframes", func(t *testing.T) {
		timeframes := []string{"hour", "day", "week", "month", "year"}
		for _, tf := range timeframes {
			t.Run(tf, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/api2/json/nodes/pve1/lxc/201/rrddata" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					if r.URL.Query().Get("timeframe") != tf {
						t.Errorf("timeframe esperado %q, recibido %q", tf, r.URL.Query().Get("timeframe"))
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"data": []map[string]any{
							{
								"time":      1787093340,
								"cpu":       0.005,
								"maxcpu":    1,
								"mem":       21046067,
								"maxmem":    1073741824,
								"netin":     50,
								"netout":    100,
								"diskread":  0,
								"diskwrite": 12,
							},
						},
					})
				}))
				defer server.Close()

				client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)
				puntos, err := client.ObtenerMetricasRRD(context.Background(), "pve1", "lxc", 201, tf)
				if err != nil {
					t.Fatalf("ObtenerMetricasRRD falló para timeframe %s: %v", tf, err)
				}
				if len(puntos) != 1 {
					t.Fatalf("Se esperaba 1 punto, vinieron %d", len(puntos))
				}
				if puntos[0].RAMTotalBytes != 1073741824 || puntos[0].RAMUsedBytes == nil || *puntos[0].RAMUsedBytes != 21046067 {
					t.Errorf("RAM de LXC incorrecta: %+v", puntos[0])
				}
			})
		}
	})

	t.Run("acepta alias 'vm' normalizándolo a qemu", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api2/json/nodes/pve1/qemu/110/rrddata" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{},
			})
		}))
		defer server.Close()

		client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)
		_, err := client.ObtenerMetricasRRD(context.Background(), "pve1", "vm", 110, "")
		if err != nil {
			t.Fatalf("ObtenerMetricasRRD con 'vm' falló: %v", err)
		}
	})

	t.Run("rechaza tipo desconocido", func(t *testing.T) {
		client := proxmox.NewClient("http://127.0.0.1:8006", tokenIDValido, tokenSecretValido, nil)
		_, err := client.ObtenerMetricasRRD(context.Background(), "pve1", "invalido", 110, "hour")
		if err == nil {
			t.Fatal("Se esperaba error por tipo desconocido, pero fue nil")
		}
	})
}

func TestClient_ObtenerMetricasNodoRRD(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/nodes/pve1/rrddata" || r.URL.Query().Get("timeframe") != "hour" {
			t.Errorf("ruta del historial del nodo inesperada: %s", r.URL.String())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"time": 1787092500, "cpu": 0.2, "memused": 2048, "memtotal": 8192, "rootused": 3000, "roottotal": 12000},
			{"time": 1787092560, "cpu": 0.1},
		}})
	}))
	defer server.Close()

	client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)
	puntos, err := client.ObtenerMetricasNodoRRD(context.Background(), "pve1", "hour")
	if err != nil {
		t.Fatalf("ObtenerMetricasNodoRRD falló: %v", err)
	}
	if len(puntos) != 2 {
		t.Fatalf("se esperaban dos puntos: %+v", puntos)
	}
	if puntos[0].Timestamp != 1787092500 || puntos[0].CPU != 0.2 || puntos[0].MemUsada != 2048 || puntos[0].MemTotal != 8192 || puntos[0].DiscoUsado != 3000 || puntos[0].DiscoTotal != 12000 {
		t.Errorf("el histórico del nodo perdió sus recursos: %+v", puntos[0])
	}
	if puntos[1].Timestamp != 1787092560 || puntos[1].CPU != 0.1 || puntos[1].MemUsada != 0 || puntos[1].MemTotal != 0 || puntos[1].DiscoUsado != 0 || puntos[1].DiscoTotal != 0 {
		t.Errorf("una muestra sin recursos debe conservar sus campos vacíos: %+v", puntos[1])
	}
}

func TestClient_ObtenerEstadoActual(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/nodes/pve1/qemu/100/status/current" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"vmid":      100,
				"status":    "running",
				"cpus":      2,
				"cpu":       0.05,
				"mem":       536870912,
				"maxmem":    2147483648,
				"disk":      1073741824,
				"maxdisk":   10737418240,
				"netin":     1024,
				"netout":    2048,
				"diskread":  500,
				"diskwrite": 600,
				"uptime":    3600,
			},
		})
	}))
	defer server.Close()

	client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)
	m, err := client.ObtenerEstadoActual(context.Background(), "pve1", "qemu", 100)
	if err != nil {
		t.Fatalf("ObtenerEstadoActual falló: %v", err)
	}

	// Proxmox informa 0.05 (fracción): sale en porcentaje.
	if m.VMID != 100 || m.Status != "running" || m.CPU == nil || *m.CPU != 5 || m.CPUs != 2 {
		t.Errorf("Estado actual incorrecto: %+v", m)
	}
	if m.Mem == nil || *m.Mem != 536870912 || m.MaxMem != 2147483648 {
		t.Errorf("Memoria incorrecta: %+v", m)
	}
}

// Con la instancia apagada, status/current trae cpu 0 y mem 0: hacia el front
// salen en null (convención de la PM: null = apagada).
func TestObtenerEstadoActual_ApagadaDevuelveNull(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"vmid": 101, "status": "stopped", "cpus": 1, "cpu": 0, "mem": 0, "maxmem": 1073741824,
		}})
	}))
	defer server.Close()

	client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)
	m, err := client.ObtenerEstadoActual(context.Background(), "pve1", "lxc", 101)
	if err != nil {
		t.Fatal(err)
	}
	if m.CPU != nil || m.Mem != nil {
		t.Errorf("Apagada: cpu y mem deben ser null, vinieron cpu=%v mem=%v", m.CPU, m.Mem)
	}
	if m.MaxMem != 1073741824 || m.Status != "stopped" {
		t.Errorf("El resto de los datos se conserva: %+v", m)
	}
	crudo, _ := json.Marshal(m)
	if !strings.Contains(string(crudo), `"cpu":null`) || !strings.Contains(string(crudo), `"mem":null`) {
		t.Errorf("El JSON debe llevar cpu y mem en null: %s", crudo)
	}
}

// ObtenerConfig: Proxmox manda memory como string en qemu y como número en lxc
// (capturas de RF-10); con enUso pide ?current=1.
func TestObtenerConfig_QemuYLXC(t *testing.T) {
	var consultas []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		consultas = append(consultas, r.URL.Path+"?"+r.URL.RawQuery)
		data := map[string]any{"memory": "4096", "cores": 2, "digest": "abc"}
		if strings.Contains(r.URL.Path, "/lxc/") {
			data = map[string]any{"memory": 4096, "cores": 1, "digest": "def"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)

	vm, err := client.ObtenerConfig(context.Background(), "pve1", "qemu", 110, true)
	if err != nil || vm.MemoriaMB != 4096 || vm.Cores != 2 || vm.Digest != "abc" {
		t.Errorf("qemu: %+v %v", vm, err)
	}
	ct, err := client.ObtenerConfig(context.Background(), "pve1", "lxc", 201, false)
	if err != nil || ct.MemoriaMB != 4096 || ct.Cores != 1 || ct.Digest != "def" {
		t.Errorf("lxc: %+v %v", ct, err)
	}
	if len(consultas) != 2 || !strings.HasSuffix(consultas[0], "/qemu/110/config?current=1") || strings.Contains(consultas[1], "current") {
		t.Errorf("Rutas consultadas: %v", consultas)
	}
}

// ActualizarConfig manda solo lo que cambia, con el digest; si Proxmox rechaza
// el digest (config modificada en el medio) se informa como instancia ocupada.
func TestActualizarConfig_ParametrosYDigestRechazado(t *testing.T) {
	var cuerpo string
	rechazar := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		cuerpo = r.PostForm.Encode()
		if rechazar {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "message": "detected modified configuration - file changed by other user? Try again.\n"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": nil})
	}))
	defer server.Close()
	client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)

	if err := client.ActualizarConfig(context.Background(), "pve1", "qemu", 110, 0, 4096, "abc"); err != nil {
		t.Fatal(err)
	}
	if cuerpo != "digest=abc&memory=4096" {
		t.Errorf("Debe mandar solo memory y el digest (sin cores): %q", cuerpo)
	}
	rechazar = true
	if err := client.ActualizarConfig(context.Background(), "pve1", "qemu", 110, 2, 0, "viejo"); !errors.Is(err, ports.ErrInstanciaOcupada) {
		t.Errorf("Digest rechazado: se esperaba ErrInstanciaOcupada, vino %v", err)
	}
}

// Última barrera: aunque un valor con caracteres de directiva llegue al
// adaptador, se rechaza antes de cualquier llamada a Proxmox (ni nextid).
func TestClient_AprovisionarInstancia_RechazaInyeccionSinLlamarAProxmox(t *testing.T) {
	llamadas := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llamadas++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)

	valido := ports.CrearInstanciaProxmoxInput{Tipo: ports.TipoInstanciaQemu, Nodo: "pve1", Nombre: "srv", Cores: 1, MemoriaMB: 512,
		StorageID: "local-lvm", DiscoGB: 5, TemplateID: "local:iso/debian-12.iso", Bridge: "vmbr0", IPv4Tipo: "static",
		IPv4Address: "192.168.1.50", IPv4Mask: "24", IPv4Gateway: "192.168.1.1"}
	casos := map[string]func(i *ports.CrearInstanciaProxmoxInput){
		"bridge":   func(i *ports.CrearInstanciaProxmoxInput) { i.Bridge = "vmbr0,tag=20" },
		"storage":  func(i *ports.CrearInstanciaProxmoxInput) { i.StorageID = "local-lvm,iothread=1" },
		"template": func(i *ports.CrearInstanciaProxmoxInput) { i.TemplateID = "local:iso/x.iso,media=disk" },
		"nombre":   func(i *ports.CrearInstanciaProxmoxInput) { i.Nombre = "srv\nnet1: virtio" },
		"nodo":     func(i *ports.CrearInstanciaProxmoxInput) { i.Nodo = "pve1;x" },
		"ip":       func(i *ports.CrearInstanciaProxmoxInput) { i.IPv4Address = "192.168.1.50,gw=10.0.0.1" },
		"mascara":  func(i *ports.CrearInstanciaProxmoxInput) { i.IPv4Mask = "24=" },
		"gateway":  func(i *ports.CrearInstanciaProxmoxInput) { i.IPv4Gateway = "192.168.1.1\x00" },
	}
	for campo, alterar := range casos {
		input := valido
		alterar(&input)
		_, err := client.AprovisionarInstancia(context.Background(), input)
		if !errors.Is(err, ports.ErrParametroInvalido) || !errors.Is(err, ports.ErrCreacionNoDespachada) {
			t.Errorf("%s: se esperaba ErrParametroInvalido (y no despachada, para liberar la reserva), vino %v", campo, err)
		}
	}
	if llamadas != 0 {
		t.Errorf("No debe haber ninguna llamada a Proxmox: hubo %d", llamadas)
	}
}

// CrearSnapshot manda vmstate=0 solo en QEMU (LXC no lo acepta) y devuelve el UPID.
func TestClient_CrearSnapshot(t *testing.T) {
	var pedidos []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		pedidos = append(pedidos, r.Method+" "+r.URL.Path+" "+r.PostForm.Encode())
		fmt.Fprint(w, `{"data":"UPID:pve1:0001:qmsnapshot"}`)
	}))
	defer server.Close()
	client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)

	for _, tipo := range []string{"qemu", "lxc"} {
		upid, err := client.CrearSnapshot(context.Background(), "pve1", tipo, 110, "snap_1", "Previo")
		if err != nil || upid != "UPID:pve1:0001:qmsnapshot" {
			t.Fatalf("%s: %q %v", tipo, upid, err)
		}
	}
	if pedidos[0] != "POST /api2/json/nodes/pve1/qemu/110/snapshot description=Previo&snapname=snap_1&vmstate=0" ||
		pedidos[1] != "POST /api2/json/nodes/pve1/lxc/110/snapshot description=Previo&snapname=snap_1" {
		t.Errorf("Pedidos inesperados: %v", pedidos)
	}
	// Última barrera: un nombre inválido no llega a Proxmox.
	if _, err := client.CrearSnapshot(context.Background(), "pve1", "qemu", 110, "snap,vmstate=1", ""); !errors.Is(err, ports.ErrParametroInvalido) || len(pedidos) != 2 {
		t.Errorf("Nombre con coma: se esperaba ErrParametroInvalido sin pedido, vino %v (%d pedidos)", err, len(pedidos))
	}
}

// ListarSnapshots normaliza la respuesta y descarta la entrada "current".
func TestClient_ListarSnapshots(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[
			{"name":"base","snaptime":1774300000,"description":"","vmstate":0},
			{"name":"snap_pre_upgrade","snaptime":1774358400,"description":"Previo","parent":"base","vmstate":1},
			{"name":"current","description":"You are here!","running":1,"parent":"snap_pre_upgrade","digest":"abc"}]}`)
	}))
	defer server.Close()
	client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)
	lista, err := client.ListarSnapshots(context.Background(), "pve1", "qemu", 110)
	if err != nil || len(lista) != 2 {
		t.Fatalf("%+v %v", lista, err)
	}
	esperado := ports.SnapshotDTO{Nombre: "snap_pre_upgrade", SnapTime: 1774358400, Descripcion: "Previo", Padre: "base", VMState: true}
	if lista[1] != esperado || lista[0].VMState || lista[0].Padre != "" {
		t.Errorf("Normalización: %+v", lista)
	}
}

func TestClient_RevertirYEliminarSnapshot(t *testing.T) {
	var pedidos []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pedidos = append(pedidos, r.Method+" "+r.URL.Path)
		fmt.Fprint(w, `{"data":"UPID:pve1:0002:qmrollback"}`)
	}))
	defer server.Close()
	client := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil)
	if upid, err := client.RevertirSnapshot(context.Background(), "pve1", "qemu", 110, "snap_1"); err != nil || upid == "" {
		t.Fatalf("rollback: %q %v", upid, err)
	}
	if upid, err := client.EliminarSnapshot(context.Background(), "pve1", "lxc", 201, "snap_1"); err != nil || upid == "" {
		t.Fatalf("delete: %q %v", upid, err)
	}
	if len(pedidos) != 2 || pedidos[0] != "POST /api2/json/nodes/pve1/qemu/110/snapshot/snap_1/rollback" || pedidos[1] != "DELETE /api2/json/nodes/pve1/lxc/201/snapshot/snap_1" {
		t.Errorf("Pedidos inesperados: %v", pedidos)
	}
	// El nombre va en la ruta: uno que la alteraría no llega a Proxmox.
	for _, malo := range []string{"../../status/stop", "snap/x", "current"} {
		if _, err := client.EliminarSnapshot(context.Background(), "pve1", "qemu", 110, malo); !errors.Is(err, ports.ErrParametroInvalido) {
			t.Errorf("%q: se esperaba ErrParametroInvalido, vino %v", malo, err)
		}
	}
	if len(pedidos) != 2 {
		t.Errorf("Un nombre inválido no debe llegar a Proxmox: %v", pedidos)
	}
}

func TestClient_ListarNodos(t *testing.T) {
	server, ruta := nuevoProxmoxSimuladoInterfaces(t, http.StatusOK,
		`{"data":[{"node":"pve-cluster01","status":"online","type":"node"},{"node":"pve-cluster02","status":"offline","type":"node"}]}`)
	nodos, err := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil).ListarNodos(context.Background())
	if err != nil || *ruta != "/api2/json/nodes" || len(nodos) != 2 || nodos[0] != "pve-cluster01" || nodos[1] != "pve-cluster02" {
		t.Fatalf("nodos=%v ruta=%s err=%v", nodos, *ruta, err)
	}
}

func TestClient_VersionPVE(t *testing.T) {
	server, ruta := nuevoProxmoxSimuladoInterfaces(t, http.StatusOK, `{"data":{"version":"8.4.21","release":"8.4","repoid":"abc"}}`)
	version, err := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil).VersionPVE(context.Background())
	if err != nil || version != "8.4.21" || *ruta != "/api2/json/version" {
		t.Fatalf("version=%q ruta=%s err=%v", version, *ruta, err)
	}
	for cuerpo, esperada := range map[string]string{
		`{"data":{"version":"9.2.2","release":"9.2","repoid":"abc"}}`: "9.2.2",  // versión completa en "version"
		`{"data":{"version":"8.4","release":"21","repoid":"abc"}}`:    "8.4.21", // el release es el último número
		`{"data":{"version":"8.4","release":"","repoid":"abc"}}`:      "8.4",
		`{"data":{"version":"8.4","release":"8.4","repoid":"abc"}}`:   "8.4",
	} {
		server, _ = nuevoProxmoxSimuladoInterfaces(t, http.StatusOK, cuerpo)
		if version, err := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil).VersionPVE(context.Background()); err != nil || version != esperada {
			t.Errorf("%s: version=%q err=%v, se esperaba %q", cuerpo, version, err, esperada)
		}
	}
	server, _ = nuevoProxmoxSimuladoInterfaces(t, http.StatusOK, `{"data":{}}`)
	if _, err := proxmox.NewClient(server.URL, tokenIDValido, tokenSecretValido, nil).VersionPVE(context.Background()); !errors.Is(err, ports.ErrProxmoxNoDisponible) {
		t.Fatalf("Sin versión en la respuesta se esperaba ErrProxmoxNoDisponible: %v", err)
	}
}
