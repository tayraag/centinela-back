package proxmox

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"el-centinela/internal/core/ports"
)

const (
	requestTimeout = 10 * time.Second
	// maxDetalleError limita cuánto del cuerpo de una respuesta de error de
	// Proxmox se incluye en el error (y por lo tanto en el log).
	maxDetalleError = 200
)

// Client implementa ports.ProxmoxPort hablando directo con la API REST de
// Proxmox VE, autenticado con un API Token de servicio.
type Client struct {
	baseURL        string
	nodePorDefecto string
	tokenID        string
	tokenSecret    string
	httpClient     *http.Client
}

// NewClient crea un cliente de Proxmox VE.
// baseURL: ej. "https://10.10.20.1:8006" o "https://10.10.20.1:8006/api2/json"
// (el sufijo /api2/json se normaliza).
// node: nodo por defecto; el tipo/nodo real de cada instancia siempre se
// resuelve contra cluster/resources.
// tokenID/tokenSecret: API Token de servicio (ej. "centinela-api@pve!backend-token").
// Si falta alguno de los dos, toda operación falla con ErrProxmoxCredenciales
// sin llegar a llamar a Proxmox.
func NewClient(baseURL, node, tokenID, tokenSecret string, tlsCfg *tls.Config) *Client {
	cleanURL := strings.TrimRight(baseURL, "/")
	cleanURL = strings.TrimSuffix(cleanURL, "/api2/json")

	return &Client{
		baseURL:        cleanURL,
		nodePorDefecto: node,
		tokenID:        tokenID,
		tokenSecret:    tokenSecret,
		httpClient: &http.Client{
			Timeout:   requestTimeout,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}
}

var _ ports.ProxmoxPort = (*Client)(nil)

// TokenConfigurado indica si el cliente tiene un API Token completo.
func (c *Client) TokenConfigurado() bool {
	return c.tokenID != "" && c.tokenSecret != ""
}

// ==========================================
// Request helper
// ==========================================

// doRequest arma y ejecuta una request autenticada contra la API de Proxmox.
// body va como application/x-www-form-urlencoded (formato que espera Proxmox
// en sus endpoints de escritura); puede ser nil para GET sin parámetros.
func (c *Client) doRequest(ctx context.Context, method, path string, body url.Values) ([]byte, error) {
	if !c.TokenConfigurado() {
		return nil, fmt.Errorf("%w: %w: PROXMOX_TOKEN_ID/PROXMOX_TOKEN_SECRET sin configurar",
			ports.ErrProxmoxNoDisponible, ports.ErrProxmoxCredenciales)
	}

	var bodyReader io.Reader
	if body != nil {
		bodyReader = strings.NewReader(body.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ports.ErrProxmoxNoDisponible, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	// Con API Token Proxmox no exige CSRFPreventionToken, ni siquiera en POST.
	req.Header.Set("Authorization", fmt.Sprintf("PVEAPIToken=%s=%s", c.tokenID, c.tokenSecret))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			return nil, fmt.Errorf("%w: %w: %v", ports.ErrProxmoxNoDisponible, ports.ErrProxmoxTimeout, err)
		}
		return nil, fmt.Errorf("%w: %v", ports.ErrProxmoxNoDisponible, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ports.ErrProxmoxNoDisponible, err)
	}

	switch {
	case resp.StatusCode == http.StatusInternalServerError && esBloqueo(resp.Status, respBody):
		return nil, fmt.Errorf("%w: %s (%s)", ports.ErrInstanciaOcupada, path, mensajeProxmox(resp.Status, respBody))
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: %w: HTTP %d en %s (%s)",
			ports.ErrProxmoxNoDisponible, ports.ErrProxmoxCredenciales, resp.StatusCode, path, detalle(respBody))
	case resp.StatusCode == http.StatusNotFound:
		// Las rutas que armamos existen siempre en Proxmox (el vmid ya se
		// resolvió contra cluster/resources), así que un 404 indica una
		// PROXMOX_URL mal configurada, no una instancia inexistente.
		return nil, fmt.Errorf("%w: HTTP 404 en %s, revisar PROXMOX_URL", ports.ErrProxmoxNoDisponible, path)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%w: HTTP %d en %s (%s)",
			ports.ErrProxmoxNoDisponible, resp.StatusCode, path, detalle(respBody))
	}

	return respBody, nil
}

// patronesBloqueo son los mensajes con los que Proxmox rechaza una operación
// porque la instancia está ocupada con otra tarea:
//   - can't lock file '/var/lock/qemu-server/lock-110.conf' - got timeout
//   - VM 110 is locked (snapshot) / CT 101 is locked (backup)
var patronesBloqueo = []string{"can't lock file", "is locked"}

// esBloqueo busca los patrones de bloqueo en la línea de estado HTTP (donde
// Proxmox pone el mensaje de error) y en el cuerpo de la respuesta.
func esBloqueo(status string, body []byte) bool {
	texto := strings.ToLower(status + " " + string(body))
	for _, patron := range patronesBloqueo {
		if strings.Contains(texto, patron) {
			return true
		}
	}
	return false
}

// mensajeProxmox devuelve el mensaje de error de Proxmox para el log: el del
// cuerpo ({"message": ...}) si viene, o el de la línea de estado.
func mensajeProxmox(status string, body []byte) string {
	var parsed struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &parsed) == nil && strings.TrimSpace(parsed.Message) != "" {
		return strings.TrimSpace(parsed.Message)
	}
	return status
}

// detalle recorta el cuerpo de una respuesta de error para incluirlo en el log.
func detalle(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > maxDetalleError {
		return s[:maxDetalleError] + "..."
	}
	return s
}

// ==========================================
// cluster/resources — usado para resolver tipo (qemu|lxc) y nodo de un vmid
// ==========================================

type clusterResourceEntry struct {
	Vmid   int    `json:"vmid"`
	Type   string `json:"type"`
	Name   string `json:"name"`
	Node   string `json:"node"`
	Status string `json:"status"`

	// Telemetría disponible en cluster/resources (0 cuando la instancia está detenida)
	Cpu    float64 `json:"cpu"`    // fracción [0,1] de uso de CPU
	Mem    int64   `json:"mem"`    // RAM usada en bytes
	MaxMem int64   `json:"maxmem"` // RAM máxima asignada en bytes
	MaxCpu int     `json:"maxcpu"` // cantidad de vCPUs asignadas
}

type clusterResourcesResponse struct {
	Data []clusterResourceEntry `json:"data"`
}

// obtenerInstancias consulta cluster/resources y devuelve únicamente las
// entradas de instancias (type=qemu o type=lxc; el endpoint también devuelve
// nodos, storages y redes, que se descartan acá).
func (c *Client) obtenerInstancias(ctx context.Context) ([]clusterResourceEntry, error) {
	raw, err := c.doRequest(ctx, http.MethodGet, "/api2/json/cluster/resources", nil)
	if err != nil {
		return nil, err
	}

	var parsed clusterResourcesResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%w: respuesta de cluster/resources inválida: %v", ports.ErrProxmoxNoDisponible, err)
	}

	instancias := make([]clusterResourceEntry, 0, len(parsed.Data))
	for _, entry := range parsed.Data {
		if entry.Type != ports.TipoInstanciaQemu && entry.Type != ports.TipoInstanciaLXC {
			continue
		}
		instancias = append(instancias, entry)
	}
	return instancias, nil
}

// buscarInstancia devuelve la entrada de cluster/resources correspondiente al
// vmid dado.
func (c *Client) buscarInstancia(ctx context.Context, vmid int) (*clusterResourceEntry, error) {
	instancias, err := c.obtenerInstancias(ctx)
	if err != nil {
		return nil, err
	}
	for _, entry := range instancias {
		if entry.Vmid == vmid {
			e := entry
			return &e, nil
		}
	}
	return nil, ports.ErrInstanciaNoEncontrada
}

// ==========================================
// ports.ProxmoxPort
// ==========================================

func (c *Client) ObtenerInstancia(ctx context.Context, vmid int) (*ports.InstanciaProxmoxDTO, error) {
	entry, err := c.buscarInstancia(ctx, vmid)
	if err != nil {
		return nil, err
	}
	return &ports.InstanciaProxmoxDTO{
		Vmid:   entry.Vmid,
		Nombre: entry.Name,
		Tipo:   entry.Type,
		Nodo:   entry.Node,
		Estado: entry.Status,
		Cpu:    entry.Cpu,
		Mem:    entry.Mem,
		MaxMem: entry.MaxMem,
		MaxCpu: entry.MaxCpu,
	}, nil
}

func (c *Client) ListarInstancias(ctx context.Context) ([]ports.InstanciaProxmoxDTO, error) {
	entries, err := c.obtenerInstancias(ctx)
	if err != nil {
		return nil, err
	}

	// cluster/resources solo devuelve lo que el token puede ver: si le falta
	// VM.Audit responde 200 con la lista vacía en vez de un error.
	if len(entries) == 0 {
		log.Println("⚠️  Proxmox: cluster/resources no devolvió ninguna VM ni contenedor. " +
			"Si el cluster tiene instancias, revisar que el token tenga VM.Audit (y su propia ACL si usa separación de privilegios).")
	}

	dtos := make([]ports.InstanciaProxmoxDTO, 0, len(entries))
	for _, entry := range entries {
		dtos = append(dtos, ports.InstanciaProxmoxDTO{
			Vmid:   entry.Vmid,
			Nombre: entry.Name,
			Tipo:   entry.Type,
			Nodo:   entry.Node,
			Estado: entry.Status,
			Cpu:    entry.Cpu,
			Mem:    entry.Mem,
			MaxMem: entry.MaxMem,
			MaxCpu: entry.MaxCpu,
		})
	}
	return dtos, nil
}

func (c *Client) IniciarInstancia(ctx context.Context, vmid int) (string, error) {
	return c.cambiarEstado(ctx, vmid, "start")
}

func (c *Client) DetenerInstancia(ctx context.Context, vmid int) (string, error) {
	return c.cambiarEstado(ctx, vmid, "stop")
}

func (c *Client) ReiniciarInstancia(ctx context.Context, vmid int) (string, error) {
	return c.cambiarEstado(ctx, vmid, "reboot")
}

func (c *Client) Shutdown(ctx context.Context, node string, vmid int, vmType string) (string, error) {
	path := fmt.Sprintf("/api2/json/nodes/%s/%s/%d/status/shutdown", node, vmType, vmid)
	raw, err := c.doRequest(ctx, http.MethodPost, path, nil)
	if err != nil {
		return "", err
	}
	var res upidResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("%w: error leyendo UPID de shutdown: %v", ports.ErrProxmoxNoDisponible, err)
	}
	return res.Data, nil
}

func (c *Client) Reboot(ctx context.Context, node string, vmid int, vmType string) (string, error) {
	path := fmt.Sprintf("/api2/json/nodes/%s/%s/%d/status/reboot", node, vmType, vmid)
	raw, err := c.doRequest(ctx, http.MethodPost, path, nil)
	if err != nil {
		return "", err
	}
	var res upidResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("%w: error leyendo UPID de reboot: %v", ports.ErrProxmoxNoDisponible, err)
	}
	return res.Data, nil
}

// EliminarInstancia elimina de forma permanente una VM o contenedor de Proxmox.
// Proxmox usa DELETE /nodes/{node}/{tipo}/{vmid}.
// La instancia debe estar detenida: si está encendida Proxmox responde 500 con
// un mensaje de bloqueo y se retorna ErrInstanciaOcupada. Devuelve el UPID de
// la tarea de borrado, que se sigue como cualquier otra acción.
func (c *Client) EliminarInstancia(ctx context.Context, node string, vmid int, vmType string) (string, error) {
	tipoProxmox := vmType
	if tipoProxmox == ports.TipoInstanciaQemu {
		tipoProxmox = "qemu"
	} else if tipoProxmox == ports.TipoInstanciaLXC {
		tipoProxmox = "lxc"
	} else if tipoProxmox == "VM" {
		tipoProxmox = "qemu"
	} else if tipoProxmox == "LXC" {
		tipoProxmox = "lxc"
	}
	
	path := fmt.Sprintf("/api2/json/nodes/%s/%s/%d", url.PathEscape(node), tipoProxmox, vmid)
	raw, err := c.doRequest(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return "", err
	}
	var res upidResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("%w: respuesta de borrado inválida: %v", ports.ErrProxmoxNoDisponible, err)
	}
	return res.Data, nil
}

type upidResponse struct {
	Data string `json:"data"`
}

// cambiarEstado resuelve el tipo/nodo real del vmid y dispara la acción
// (start|stop) contra /nodes/{node}/{tipo}/{vmid}/status/{accion}.
func (c *Client) cambiarEstado(ctx context.Context, vmid int, accion string) (string, error) {
	entry, err := c.buscarInstancia(ctx, vmid)
	if err != nil {
		return "", err
	}

	path := fmt.Sprintf("/api2/json/nodes/%s/%s/%d/status/%s", entry.Node, entry.Type, vmid, accion)
	raw, err := c.doRequest(ctx, http.MethodPost, path, url.Values{})
	if err != nil {
		return "", err
	}

	var parsed upidResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("%w: respuesta de %s inválida: %v", ports.ErrProxmoxNoDisponible, accion, err)
	}
	return parsed.Data, nil
}

type estadoTareaResponse struct {
	Data struct {
		Status     string `json:"status"`     // "running" | "stopped"
		ExitStatus string `json:"exitstatus"` // presente cuando status = "stopped"
	} `json:"data"`
}

// EstadoTarea consulta GET /nodes/{node}/tasks/{upid}/status. El nodo sale del
// propio UPID (UPID:<nodo>:<pid>:...), y el UPID va completo, con el ":" final.
func (c *Client) EstadoTarea(ctx context.Context, upid string) (*ports.EstadoTareaDTO, error) {
	partes := strings.Split(upid, ":")
	if len(partes) < 3 || partes[0] != "UPID" || partes[1] == "" {
		return nil, fmt.Errorf("UPID con formato inválido: %q", upid)
	}
	path := fmt.Sprintf("/api2/json/nodes/%s/tasks/%s/status", partes[1], url.PathEscape(upid))
	raw, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var parsed estadoTareaResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%w: respuesta de estado de tarea inválida: %v", ports.ErrProxmoxNoDisponible, err)
	}
	return &ports.EstadoTareaDTO{Terminada: parsed.Data.Status == "stopped", ExitStatus: parsed.Data.ExitStatus}, nil
}

type estadoNodoResponse struct {
	Data struct {
		CPU     float64 `json:"cpu"`
		Uptime  int64   `json:"uptime"`
		CPUInfo struct {
			CPUs int `json:"cpus"`
		} `json:"cpuinfo"`
		Memory struct {
			Total int64 `json:"total"`
			Used  int64 `json:"used"`
		} `json:"memory"`
		RootFS struct {
			Total int64 `json:"total"`
			Used  int64 `json:"used"`
		} `json:"rootfs"`
	} `json:"data"`
}

// ObtenerEstadoNodo consulta GET /nodes/{node}/status: CPU, memoria, disco raíz
// y uptime del hipervisor (ver captura RF-02 en "API proxmox respuestas/").
func (c *Client) ObtenerEstadoNodo(ctx context.Context, node string) (*ports.NodeStatusDTO, error) {
	raw, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/api2/json/nodes/%s/status", url.PathEscape(node)), nil)
	if err != nil {
		return nil, err
	}
	var parsed estadoNodoResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%w: respuesta de estado del nodo inválida: %v", ports.ErrProxmoxNoDisponible, err)
	}
	d := parsed.Data
	return &ports.NodeStatusDTO{
		CPU: d.CPU, CPUs: d.CPUInfo.CPUs,
		MemTotal: d.Memory.Total, MemUsada: d.Memory.Used,
		DiscoTotal: d.RootFS.Total, DiscoUsado: d.RootFS.Used,
		UptimeSegs: d.Uptime,
	}, nil
}

// prefijoFlexible acepta el prefix como número (guest agent de qemu: 24) o como
// string (/interfaces de lxc: "24"), igual que Proxmox VE 9.2.2.
type prefijoFlexible int

func (p *prefijoFlexible) UnmarshalJSON(b []byte) error {
	texto := strings.Trim(string(b), `"`)
	if texto == "" || texto == "null" {
		*p = 0
		return nil
	}
	n, err := strconv.Atoi(texto)
	if err != nil {
		return fmt.Errorf("prefix inválido %s: %w", b, err)
	}
	*p = prefijoFlexible(n)
	return nil
}

type direccionCruda struct {
	IP      string          `json:"ip-address"`
	Tipo    string          `json:"ip-address-type"` // qemu: ipv4/ipv6 · lxc: inet/inet6
	Prefijo prefijoFlexible `json:"prefix"`
}

type interfazCruda struct {
	Nombre      string           `json:"name"`
	MACAgente   string           `json:"hardware-address"` // qemu (y lxc en PVE 8+)
	MACLXC      string           `json:"hwaddr"`           // lxc
	Direcciones []direccionCruda `json:"ip-addresses"`
	Inet        string           `json:"inet"`  // lxc: "192.168.1.101/24"
	Inet6       string           `json:"inet6"` // lxc: "fe80::.../64"
}

// ObtenerInterfaces lee las interfaces de red de una instancia y las normaliza.
func (c *Client) ObtenerInterfaces(ctx context.Context, node, tipo string, vmid int) ([]ports.InterfazRed, error) {
	var crudas []interfazCruda
	switch tipo {
	case ports.TipoInstanciaQemu:
		raw, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/network-get-interfaces", url.PathEscape(node), vmid), nil)
		if err != nil {
			return nil, err
		}
		var parsed struct {
			Data struct {
				Result []interfazCruda `json:"result"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, fmt.Errorf("%w: respuesta del guest agent inválida: %v", ports.ErrProxmoxNoDisponible, err)
		}
		crudas = parsed.Data.Result
	case ports.TipoInstanciaLXC:
		raw, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/api2/json/nodes/%s/lxc/%d/interfaces", url.PathEscape(node), vmid), nil)
		if err != nil {
			return nil, err
		}
		var parsed struct {
			Data []interfazCruda `json:"data"` // null con el contenedor apagado
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, fmt.Errorf("%w: respuesta de interfaces inválida: %v", ports.ErrProxmoxNoDisponible, err)
		}
		crudas = parsed.Data
	default:
		return nil, fmt.Errorf("tipo de instancia desconocido %q", tipo)
	}

	interfaces := make([]ports.InterfazRed, 0, len(crudas))
	for _, cr := range crudas {
		itf := ports.InterfazRed{Nombre: cr.Nombre, MAC: strings.ToLower(cr.MACAgente)}
		if itf.MAC == "" {
			itf.MAC = strings.ToLower(cr.MACLXC)
		}
		for _, d := range cr.Direcciones {
			itf.Direcciones = append(itf.Direcciones, ports.DireccionIP{IP: d.IP, Prefijo: int(d.Prefijo), Version: versionIP(d.Tipo, d.IP)})
		}
		// Proxmox viejos de lxc solo traen inet/inet6 ("ip/prefijo").
		if len(itf.Direcciones) == 0 {
			for _, cidr := range []string{cr.Inet, cr.Inet6} {
				if ip, prefijo, ok := strings.Cut(strings.TrimSpace(cidr), "/"); ok && ip != "" {
					n, _ := strconv.Atoi(prefijo)
					itf.Direcciones = append(itf.Direcciones, ports.DireccionIP{IP: ip, Prefijo: n, Version: versionIP("", ip)})
				}
			}
		}
		interfaces = append(interfaces, itf)
	}
	return interfaces, nil
}

func versionIP(tipo, ip string) int {
	switch tipo {
	case "ipv4", "inet":
		return 4
	case "ipv6", "inet6":
		return 6
	}
	if strings.Contains(ip, ":") {
		return 6
	}
	return 4
}
