package http

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// CheckOrigin permite conexiones desde cualquier origen en desarrollo
	// respetando la configuración del reverse proxy en producción.
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// MetricsWSHandler gestiona el canal WebSocket para métricas en vivo (RF-05 / RNF-06).
type MetricsWSHandler struct {
	proxmox ports.ProxmoxPort
	acceso  ports.MetricasVivoService
	nodo    string

	apagado chan struct{}
	una     sync.Once
}

// NewMetricsWSHandler construye una nueva instancia del handler WebSocket de métricas.
func NewMetricsWSHandler(proxmox ports.ProxmoxPort, acceso ports.MetricasVivoService, nodo string) *MetricsWSHandler {
	return &MetricsWSHandler{
		proxmox: proxmox,
		acceso:  acceso,
		nodo:    nodo,
		apagado: make(chan struct{}),
	}
}

// CerrarConexiones finaliza todas las goroutines de streaming al apagar el servidor.
func (h *MetricsWSHandler) CerrarConexiones() {
	h.una.Do(func() {
		close(h.apagado)
	})
}

// StreamMetrics establece el canal WebSocket y transmite métricas cada 2 segundos.
//
// @Summary      Canal WebSocket de métricas de instancia en tiempo real
// @Description  Establece un canal bidireccional vía WebSocket que emite métricas instantáneas cada 2 segundos. Requiere parámetro query `?ticket=<uuid>` emitido previamente vía POST /api/auth/ws-ticket. El servidor cierra el canal con código 1008 y el motivo como reason cuando el usuario pierde el acceso: `LOGOUT` (cerró sesión; corta todas sus conexiones WebSocket), `SESSIONS_REVOKED` (baja, suspensión o reset de credenciales), `USER_INACTIVE` (suspendido o eliminado), `INSTANCE_ACCESS_DENIED` (le quitaron el permiso sobre la instancia) o `EVENTS_UNAVAILABLE` (se perdió el canal de avisos; reconectar con un ticket nuevo). Al apagarse el servidor cierra con 1001.
// @Tags         Métricas en tiempo real
// @Param        vmid   path  int     true  "Identificador de instancia (VMID)"
// @Param        ticket query string  true  "Ticket efímero de un solo uso"
// @Success      101    "Switching Protocols a WebSocket"
// @Failure      400    {object} ErrorResponse "INVALID_VMID"
// @Failure      401    {object} ErrorResponse "UNAUTHORIZED"
// @Failure      403    {object} ErrorResponse "INSTANCE_ACCESS_DENIED | USER_INACTIVE"
// @Failure      500    {object} ErrorResponse "INTERNAL_ERROR — no se pudo comprobar el acceso"
// @Failure      503    {object} ErrorResponse "EVENTS_UNAVAILABLE — no se pudo abrir el canal de avisos de corte"
// @Router       /ws/instances/{vmid}/metrics [get]
func (h *MetricsWSHandler) StreamMetrics(c *gin.Context) {
	// 1. Validar VMID
	vmidStr := c.Param("vmid")
	vmid, err := strconv.Atoi(vmidStr)
	if err != nil || vmid <= 0 {
		SendError(c, http.StatusBadRequest, "INVALID_VMID", "El identificador de instancia debe ser un número entero positivo.")
		return
	}

	// 2. Consumir el ticket (GETDEL, un solo uso) y validar usuario y permiso
	userID, err := h.acceso.ConsumirTicket(c.Request.Context(), c.Query("ticket"), vmid)
	switch {
	case errors.Is(err, ports.ErrTicketInvalido):
		SendError(c, http.StatusUnauthorized, "UNAUTHORIZED", "Ticket inválido, vencido o ya utilizado.")
		return
	case errors.Is(err, ports.ErrCuentaSuspendida):
		SendError(c, http.StatusForbidden, "USER_INACTIVE", "Usuario suspendido o inactivo.")
		return
	case errors.Is(err, ports.ErrAccesoInstanciaDenegado):
		SendError(c, http.StatusForbidden, "INSTANCE_ACCESS_DENIED", "No tiene permisos para acceder a las métricas de esta instancia.")
		return
	case err != nil:
		log.Printf("[WS] no se pudo comprobar el acceso al WebSocket de %d: %v", vmid, err)
		SendError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo comprobar el acceso.")
		return
	}

	// 3. Determinar tipo de instancia (qemu o lxc) y nodo
	instancia, err := h.proxmox.ObtenerInstancia(c.Request.Context(), vmid)
	if err != nil {
		if errors.Is(err, ports.ErrInstanciaNoEncontrada) {
			SendError(c, http.StatusNotFound, "INSTANCE_NOT_FOUND", "La instancia no existe en el cluster.")
			return
		}
		SendError(c, http.StatusBadGateway, "PROXMOX_UNAVAILABLE", "No se pudo consultar la instancia en Proxmox.")
		return
	}

	nodo := instancia.Nodo
	if nodo == "" {
		nodo = h.nodo
	}
	tipo := instancia.Tipo

	// 4. Contexto de la conexión y vigilancia del acceso: avisos de logout y
	//    revocación del bus, y revisión periódica del usuario y su permiso.
	//    Sin vigilancia no se abre el canal (fail-closed).
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	cortes, err := h.acceso.Vigilar(ctx, userID, vmid)
	if err != nil {
		log.Printf("[WS] no se pudo vigilar el acceso de %s: %v", userID, err)
		SendError(c, http.StatusServiceUnavailable, "EVENTS_UNAVAILABLE", "No se pudo abrir el canal de métricas. Reintente.")
		return
	}

	// 5. Upgrade a WebSocket
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("[WS] Error durante handshake WebSocket: %v", err)
		return
	}
	defer ws.Close()

	// Canal para avisar cuando el cliente cierra el socket
	clienteDesconectado := make(chan struct{})

	// Loop de lectura: detecta desconexión del cliente (browser tab cerrada, ping/pong, etc.)
	go func() {
		defer close(clienteDesconectado)
		for {
			_, _, err := ws.ReadMessage()
			if err != nil {
				// El cliente cerró o la red cayó
				return
			}
		}
	}()

	// 6. Ticker de sondeo y transmisión cada 2 segundos
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Enviar primer punto de inmediato
	h.enviarMetrica(ctx, ws, nodo, tipo, vmid)

	for {
		select {
		case <-ctx.Done():
			return
		case <-h.apagado:
			// Servidor cerrando: mandar close frame ordenado
			_ = ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, "Servidor cerrando"))
			return
		case <-clienteDesconectado:
			return
		case motivo := <-cortes:
			// Perdió el acceso: cierre ordenado con el motivo (1008 policy violation)
			_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, motivo), time.Now().Add(time.Second))
			return
		case <-ticker.C:
			if err := h.enviarMetrica(ctx, ws, nodo, tipo, vmid); err != nil {
				// Error al escribir en el socket; terminar poller
				return
			}
		}
	}
}

func (h *MetricsWSHandler) enviarMetrica(ctx context.Context, ws *websocket.Conn, nodo, tipo string, vmid int) error {
	metricas, err := h.proxmox.ObtenerEstadoActual(ctx, nodo, tipo, vmid)
	if err != nil {
		// Si Proxmox falla en un intervalo, armar paquete de fallback o error
		metricas = &domain.MetricasEnVivoDTO{
			Timestamp: time.Now().Unix(),
			VMID:      vmid,
			Status:    "unknown",
		}
	}

	data, err := json.Marshal(metricas)
	if err != nil {
		return err
	}

	_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return ws.WriteMessage(websocket.TextMessage, data)
}
