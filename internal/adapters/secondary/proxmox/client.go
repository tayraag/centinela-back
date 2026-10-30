package proxmox

import (
	"bytes"
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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"el-centinela/internal/core/domain"
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
	baseURL      string
	tokenID      string
	tokenSecret  string
	httpClient   *http.Client
	aprovisionMu sync.Mutex // serializa nextid + create dentro de esta API
}

// NewClient crea un cliente de Proxmox VE.
// baseURL: ej. "https://10.10.20.1:8006" o "https://10.10.20.1:8006/api2/json"
// (el sufijo /api2/json se normaliza).
// El cliente no tiene un nodo fijo: el de cada instancia se resuelve contra
// cluster/resources y el del hipervisor (PROXMOX_NODE) lo recibe cada método.
// tokenID/tokenSecret: API Token de servicio (ej. "centinela-api@pve!backend-token").
// Si falta alguno de los dos, toda operación falla con ErrProxmoxCredenciales
// sin llegar a llamar a Proxmox.
func NewClient(baseURL, tokenID, tokenSecret string, tlsCfg *tls.Config) *Client {
	cleanURL := strings.TrimRight(baseURL, "/")
	cleanURL = strings.TrimSuffix(cleanURL, "/api2/json")

	return &Client{
		baseURL:     cleanURL,
		tokenID:     tokenID,
		tokenSecret: tokenSecret,
		httpClient: &http.Client{
			Timeout:   requestTimeout,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}
}

var _ ports.ProxmoxPort = (*Client)(nil)
var _ ports.AprovisionamientoPort = (*Client)(nil)

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

// AprovisionarInstancia reserva el siguiente VMID y dispara qmcreate/vzcreate.
// El mutex cubre ambas llamadas para que dos requests atendidos por este
// proceso no intenten crear el mismo VMID. Proxmox conserva la última defensa
// ante carreras con otros procesos mediante la unicidad global del VMID.
func (c *Client) AprovisionarInstancia(ctx context.Context, input ports.CrearInstanciaProxmoxInput) (*ports.ResultadoAprovisionamiento, error) {
	// Antes de cualquier llamada (ni siquiera nextid): nada que permita
	// inyectar directivas llega a Proxmox.
	if err := validarDirectivas(input); err != nil {
		return nil, fmt.Errorf("%w: %w", ports.ErrCreacionNoDespachada, err)
	}
	c.aprovisionMu.Lock()
	defer c.aprovisionMu.Unlock()

	raw, err := c.doRequest(ctx, http.MethodGet, "/api2/json/cluster/nextid", nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ports.ErrCreacionNoDespachada, err)
	}
	var siguiente struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &siguiente); err != nil {
		return nil, fmt.Errorf("%w: %w: respuesta de cluster/nextid inválida: %v", ports.ErrCreacionNoDespachada, ports.ErrProxmoxNoDisponible, err)
	}
	vmid, err := strconv.Atoi(siguiente.Data)
	if err != nil || vmid < 100 {
		return nil, fmt.Errorf("%w: %w: VMID inválido en cluster/nextid: %q", ports.ErrCreacionNoDespachada, ports.ErrProxmoxNoDisponible, siguiente.Data)
	}

	tipo := strings.ToLower(input.Tipo)
	if tipo != ports.TipoInstanciaQemu && tipo != ports.TipoInstanciaLXC {
		return nil, fmt.Errorf("%w: tipo de instancia desconocido %q", ports.ErrCreacionNoDespachada, input.Tipo)
	}
	params := parametrosCreacion(vmid, input)
	path := fmt.Sprintf("/api2/json/nodes/%s/%s", url.PathEscape(input.Nodo), tipo)
	raw, err = c.doRequest(ctx, http.MethodPost, path, params)
	if err != nil {
		return nil, err
	}
	var creada upidResponse
	if err := json.Unmarshal(raw, &creada); err != nil || strings.TrimSpace(creada.Data) == "" {
		return nil, fmt.Errorf("%w: respuesta de creación inválida", ports.ErrProxmoxNoDisponible)
	}
	accion := ports.AccionProxmoxCrearVM
	if tipo == ports.TipoInstanciaLXC {
		accion = ports.AccionProxmoxCrearLXC
	}
	return &ports.ResultadoAprovisionamiento{VMID: vmid, UPID: creada.Data, Accion: accion}, nil
}

// caracteresDeDirectiva son los que cambian el sentido de una directiva de
// Proxmox: la coma separa opciones ("bridge=vmbr0,tag=20" agregaría una VLAN),
// el = arma claves, el ; separa el orden de booteo y los saltos de línea y el
// NUL cortan la configuración.
const caracteresDeDirectiva = ",=;\n\r\x00"

// validarDirectivas es la última barrera del adaptador: ningún valor que se
// concatena en net0, ipconfig0, scsi0, rootfs, ide2 u ostemplate puede traer
// caracteres de directiva, aunque el handler ya lo haya validado.
func validarDirectivas(input ports.CrearInstanciaProxmoxInput) error {
	campos := []struct{ nombre, valor string }{
		{"bridge", input.Bridge}, {"storageId", input.StorageID}, {"template", input.TemplateID},
		{"nombre", input.Nombre}, {"nodo", input.Nodo},
		{"ip", input.IPv4Address}, {"mascara", input.IPv4Mask}, {"gateway", input.IPv4Gateway},
	}
	for _, c := range campos {
		if strings.ContainsAny(c.valor, caracteresDeDirectiva) {
			return fmt.Errorf("%w: el parámetro %s contiene caracteres inválidos", ports.ErrParametroInvalido, c.nombre)
		}
	}
	return nil
}

func parametrosCreacion(vmid int, input ports.CrearInstanciaProxmoxInput) url.Values {
	params := url.Values{
		"vmid":   {strconv.Itoa(vmid)},
		"memory": {strconv.Itoa(input.MemoriaMB)},
		"cores":  {strconv.Itoa(input.Cores)},
	}
	if input.Descripcion != "" {
		params.Set("description", input.Descripcion)
	}
	firewall := "0"
	if input.Firewall {
		firewall = "1"
	}

	if strings.EqualFold(input.Tipo, ports.TipoInstanciaLXC) {
		params.Set("hostname", input.Nombre)
		params.Set("unprivileged", "1")
		params.Set("features", "nesting=1")
		if input.Password != "" {
			params.Set("password", input.Password)
		}
		params.Set("ostemplate", input.TemplateID)
		params.Set("rootfs", fmt.Sprintf("%s:%d", input.StorageID, input.DiscoGB))
		net0 := fmt.Sprintf("name=eth0,bridge=%s,firewall=%s", input.Bridge, firewall)
		if input.IPv4Tipo == "static" {
			net0 += fmt.Sprintf(",ip=%s/%s,gw=%s", input.IPv4Address, input.IPv4Mask, input.IPv4Gateway)
		} else {
			net0 += ",ip=dhcp"
		}
		if input.VLAN != nil {
			net0 += ",tag=" + strconv.Itoa(*input.VLAN)
		}
		params.Set("net0", net0)
		return params
	}

	params.Set("name", input.Nombre)
	params.Set("ostype", "l26")
	params.Set("agent", "1")
	params.Set("scsi0", fmt.Sprintf("%s:%d", input.StorageID, input.DiscoGB))
	params.Set("scsihw", "virtio-scsi-pci")
	params.Set("ide2", input.TemplateID+",media=cdrom")
	params.Set("boot", "order=ide2;scsi0")
	net0 := fmt.Sprintf("virtio,bridge=%s,firewall=%s", input.Bridge, firewall)
	if input.VLAN != nil {
		net0 += ",tag=" + strconv.Itoa(*input.VLAN)
	}
	params.Set("net0", net0)
	if input.IPv4Tipo == "static" {
		params.Set("ipconfig0", fmt.Sprintf("ip=%s/%s,gw=%s", input.IPv4Address, input.IPv4Mask, input.IPv4Gateway))
	} else if input.IPv4Tipo == "dhcp" {
		params.Set("ipconfig0", "ip=dhcp")
	}
	return params
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
// El control de que la instancia esté apagada se resuelve previamente en el handler HTTP
// (respondiendo 409 INSTANCE_INVALID_STATE si sigue encendida o INSTANCE_BUSY si hay tareas activas).
// Devuelve el UPID de la tarea de borrado, que se sigue como cualquier otra acción.
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
			CPUs  int `json:"cpus"`
			Cores int `json:"cores"`
		} `json:"cpuinfo"`
		Memory struct {
			Total int64 `json:"total"`
			Used  int64 `json:"used"`
		} `json:"memory"`
		RootFS struct {
			Total int64 `json:"total"`
			Used  int64 `json:"used"`
			Avail int64 `json:"avail"`
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
		CPU: d.CPU, CPUs: d.CPUInfo.CPUs, PhysicalCores: d.CPUInfo.Cores,
		MemTotal: d.Memory.Total, MemUsada: d.Memory.Used,
		DiscoTotal: d.RootFS.Total, DiscoUsado: d.RootFS.Used, DiscoDisponible: d.RootFS.Avail,
		UptimeSegs: d.Uptime,
	}, nil
}

// ListarNodos consulta GET /nodes y devuelve el nombre de cada nodo del
// cluster. Se usa al arrancar para descubrir el nodo si falta PROXMOX_NODE.
func (c *Client) ListarNodos(ctx context.Context) ([]string, error) {
	raw, err := c.doRequest(ctx, http.MethodGet, "/api2/json/nodes", nil)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Data []struct {
			Node string `json:"node"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%w: respuesta de nodos inválida: %v", ports.ErrProxmoxNoDisponible, err)
	}
	nodos := make([]string, 0, len(parsed.Data))
	for _, n := range parsed.Data {
		if n.Node != "" {
			nodos = append(nodos, n.Node)
		}
	}
	return nodos, nil
}

// VersionPVE consulta GET /version y devuelve la versión de Proxmox VE (ej.
// "8.4.21"). Se usa al arrancar para contrastarla con PROXMOX_TARGET_PVE_VERSION.
func (c *Client) VersionPVE(ctx context.Context) (string, error) {
	raw, err := c.doRequest(ctx, http.MethodGet, "/api2/json/version", nil)
	if err != nil {
		return "", err
	}
	var parsed struct {
		Data struct {
			Version string `json:"version"`
			Release string `json:"release"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed.Data.Version == "" {
		return "", fmt.Errorf("%w: respuesta de versión inválida", ports.ErrProxmoxNoDisponible)
	}
	return versionCompleta(parsed.Data.Version, parsed.Data.Release), nil
}

// versionCompleta arma la versión de tres partes a partir de /version. Acepta
// los dos formatos: version "9.2.2" con release "9.2" (la versión ya viene
// completa) y version "8.4" con release "21" (el release es el último número).
func versionCompleta(version, release string) string {
	if strings.Count(version, ".") == 1 && release != "" && strings.Trim(release, "0123456789") == "" {
		return version + "." + release
	}
	return version
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

// ObtenerStorages consulta GET /nodes/{node}/storage y devuelve la lista de storages de Proxmox.
func (c *Client) ObtenerStorages(ctx context.Context, node string) ([]ports.StorageDTO, error) {
	raw, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/api2/json/nodes/%s/storage", url.PathEscape(node)), nil)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Data []ports.StorageDTO `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%w: respuesta de storages inválida: %v", ports.ErrProxmoxNoDisponible, err)
	}
	return parsed.Data, nil
}

// ObtenerContenidoStorage consulta GET /nodes/{node}/storage/{storage}/content.
func (c *Client) ObtenerContenidoStorage(ctx context.Context, node, storage string) ([]ports.StorageContentDTO, error) {
	raw, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/api2/json/nodes/%s/storage/%s/content", url.PathEscape(node), url.PathEscape(storage)), nil)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Data []ports.StorageContentDTO `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%w: respuesta de contenido de storage inválida: %v", ports.ErrProxmoxNoDisponible, err)
	}
	return parsed.Data, nil
}

// puntoRRDProxmox representa el formato crudo devuelto por Proxmox VE en /rrddata.
type puntoRRDProxmox struct {
	Time      int64    `json:"time"`
	CPU       *float64 `json:"cpu"`
	Mem       *float64 `json:"mem"`
	MaxMem    *float64 `json:"maxmem"`
	NetIn     *float64 `json:"netin"`
	NetOut    *float64 `json:"netout"`
	DiskRead  *float64 `json:"diskread"`
	DiskWrite *float64 `json:"diskwrite"`
	// El RRD de instancias usa disk y maxdisk; se conservan para esas métricas.
	Disk    *float64 `json:"disk"`
	MaxDisk *float64 `json:"maxdisk"`
	// El RRD del nodo usa nombres distintos de los de VM y contenedores.
	MemUsed   *float64 `json:"memused"`
	MemTotal  *float64 `json:"memtotal"`
	RootUsed  *float64 `json:"rootused"`
	RootTotal *float64 `json:"roottotal"`
}

// ObtenerMetricasNodoRRD consulta la serie histórica del host. Es la misma
// forma que la de instancia pero contra /nodes/{node}/rrddata, sin tipo ni vmid.
func (c *Client) ObtenerMetricasNodoRRD(ctx context.Context, nodo, timeframe string) ([]ports.MuestraNodoRRD, error) {
	if timeframe == "" {
		timeframe = "hour"
	}

	path := fmt.Sprintf("/api2/json/nodes/%s/rrddata?timeframe=%s",
		url.PathEscape(nodo),
		url.QueryEscape(timeframe),
	)

	raw, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Data []puntoRRDProxmox `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%w: respuesta de rrddata del nodo inválida: %v", ports.ErrProxmoxNoDisponible, err)
	}

	muestras := make([]ports.MuestraNodoRRD, 0, len(parsed.Data))
	for _, p := range parsed.Data {
		m := ports.MuestraNodoRRD{Timestamp: p.Time}
		if p.CPU != nil {
			m.CPU = *p.CPU
		}
		if p.MemUsed != nil {
			m.MemUsada = int64(*p.MemUsed)
		}
		if p.MemTotal != nil {
			m.MemTotal = int64(*p.MemTotal)
		}
		if p.RootUsed != nil {
			m.DiscoUsado = int64(*p.RootUsed)
		}
		if p.RootTotal != nil {
			m.DiscoTotal = int64(*p.RootTotal)
		}
		muestras = append(muestras, m)
	}

	return muestras, nil
}

// ObtenerMetricasRRD consulta la serie temporal histórica de métricas de una VM o contenedor.
func (c *Client) ObtenerMetricasRRD(ctx context.Context, nodo, tipo string, vmid int, timeframe string) ([]domain.PuntoMetricaDTO, error) {
	tipoPVE := strings.ToLower(strings.TrimSpace(tipo))
	if tipoPVE == "vm" {
		tipoPVE = ports.TipoInstanciaQemu
	}
	if tipoPVE != ports.TipoInstanciaQemu && tipoPVE != ports.TipoInstanciaLXC {
		return nil, fmt.Errorf("tipo de instancia desconocido %q", tipo)
	}
	if timeframe == "" {
		timeframe = "hour"
	}

	path := fmt.Sprintf("/api2/json/nodes/%s/%s/%d/rrddata?timeframe=%s",
		url.PathEscape(nodo),
		url.PathEscape(tipoPVE),
		vmid,
		url.QueryEscape(timeframe),
	)

	raw, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Data []puntoRRDProxmox `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%w: respuesta de rrddata inválida: %v", ports.ErrProxmoxNoDisponible, err)
	}

	resultado := make([]domain.PuntoMetricaDTO, len(parsed.Data))
	for i, p := range parsed.Data {
		// Sin dato de CPU o RAM (instancia apagada en ese período) queda null.
		var cpuVal *float64
		if p.CPU != nil {
			v := domain.PorcentajeCPU(*p.CPU)
			cpuVal = &v
		}

		var ramUsed *int64
		if p.Mem != nil {
			v := int64(*p.Mem)
			ramUsed = &v
		}

		var ramTotal int64
		if p.MaxMem != nil {
			ramTotal = int64(*p.MaxMem)
		}

		var netIn int64
		if p.NetIn != nil {
			netIn = int64(*p.NetIn)
		}

		var netOut int64
		if p.NetOut != nil {
			netOut = int64(*p.NetOut)
		}

		var diskRead int64
		if p.DiskRead != nil {
			diskRead = int64(*p.DiskRead)
		}

		var diskWrite int64
		if p.DiskWrite != nil {
			diskWrite = int64(*p.DiskWrite)
		}

		resultado[i] = domain.PuntoMetricaDTO{
			Timestamp:     p.Time,
			CPUPercent:    cpuVal,
			RAMUsedBytes:  ramUsed,
			RAMTotalBytes: ramTotal,
			NetInBps:      netIn,
			NetOutBps:     netOut,
			DiskReadBps:   diskRead,
			DiskWriteBps:  diskWrite,
		}
	}

	return resultado, nil
}

// estadoActualProxmox representa el cuerpo crudo de /status/current.
type estadoActualProxmox struct {
	Vmid      int      `json:"vmid"`
	Status    string   `json:"status"`
	Cpus      int      `json:"cpus"`
	CPU       *float64 `json:"cpu"`
	Mem       *int64   `json:"mem"`
	MaxMem    *int64   `json:"maxmem"`
	Disk      *int64   `json:"disk"`
	MaxDisk   *int64   `json:"maxdisk"`
	NetIn     *int64   `json:"netin"`
	NetOut    *int64   `json:"netout"`
	DiskRead  *int64   `json:"diskread"`
	DiskWrite *int64   `json:"diskwrite"`
	Uptime    *int64   `json:"uptime"`
}

// ObtenerEstadoActual consulta /nodes/{node}/{tipo}/{vmid}/status/current.
func (c *Client) ObtenerEstadoActual(ctx context.Context, nodo, tipo string, vmid int) (*domain.MetricasEnVivoDTO, error) {
	tipoPVE := strings.ToLower(strings.TrimSpace(tipo))
	if tipoPVE == "vm" {
		tipoPVE = ports.TipoInstanciaQemu
	}
	if tipoPVE != ports.TipoInstanciaQemu && tipoPVE != ports.TipoInstanciaLXC {
		return nil, fmt.Errorf("tipo de instancia desconocido %q", tipo)
	}

	path := fmt.Sprintf("/api2/json/nodes/%s/%s/%d/status/current", url.PathEscape(nodo), url.PathEscape(tipoPVE), vmid)
	raw, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Data estadoActualProxmox `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%w: respuesta de status/current inválida: %v", ports.ErrProxmoxNoDisponible, err)
	}

	d := parsed.Data
	// CPU en porcentaje y RAM usada solo con la instancia encendida; si no, null.
	var cpuVal *float64
	var memVal *int64
	if d.Status == "running" {
		if d.CPU != nil {
			v := domain.PorcentajeCPU(*d.CPU)
			cpuVal = &v
		}
		memVal = d.Mem
	}
	var maxMemVal, diskVal, maxDiskVal, netInVal, netOutVal, readVal, writeVal, upVal int64
	if d.MaxMem != nil {
		maxMemVal = *d.MaxMem
	}
	if d.Disk != nil {
		diskVal = *d.Disk
	}
	if d.MaxDisk != nil {
		maxDiskVal = *d.MaxDisk
	}
	if d.NetIn != nil {
		netInVal = *d.NetIn
	}
	if d.NetOut != nil {
		netOutVal = *d.NetOut
	}
	if d.DiskRead != nil {
		readVal = *d.DiskRead
	}
	if d.DiskWrite != nil {
		writeVal = *d.DiskWrite
	}
	if d.Uptime != nil {
		upVal = *d.Uptime
	}

	return &domain.MetricasEnVivoDTO{
		Timestamp: time.Now().Unix(),
		VMID:      vmid,
		Status:    d.Status,
		CPU:       cpuVal,
		CPUs:      d.Cpus,
		Mem:       memVal,
		MaxMem:    maxMemVal,
		Disk:      diskVal,
		MaxDisk:   maxDiskVal,
		NetIn:     netInVal,
		NetOut:    netOutVal,
		DiskRead:  readVal,
		DiskWrite: writeVal,
		Uptime:    upVal,
	}, nil
}

// ObtenerConfig consulta GET /nodes/{node}/{tipo}/{vmid}/config (con
// ?current=1 si enUso). Proxmox informa memory como string en qemu y como
// número en lxc; se aceptan las dos formas.
func (c *Client) ObtenerConfig(ctx context.Context, nodo, tipo string, vmid int, enUso bool) (*ports.ConfigInstanciaDTO, error) {
	tipoPVE, err := tipoProxmox(tipo)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/api2/json/nodes/%s/%s/%d/config", url.PathEscape(nodo), tipoPVE, vmid)
	if enUso {
		path += "?current=1"
	}
	raw, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Data struct {
			Cores  json.Number `json:"cores"`
			Memory json.Number `json:"memory"`
			Digest string      `json:"digest"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("%w: respuesta de config inválida: %v", ports.ErrProxmoxNoDisponible, err)
	}
	cores, _ := strconv.Atoi(parsed.Data.Cores.String())
	memoria, _ := strconv.Atoi(parsed.Data.Memory.String())
	if cores == 0 && tipoPVE == ports.TipoInstanciaQemu {
		cores = 1 // en qemu, sin "cores" en la config Proxmox usa 1
	}
	return &ports.ConfigInstanciaDTO{Cores: cores, MemoriaMB: memoria, Digest: parsed.Data.Digest}, nil
}

// ActualizarConfig hace PUT /nodes/{node}/{tipo}/{vmid}/config. Proxmox responde
// {"data": null}: no es una tarea, el cambio queda guardado al responder.
func (c *Client) ActualizarConfig(ctx context.Context, nodo, tipo string, vmid int, cores, memoriaMB int, digest string) error {
	tipoPVE, err := tipoProxmox(tipo)
	if err != nil {
		return err
	}
	cambios := url.Values{}
	if cores > 0 {
		cambios.Set("cores", strconv.Itoa(cores))
	}
	if memoriaMB > 0 {
		cambios.Set("memory", strconv.Itoa(memoriaMB))
	}
	if digest != "" {
		cambios.Set("digest", digest)
	}
	path := fmt.Sprintf("/api2/json/nodes/%s/%s/%d/config", url.PathEscape(nodo), tipoPVE, vmid)
	_, err = c.doRequest(ctx, http.MethodPut, path, cambios)
	if err != nil && strings.Contains(err.Error(), "detected modified configuration") {
		// El digest no coincide: alguien cambió la config (p. ej. desde la UI de
		// Proxmox) entre la lectura y la escritura. Se informa como ocupada.
		return fmt.Errorf("%w: %v", ports.ErrInstanciaOcupada, err)
	}
	return err
}

// tipoProxmox normaliza el tipo de instancia al segmento de la ruta de Proxmox.
func tipoProxmox(tipo string) (string, error) {
	t := strings.ToLower(strings.TrimSpace(tipo))
	if t == "vm" {
		t = ports.TipoInstanciaQemu
	}
	if t != ports.TipoInstanciaQemu && t != ports.TipoInstanciaLXC {
		return "", fmt.Errorf("tipo de instancia desconocido %q", tipo)
	}
	return t, nil
}

// ==========================================
// Snapshots (RF-06)
// ==========================================

// formatoSnapshot es el nombre que acepta Proxmox (pve-configid): empieza con
// letra; letras, números, _ y -; de 2 a 40 caracteres. Se repite acá como
// última barrera, aunque el servicio ya lo validó.
var formatoSnapshot = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{1,39}$`)

// CrearSnapshot hace POST /nodes/{node}/{tipo}/{vmid}/snapshot y devuelve el
// UPID. En QEMU manda vmstate=0 (sin guardar la RAM); LXC no acepta vmstate.
func (c *Client) CrearSnapshot(ctx context.Context, node, tipo string, vmid int, snapname, descripcion string) (string, error) {
	tipo, err := tipoProxmox(tipo)
	if err != nil {
		return "", err
	}
	if !formatoSnapshot.MatchString(snapname) || snapname == "current" {
		return "", fmt.Errorf("%w: nombre de snapshot %q", ports.ErrParametroInvalido, snapname)
	}
	params := url.Values{"snapname": {snapname}}
	if descripcion != "" {
		params.Set("description", descripcion)
	}
	if tipo == ports.TipoInstanciaQemu {
		params.Set("vmstate", "0")
	}
	path := fmt.Sprintf("/api2/json/nodes/%s/%s/%d/snapshot", url.PathEscape(node), tipo, vmid)
	raw, err := c.doRequest(ctx, http.MethodPost, path, params)
	if err != nil {
		return "", err
	}
	var res upidResponse
	if err := json.Unmarshal(raw, &res); err != nil || strings.TrimSpace(res.Data) == "" {
		return "", fmt.Errorf("%w: respuesta de snapshot inválida", ports.ErrProxmoxNoDisponible)
	}
	return res.Data, nil
}

// snapshotProxmox es una entrada de GET .../snapshot. vmstate llega como 0/1
// (y no llega en LXC); snaptime falta en la entrada "current".
type snapshotProxmox struct {
	Name        string          `json:"name"`
	SnapTime    int64           `json:"snaptime"`
	Description string          `json:"description"`
	Parent      string          `json:"parent"`
	VMState     json.RawMessage `json:"vmstate"`
}

// ListarSnapshots consulta GET /nodes/{node}/{tipo}/{vmid}/snapshot, sin la
// entrada sintética "current" ("You are here!"), que no es un snapshot guardado.
func (c *Client) ListarSnapshots(ctx context.Context, node, tipo string, vmid int) ([]ports.SnapshotDTO, error) {
	tipo, err := tipoProxmox(tipo)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/api2/json/nodes/%s/%s/%d/snapshot", url.PathEscape(node), tipo, vmid)
	raw, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var res struct {
		Data []snapshotProxmox `json:"data"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("%w: respuesta de snapshots inválida: %v", ports.ErrProxmoxNoDisponible, err)
	}
	snapshots := make([]ports.SnapshotDTO, 0, len(res.Data))
	for _, s := range res.Data {
		if s.Name == "current" {
			continue
		}
		vmstate := strings.Trim(string(s.VMState), `"`)
		snapshots = append(snapshots, ports.SnapshotDTO{
			Nombre: s.Name, SnapTime: s.SnapTime, Descripcion: s.Description, Padre: s.Parent,
			VMState: vmstate == "1" || vmstate == "true",
		})
	}
	return snapshots, nil
}

// RevertirSnapshot hace POST .../snapshot/{snapname}/rollback y devuelve el UPID.
func (c *Client) RevertirSnapshot(ctx context.Context, node, tipo string, vmid int, snapname string) (string, error) {
	return c.ordenSnapshot(ctx, http.MethodPost, node, tipo, vmid, snapname, "/rollback")
}

// EliminarSnapshot hace DELETE .../snapshot/{snapname} y devuelve el UPID.
func (c *Client) EliminarSnapshot(ctx context.Context, node, tipo string, vmid int, snapname string) (string, error) {
	return c.ordenSnapshot(ctx, http.MethodDelete, node, tipo, vmid, snapname, "")
}

// ordenSnapshot despacha una orden sobre un snapshot existente. El nombre va
// en la ruta: se valida antes (última barrera) para que no altere el path.
func (c *Client) ordenSnapshot(ctx context.Context, metodo, node, tipo string, vmid int, snapname, sufijo string) (string, error) {
	tipo, err := tipoProxmox(tipo)
	if err != nil {
		return "", err
	}
	if !formatoSnapshot.MatchString(snapname) || snapname == "current" {
		return "", fmt.Errorf("%w: nombre de snapshot %q", ports.ErrParametroInvalido, snapname)
	}
	path := fmt.Sprintf("/api2/json/nodes/%s/%s/%d/snapshot/%s%s", url.PathEscape(node), tipo, vmid, url.PathEscape(snapname), sufijo)
	raw, err := c.doRequest(ctx, metodo, path, nil)
	if err != nil {
		return "", err
	}
	var res upidResponse
	if err := json.Unmarshal(raw, &res); err != nil || strings.TrimSpace(res.Data) == "" {
		return "", fmt.Errorf("%w: respuesta de snapshot inválida", ports.ErrProxmoxNoDisponible)
	}
	return res.Data, nil
}
