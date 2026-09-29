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
