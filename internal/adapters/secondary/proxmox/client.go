package proxmox

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"el-centinela/internal/core/ports"
)

// ticketTTL es la vigencia real de un ticket de autenticación de Proxmox VE.
// Refrescamos un poco antes de que venza para no arriesgarnos a que una
// request en curso se quede con un ticket vencido a mitad de camino.
const (
	ticketTTL          = 2 * time.Hour
	ticketRefreshAhead = 10 * time.Minute
	requestTimeout     = 10 * time.Second
)

// Client implementa ports.ProxmoxPort hablando directo con la API REST de
// Proxmox VE, autenticado por ticket + CSRF (el mismo mecanismo que usa la
// web UI de Proxmox).
type Client struct {
	baseURL        string
	nodePorDefecto string
	username       string
	password       string
	httpClient     *http.Client

	mu         sync.Mutex
	ticket     string
	csrfToken  string
	obtenidoEn time.Time
}

// NewClient crea un cliente de Proxmox VE.
// baseURL: ej. "https://100.81.49.19:8006" (sin barra final).
// node: nodo por defecto; hoy el cluster tiene un único nodo ("pve"), pero el
// tipo/nodo real de cada instancia siempre se resuelve contra cluster/resources.
func NewClient(baseURL, node, username, password string, tlsCfg *tls.Config) *Client {
	return &Client{
		baseURL:        strings.TrimRight(baseURL, "/"),
		nodePorDefecto: node,
		username:       username,
		password:       password,
		httpClient: &http.Client{
			Timeout:   requestTimeout,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}
}

var _ ports.ProxmoxPort = (*Client)(nil)

// ==========================================
// Autenticación (ticket + CSRF)
// ==========================================

type ticketResponse struct {
	Data struct {
		Ticket              string `json:"ticket"`
		CSRFPreventionToken string `json:"CSRFPreventionToken"`
	} `json:"data"`
}

// ensureTicket obtiene un ticket nuevo si no hay uno cacheado o está por vencer.
func (c *Client) ensureTicket(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ticket != "" && time.Since(c.obtenidoEn) < ticketTTL-ticketRefreshAhead {
		return nil
	}

	form := url.Values{
		"username": {c.username},
		"password": {c.password},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api2/json/access/ticket", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("%w: %v", ports.ErrProxmoxNoDisponible, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ports.ErrProxmoxNoDisponible, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: login rechazado (HTTP %d)", ports.ErrProxmoxNoDisponible, resp.StatusCode)
	}

	var parsed ticketResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return fmt.Errorf("%w: respuesta de login inválida: %v", ports.ErrProxmoxNoDisponible, err)
	}
	if parsed.Data.Ticket == "" {
		return fmt.Errorf("%w: login sin ticket en la respuesta", ports.ErrProxmoxNoDisponible)
	}

	c.ticket = parsed.Data.Ticket
	c.csrfToken = parsed.Data.CSRFPreventionToken
	c.obtenidoEn = time.Now()
	return nil
}

// ==========================================
// Request helper
// ==========================================

// doRequest arma y ejecuta una request autenticada contra la API de Proxmox.
// body va como application/x-www-form-urlencoded (formato que espera Proxmox
// en sus endpoints de escritura); puede ser nil para GET sin parámetros.
func (c *Client) doRequest(ctx context.Context, method, path string, body url.Values) ([]byte, error) {
	if err := c.ensureTicket(ctx); err != nil {
		return nil, err
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

	c.mu.Lock()
	req.AddCookie(&http.Cookie{Name: "PVEAuthCookie", Value: c.ticket})
	if method != http.MethodGet {
		req.Header.Set("CSRFPreventionToken", c.csrfToken)
	}
	c.mu.Unlock()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ports.ErrProxmoxNoDisponible, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ports.ErrProxmoxNoDisponible, err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, ports.ErrInstanciaNoEncontrada
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP %d en %s", ports.ErrProxmoxNoDisponible, resp.StatusCode, path)
	}

	return respBody, nil
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
	}, nil
}

func (c *Client) ListarInstancias(ctx context.Context) ([]ports.InstanciaProxmoxDTO, error) {
	entries, err := c.obtenerInstancias(ctx)
	if err != nil {
		return nil, err
	}

	dtos := make([]ports.InstanciaProxmoxDTO, 0, len(entries))
	for _, entry := range entries {
		dtos = append(dtos, ports.InstanciaProxmoxDTO{
			Vmid:   entry.Vmid,
			Nombre: entry.Name,
			Tipo:   entry.Type,
			Nodo:   entry.Node,
			Estado: entry.Status,
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
