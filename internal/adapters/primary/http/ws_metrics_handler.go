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

	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// LimitesWS acota los recursos que puede usar el canal de métricas.
type LimitesWS struct {
	// MaxConexionesPorUsuario: WebSockets abiertos a la vez por usuario en esta
	// instancia de la API. Cada uno consulta Proxmox cada 2 s.
	MaxConexionesPorUsuario int
	// Ping: cada cuánto el servidor manda un ping (el navegador responde solo).
	Ping time.Duration
	// EsperaPong: sin noticias del cliente en este lapso, la conexión se da por
	// muerta (red cortada sin cierre) y se libera.
	EsperaPong time.Duration
	// TimeoutConsulta: tiempo máximo de cada consulta a Proxmox, menor que el
	// intervalo de envío para que un Proxmox colgado no trabe el loop.
	TimeoutConsulta time.Duration
}

// LimitesWSPorDefecto: 10 conexiones por usuario, ping cada 25 s (como el
// SSE), conexión muerta a los 60 s y consultas de hasta 1,5 s.
var LimitesWSPorDefecto = LimitesWS{MaxConexionesPorUsuario: 10, Ping: 25 * time.Second, EsperaPong: 60 * time.Second, TimeoutConsulta: 1500 * time.Millisecond}

// tamanoMaximoMensajeCliente: el cliente no necesita mandar datos (solo pongs
// y el cierre); un mensaje más grande cierra la conexión.
const tamanoMaximoMensajeCliente = 512

// MetricsWSHandler gestiona el canal WebSocket para métricas en vivo (RF-05 / RNF-06).
type MetricsWSHandler struct {
	proxmox  ports.ProxmoxPort
	acceso   ports.MetricasVivoService
	nodo     string
	origenes map[string]struct{}
	limites  LimitesWS
	upgrader websocket.Upgrader

	muConexiones sync.Mutex
	conexiones   map[uuid.UUID]int // WebSockets abiertos por usuario

	apagado chan struct{}
	una     sync.Once
}

// NewMetricsWSHandler construye el handler. origenes son los Origin de
// navegador aceptados (los mismos de ALLOWED_ORIGINS que usa el CORS).
func NewMetricsWSHandler(proxmox ports.ProxmoxPort, acceso ports.MetricasVivoService, nodo string, origenes map[string]struct{}, limites LimitesWS) *MetricsWSHandler {
	return &MetricsWSHandler{
		proxmox:  proxmox,
		acceso:   acceso,
		nodo:     nodo,
		origenes: origenes,
		limites:  limites,
		// El origen ya se validó antes de consumir el ticket (origenPermitido).
		upgrader:   websocket.Upgrader{ReadBufferSize: 1024, WriteBufferSize: 1024, CheckOrigin: func(*http.Request) bool { return true }},
		conexiones: map[uuid.UUID]int{},
		apagado:    make(chan struct{}),
	}
}

// origenPermitido evita el Cross-Site WebSocket Hijacking: el navegador manda
// siempre el header Origin, y solo se aceptan los del front. Un cliente que no
// es un navegador (curl, tests) no lo manda y se acepta: igual necesita un ticket.
func (h *MetricsWSHandler) origenPermitido(r *http.Request) bool {
	origen := r.Header.Get("Origin")
	if origen == "" {
		return true
	}
	_, ok := h.origenes[origen]
	return ok
}

// reservarConexion cuenta la conexión del usuario; false si ya llegó al máximo.
func (h *MetricsWSHandler) reservarConexion(usuarioID uuid.UUID) bool {
	h.muConexiones.Lock()
	defer h.muConexiones.Unlock()
	if h.conexiones[usuarioID] >= h.limites.MaxConexionesPorUsuario {
		return false
	}
	h.conexiones[usuarioID]++
	return true
}

func (h *MetricsWSHandler) liberarConexion(usuarioID uuid.UUID) {
	h.muConexiones.Lock()
	defer h.muConexiones.Unlock()
	if h.conexiones[usuarioID]--; h.conexiones[usuarioID] <= 0 {
		delete(h.conexiones, usuarioID)
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
// @Description  Establece un canal bidireccional vía WebSocket que emite métricas instantáneas cada 2 segundos. Requiere parámetro query `?ticket=<uuid>` emitido previamente vía POST /api/auth/ws-ticket. El ticket identifica al usuario y a la sesión que lo emitió. El servidor cierra el canal con código 1008 y el motivo como reason cuando se pierde el acceso: `LOGOUT` (se cerró esa sesión), `SESSIONS_REVOKED` (se revocó esa sesión o todas las del usuario), `USER_INACTIVE` (suspendido o eliminado), `INSTANCE_ACCESS_DENIED` (le quitaron el permiso sobre la instancia) o `EVENTS_UNAVAILABLE` (se perdió el canal de avisos; reconectar con un ticket nuevo). Al apagarse el servidor cierra con 1001; si la instancia deja de existir, con 1000 e `INSTANCE_NOT_FOUND`. Cada mensaje es un JSON de métricas: `cpu` en porcentaje (0 a 100) y `cpu` y `mem` en null si la instancia no está encendida; si Proxmox falla en un intervalo, el mensaje no trae métricas sino `{"timestamp","vmid","status":"unavailable","error":{"errorCode":"PROXMOX_UNAVAILABLE|PROXMOX_TIMEOUT","message"}}` y la conexión sigue. Solo acepta el Origin del front (ALLOWED_ORIGINS), hasta 10 conexiones simultáneas por usuario y mensajes del cliente de hasta 512 bytes; el servidor manda un ping cada 25 s y libera la conexión si no tiene respuesta en 60 s.
// @Tags         Métricas en tiempo real
// @Param        vmid   path  int     true  "Identificador de instancia (VMID)"
// @Param        ticket query string  true  "Ticket efímero de un solo uso"
// @Success      101    "Switching Protocols a WebSocket"
// @Failure      400    {object} ErrorResponse "INVALID_VMID"
// @Failure      401    {object} ErrorResponse "UNAUTHORIZED"
// @Failure      403    {object} ErrorResponse "ORIGIN_NOT_ALLOWED — el Origin no es el del front | INSTANCE_ACCESS_DENIED | USER_INACTIVE"
// @Failure      404    {object} ErrorResponse "INSTANCE_NOT_FOUND"
// @Failure      429    {object} ErrorResponse "TOO_MANY_CONNECTIONS — el usuario ya tiene 10 WebSockets abiertos"
// @Failure      500    {object} ErrorResponse "INTERNAL_ERROR — no se pudo comprobar el acceso"
// @Failure      502    {object} ErrorResponse "PROXMOX_UNAVAILABLE"
// @Failure      503    {object} ErrorResponse "EVENTS_UNAVAILABLE — no se pudo abrir el canal de avisos de corte"
// @Failure      504    {object} ErrorResponse "PROXMOX_TIMEOUT"
// @Router       /ws/instances/{vmid}/metrics [get]
func (h *MetricsWSHandler) StreamMetrics(c *gin.Context) {
	// 1. Validar VMID
	vmidStr := c.Param("vmid")
	vmid, err := strconv.Atoi(vmidStr)
	if err != nil || vmid <= 0 {
		SendError(c, http.StatusBadRequest, "INVALID_VMID", "El identificador de instancia debe ser un número entero positivo.")
		return
	}

	// 2. Origen: se valida antes de consumir el ticket, así un intento desde
	//    otro sitio no gasta el ticket del usuario.
	if !h.origenPermitido(c.Request) {
		log.Printf("[WS] conexión rechazada por origen no permitido: %q", c.GetHeader("Origin"))
		SendError(c, http.StatusForbidden, "ORIGIN_NOT_ALLOWED", "Origen no permitido.")
		return
	}

	// 3. Consumir el ticket (GETDEL, un solo uso) y validar usuario y permiso
	identidad, err := h.acceso.ConsumirTicket(c.Request.Context(), c.Query("ticket"), vmid)
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
	userID := identidad.UsuarioID

	// 4. Tope de conexiones simultáneas del usuario
	if !h.reservarConexion(userID) {
		SendError(c, http.StatusTooManyRequests, "TOO_MANY_CONNECTIONS", "Demasiadas conexiones de métricas abiertas. Cierre alguna e intente de nuevo.")
		return
	}
	defer h.liberarConexion(userID)

	// 5. Determinar tipo de instancia (qemu o lxc) y nodo
	instancia, err := h.proxmox.ObtenerInstancia(c.Request.Context(), vmid)
	if err != nil {
		mapearErrorProxmox(c, err)
		return
	}

	nodo := instancia.Nodo
	if nodo == "" {
		nodo = h.nodo
	}
	tipo := instancia.Tipo

	// 6. Contexto de la conexión y vigilancia del acceso: avisos de logout y
	//    revocación del bus, y revisión periódica del usuario y su permiso.
	//    Sin vigilancia no se abre el canal (fail-closed).
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	cortes, err := h.acceso.Vigilar(ctx, identidad, vmid)
	if err != nil {
		log.Printf("[WS] no se pudo vigilar el acceso de %s: %v", userID, err)
		SendError(c, http.StatusServiceUnavailable, "EVENTS_UNAVAILABLE", "No se pudo abrir el canal de métricas. Reintente.")
		return
	}

	// 7. Upgrade a WebSocket
	ws, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("[WS] Error durante handshake WebSocket: %v", err)
		return
	}
	defer ws.Close()

	// Canal para avisar cuando el cliente cierra el socket
	clienteDesconectado := make(chan struct{})

	// Loop de lectura: detecta el cierre del cliente (pestaña cerrada) y, con
	// el plazo de lectura que renueva cada pong, una red cortada sin cierre.
	ws.SetReadLimit(tamanoMaximoMensajeCliente)
	_ = ws.SetReadDeadline(time.Now().Add(h.limites.EsperaPong))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(h.limites.EsperaPong))
	})
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

	// 8. Ticker de sondeo y transmisión cada 2 segundos, y ping periódico
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	ping := time.NewTicker(h.limites.Ping)
	defer ping.Stop()

	// Enviar primer punto de inmediato
	if h.enviarMetrica(ctx, ws, nodo, tipo, vmid) != nil {
		return
	}

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
		case <-ping.C:
			if err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return
			}
		case <-ticker.C:
			if err := h.enviarMetrica(ctx, ws, nodo, tipo, vmid); err != nil {
				// No se pudo escribir en el socket o la instancia ya no existe
				return
			}
		}
	}
}

// errorMetricasWS es el mensaje que reemplaza a las métricas cuando Proxmox
// falla en un intervalo: sin valores en cero que el front pueda graficar como reales.
type errorMetricasWS struct {
	Timestamp int64          `json:"timestamp"`
	VMID      int            `json:"vmid"`
	Status    string         `json:"status"` // "unavailable"
	Error     detalleErrorWS `json:"error"`
}

type detalleErrorWS struct {
	ErrorCode string `json:"errorCode"`
	Message   string `json:"message"`
}

// errInstanciaEliminada corta el loop cuando la instancia deja de existir.
var errInstanciaEliminada = errors.New("la instancia ya no existe")

// enviarMetrica consulta Proxmox (con su propio plazo) y manda un mensaje: las
// métricas, o un error explícito si Proxmox falló. Devuelve error si no se pudo
// escribir en el socket o si la instancia ya no existe (después de cerrar).
func (h *MetricsWSHandler) enviarMetrica(ctx context.Context, ws *websocket.Conn, nodo, tipo string, vmid int) error {
	ctxConsulta, cancel := context.WithTimeout(ctx, h.limites.TimeoutConsulta)
	metricas, err := h.proxmox.ObtenerEstadoActual(ctxConsulta, nodo, tipo, vmid)
	cancel()

	var mensaje any = metricas
	if err != nil {
		if errors.Is(err, ports.ErrInstanciaNoEncontrada) {
			_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "INSTANCE_NOT_FOUND"), time.Now().Add(time.Second))
			return errInstanciaEliminada
		}
		detalle := detalleErrorWS{ErrorCode: "PROXMOX_UNAVAILABLE", Message: "No se pudieron obtener las métricas de Proxmox en este intervalo."}
		if errors.Is(err, ports.ErrProxmoxTimeout) || errors.Is(err, context.DeadlineExceeded) {
			detalle = detalleErrorWS{ErrorCode: "PROXMOX_TIMEOUT", Message: "Proxmox no respondió a tiempo en este intervalo."}
		}
		log.Printf("[WS] métricas de %d no disponibles: %v", vmid, err)
		mensaje = errorMetricasWS{Timestamp: time.Now().Unix(), VMID: vmid, Status: "unavailable", Error: detalle}
	}

	data, err := json.Marshal(mensaje)
	if err != nil {
		return err
	}
	_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return ws.WriteMessage(websocket.TextMessage, data)
}
