package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"

	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

// ==========================================
// Tipos de respuesta
// ==========================================

// listarInstanciasResponse fue eliminado ya que el contrato original exige
// devolver []InstanciaListadaDTO directamente para no romper el front.

// ==========================================
// Handler
// ==========================================

// InstanceHandler maneja los endpoints HTTP de instancias Proxmox.
type InstanceHandler struct {
	proxmox           ports.ProxmoxPort
	aprovisionamiento ports.AprovisionamientoPort
	nodoPorDefecto    string
	userRepo          ports.UserRepository
	seguimiento       ports.SeguimientoTareas
	inventario        ports.InventarioService // resuelve las IPs del listado
	tareas            ports.TareaRepository
	audit             ports.AuditService
}

type crearInstanciaCPUWizardRequest struct {
	Cores int `json:"cores" example:"2"`
}

type crearInstanciaRAMWizardRequest struct {
	MemoryMB int `json:"memoryMb" example:"4096"`
}

type crearInstanciaDiscoWizardRequest struct {
	StorageID        string `json:"storageId" example:"local-lvm"`
	SizeGB           int    `json:"sizeGb" example:"50"`
	Format           string `json:"format,omitempty" example:"raw"`
	ThinProvisioning bool   `json:"thinProvisioning,omitempty" example:"true"`
}

type crearInstanciaIPv4WizardRequest struct {
	Type    string `json:"type" example:"dhcp"`
	Address string `json:"address,omitempty" example:"192.168.1.50"`
	Mask    string `json:"mask,omitempty" example:"24"`
	Gateway string `json:"gateway,omitempty" example:"192.168.1.1"`
}

type crearInstanciaRedWizardRequest struct {
	Bridge   string                          `json:"bridge" example:"vmbr0"`
	VLAN     *int                            `json:"vlan,omitempty" example:"100"`
	Firewall bool                            `json:"firewall" example:"true"`
	IPv4     crearInstanciaIPv4WizardRequest `json:"ipv4"`
}

type crearInstanciaOSWizardRequest struct {
	TemplateID string `json:"templateId" example:"local:iso/debian-12.iso"`
}

// CrearInstanciaRedRequest representa la red del contrato RF-07. Para una IP
// estática se acepta ip con CIDR (192.168.1.50/24) o mascara por separado; si
// se omite la máscara se usa /24. Bridge mantiene vmbr0 como valor por defecto.
type CrearInstanciaRedRequest struct {
	Modo     string `json:"modo" binding:"required,oneof=dhcp static DHCP STATIC" example:"dhcp"`
	IP       string `json:"ip,omitempty" example:"192.168.1.50/24"`
	Gateway  string `json:"gateway,omitempty" example:"192.168.1.1"`
	Mascara  string `json:"mascara,omitempty" example:"24"`
	Bridge   string `json:"bridge,omitempty" example:"vmbr0"`
	VLAN     *int   `json:"vlan,omitempty" example:"100"`
	Firewall bool   `json:"firewall,omitempty" example:"true"`
}

// CrearInstanciaRequest es el contrato unificado RF-07. nodo, password y los
// ajustes avanzados de red son opcionales; el backend usa el nodo configurado
// y vmbr0 cuando no se informan.
type CrearInstanciaRequest struct {
	Tipo        string                   `json:"tipo" binding:"required" example:"vm"`
	Nodo        string                   `json:"nodo,omitempty" example:"pve1"`
	Nombre      string                   `json:"nombre" binding:"required" example:"srv-prod-01"`
	Descripcion string                   `json:"descripcion,omitempty" example:"Servidor web principal"`
	Password    string                   `json:"password,omitempty"`
	Cores       int                      `json:"cores" binding:"required,min=1" example:"2"`
	RAMMB       int                      `json:"ramMb" binding:"required,min=16" example:"2048"`
	DiscoGB     int                      `json:"discoGb" binding:"required,min=1" example:"20"`
	StorageID   string                   `json:"storageId" binding:"required" example:"local-lvm"`
	TemplateOS  string                   `json:"templateOso" binding:"required" example:"local:iso/debian-12.iso"`
	Red         CrearInstanciaRedRequest `json:"red" binding:"required"`
}

// crearInstanciaWizardPayload conserva temporalmente el contrato anidado que
// ya consume el frontend. No se expone en Swagger como contrato nuevo.
type crearInstanciaWizardPayload struct {
	Type        string                           `json:"type"`
	Node        string                           `json:"node"`
	Name        string                           `json:"name"`
	Description string                           `json:"description,omitempty"`
	Password    string                           `json:"password,omitempty"`
	CPU         crearInstanciaCPUWizardRequest   `json:"cpu"`
	RAM         crearInstanciaRAMWizardRequest   `json:"ram"`
	Disk        crearInstanciaDiscoWizardRequest `json:"disk"`
	Network     crearInstanciaRedWizardRequest   `json:"network"`
	OS          crearInstanciaOSWizardRequest    `json:"os"`
}

// NewInstanceHandler crea un nuevo InstanceHandler.
// Recibe el cliente de Proxmox, el repositorio de usuarios (RBAC del listado),
// el seguimiento de tareas (publica TASK_FINISHED) y el servicio de auditoría.
func NewInstanceHandler(
	proxmox ports.ProxmoxPort,
	userRepo ports.UserRepository,
	seguimiento ports.SeguimientoTareas,
	tareas ports.TareaRepository,
	audit ports.AuditService,
	inventario ports.InventarioService,
	aprovisionamiento ...ports.AprovisionamientoPort,
) *InstanceHandler {
	return NewInstanceHandlerConNodo(proxmox, userRepo, seguimiento, tareas, audit, inventario, "proxmox", aprovisionamiento...)
}

// NewInstanceHandlerConNodo permite que el contrato RF-07 omita nodo sin
// acoplar el handler a variables de entorno. main inyecta PROXMOX_NODE.
func NewInstanceHandlerConNodo(
	proxmox ports.ProxmoxPort,
	userRepo ports.UserRepository,
	seguimiento ports.SeguimientoTareas,
	tareas ports.TareaRepository,
	audit ports.AuditService,
	inventario ports.InventarioService,
	nodoPorDefecto string,
	aprovisionamiento ...ports.AprovisionamientoPort,
) *InstanceHandler {
	var provisionador ports.AprovisionamientoPort
	if len(aprovisionamiento) > 0 {
		provisionador = aprovisionamiento[0]
	} else {
		provisionador, _ = proxmox.(ports.AprovisionamientoPort)
	}
	return &InstanceHandler{
		inventario:        inventario,
		proxmox:           proxmox,
		aprovisionamiento: provisionador,
		nodoPorDefecto:    strings.TrimSpace(nodoPorDefecto),
		userRepo:          userRepo,
		seguimiento:       seguimiento,
		tareas:            tareas,
		audit:             audit,
	}
}

// ==========================================
// Helpers privados
// ==========================================

// registrarAuditVM registra una acción sobre una instancia en la tabla auditoria.
// Los errores se descartan silenciosamente para no interrumpir la operación principal.
func (h *InstanceHandler) registrarAuditVM(c *gin.Context, vmid int, instanciaNombre, accion, resultado string, detalles map[string]any) {
	h.audit.Registrar(c.Request.Context(), ports.RegistrarAuditoriaInput{
		UsuarioID:       extraerUserID(c),
		Accion:          accion,
		InstanciaID:     strconv.Itoa(vmid),
		InstanciaNombre: instanciaNombre,
		Resultado:       resultado,
		Detalles:        detalles,
	})
}

// responderTarea registra la tarea para seguirla y responde 202 con el UPID y
// el tareaId (el mismo que llega en el evento TASK_FINISHED). Si no se puede
// registrar, la acción ya se disparó en Proxmox igual: se responde sin tareaId.
func (h *InstanceHandler) responderTarea(c *gin.Context, vmid int, accion, upid string, metadatos ...map[string]any) {
	respuesta := gin.H{"upid": upid}
	tareaID, err := h.seguimiento.Seguir(c.Request.Context(), extraerUserID(c), vmid, accion, upid, metadatos...)
	if err != nil {
		log.Printf("[TAREAS] no se pudo registrar la tarea %s de %d: %v", accion, vmid, err)
	} else {
		respuesta["tareaId"] = tareaID.String()
	}
	c.JSON(http.StatusAccepted, respuesta)
}

// extraerVmid parsea el parámetro de ruta :vmid. El guard RequireInstanceAccess
// ya lo valida antes de llegar acá, pero se vuelve a parsear porque no queda
// guardado en el contexto de gin.
func extraerVmid(c *gin.Context) (int, bool) {
	vmid, err := strconv.Atoi(c.Param("vmid"))
	if err != nil {
		SendError(c, http.StatusBadRequest, "INVALID_VMID", "El identificador de instancia debe ser un número entero válido.")
		return 0, false
	}
	return vmid, true
}

// estadosRequeridosPorAccion es la matriz de estados operativos: el estado en
// el que debe estar la instancia para que la acción tenga sentido.
var estadosRequeridosPorAccion = map[string]string{
	"start":    "stopped",
	"stop":     "running",
	"shutdown": "running",
	"reboot":   "running",
}

// tieneTareaActiva consulta si el vmid posee alguna tarea en curso (RUNNING)
// registrada en tareas_asincronas.
func (h *InstanceHandler) tieneTareaActiva(c *gin.Context, vmid int) bool {
	if h.tareas == nil {
		return false
	}
	activas, err := h.tareas.BuscarTareasActivasPorVmids(c.Request.Context(), []int{vmid})
	if err != nil {
		log.Printf("⚠️  [INSTANCES] error al consultar tareas activas para vmid %d: %v", vmid, err)
		return false
	}
	_, activa := activas[vmid]
	return activa
}

// validarEstadoParaAccion verifica primero si la instancia posee alguna tarea
// activa (RUNNING) en tareas_asincronas para responder 409 INSTANCE_BUSY
// inmediatamente, sin consultar Proxmox. Si no hay tareas activas, consulta el
// estado actual en Proxmox (lectura) y aborta con 409 INSTANCE_INVALID_STATE
// si es incompatible con la acción, sin emitir ninguna orden de escritura a Proxmox.
// Devuelve la instancia consultada.
func (h *InstanceHandler) validarEstadoParaAccion(c *gin.Context, vmid int, accion string) (*ports.InstanciaProxmoxDTO, bool) {
	if h.tieneTareaActiva(c, vmid) {
		SendError(c, http.StatusConflict, "INSTANCE_BUSY",
			"La instancia se encuentra ejecutando otra tarea. Aguarde a que finalice.")
		return nil, false
	}

	instancia, err := h.proxmox.ObtenerInstancia(c.Request.Context(), vmid)
	if err != nil {
		mapearErrorProxmox(c, err)
		return nil, false
	}
	if instancia.Estado != estadosRequeridosPorAccion[accion] {
		SendError(c, http.StatusConflict, "INSTANCE_INVALID_STATE",
			"La instancia se encuentra en un estado incompatible para la acción solicitada")
		return nil, false
	}
	return instancia, true
}

// mapearErrorProxmox traduce los errores de ports.ProxmoxPort al contrato HTTP.
// El detalle del error nunca llega al cliente, pero se registra en el log para
// poder diagnosticar la causa real (token rechazado, red, URL mal configurada).
func mapearErrorProxmox(c *gin.Context, err error) {
	ruta := c.Request.Method + " " + c.Request.URL.Path
	switch {
	case errors.Is(err, ports.ErrInstanciaNoEncontrada):
		SendError(c, http.StatusNotFound, "INSTANCE_NOT_FOUND", "La instancia no existe en Proxmox.")
	case errors.Is(err, ports.ErrInstanciaOcupada):
		log.Printf("⏳ Instancia ocupada [%s]: %v", ruta, err)
		SendError(c, http.StatusConflict, "INSTANCE_BUSY", "La instancia se encuentra ejecutando otra tarea. Aguarde a que finalice.")
	case errors.Is(err, ports.ErrRecursosInsuficientes):
		log.Printf("📊 Cuota insuficiente [%s]: %v", ruta, err)
		SendError(c, http.StatusConflict, "RESOURCE_QUOTA_EXCEEDED", "Los recursos solicitados superan la capacidad disponible en el nodo físico o storage seleccionado.")
	case errors.Is(err, ports.ErrCoordinacionAprovisionamiento):
		log.Printf("🔒 No se pudo coordinar la reserva [%s]: %v", ruta, err)
		SendError(c, http.StatusServiceUnavailable, "PROVISIONING_COORDINATION_UNAVAILABLE", "No se pudo reservar la capacidad de forma segura. Intente nuevamente.")
	case errors.Is(err, ports.ErrProxmoxTimeout):
		log.Printf("⏱️  Proxmox no respondió a tiempo [%s]: %v", ruta, err)
		// Distinto de PROXMOX_UNAVAILABLE: la orden pudo haber llegado y aplicarse,
		// así que el front no debe sugerir reintentar sin verificar el estado.
		SendError(c, http.StatusGatewayTimeout, "PROXMOX_TIMEOUT", "Proxmox no respondió a tiempo; la acción puede haberse aplicado")
	case errors.Is(err, ports.ErrProxmoxCredenciales):
		log.Printf("🔑 Proxmox rechazó el API Token [%s]: %v", ruta, err)
		SendError(c, http.StatusBadGateway, "PROXMOX_UNAVAILABLE", "Error al consultar la infraestructura subyacente.")
	case errors.Is(err, ports.ErrProxmoxNoDisponible):
		log.Printf("⚠️  Proxmox no disponible [%s]: %v", ruta, err)
		SendError(c, http.StatusBadGateway, "PROXMOX_UNAVAILABLE", "Error al consultar la infraestructura subyacente.")
	default:
		log.Printf("❌ Error inesperado de Proxmox [%s]: %v", ruta, err)
		SendError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Error inesperado al comunicarse con Proxmox VE.")
	}
}

func normalizarCreacionPlana(req CrearInstanciaRequest, nodoPorDefecto string) (ports.CrearInstanciaProxmoxInput, error) {
	tipo, err := normalizarTipoInstancia(req.Tipo)
	if err != nil {
		return ports.CrearInstanciaProxmoxInput{}, err
	}
	nodo := strings.TrimSpace(req.Nodo)
	if nodo == "" {
		nodo = strings.TrimSpace(nodoPorDefecto)
	}
	bridge := strings.TrimSpace(req.Red.Bridge)
	if bridge == "" {
		bridge = "vmbr0"
	}
	address, mask := separarIPv4YMascara(req.Red.IP, req.Red.Mascara)
	templateID := strings.TrimSpace(req.TemplateOS)
	if templateID != "" && !strings.Contains(templateID, ":") {
		carpeta := "iso"
		if tipo == ports.TipoInstanciaLXC {
			carpeta = "vztmpl"
		}
		templateID = fmt.Sprintf("%s:%s/%s", strings.TrimSpace(req.StorageID), carpeta, templateID)
	}
	return validarCreacionNormalizada(ports.CrearInstanciaProxmoxInput{
		Tipo: tipo, Nodo: nodo, Nombre: req.Nombre, Descripcion: req.Descripcion, Password: req.Password,
		Cores: req.Cores, MemoriaMB: req.RAMMB, StorageID: req.StorageID, DiscoGB: req.DiscoGB,
		TemplateID: templateID, Bridge: bridge, VLAN: req.Red.VLAN, Firewall: req.Red.Firewall,
		IPv4Tipo: req.Red.Modo, IPv4Address: address, IPv4Mask: mask, IPv4Gateway: req.Red.Gateway,
	}, false)
}

func normalizarCreacionWizard(req crearInstanciaWizardPayload) (ports.CrearInstanciaProxmoxInput, error) {
	tipo, err := normalizarTipoInstancia(req.Type)
	if err != nil {
		return ports.CrearInstanciaProxmoxInput{}, err
	}
	return validarCreacionNormalizada(ports.CrearInstanciaProxmoxInput{
		Tipo: tipo, Nodo: req.Node, Nombre: req.Name, Descripcion: req.Description, Password: req.Password,
		Cores: req.CPU.Cores, MemoriaMB: req.RAM.MemoryMB, StorageID: req.Disk.StorageID, DiscoGB: req.Disk.SizeGB,
		TemplateID: req.OS.TemplateID, Bridge: req.Network.Bridge, VLAN: req.Network.VLAN, Firewall: req.Network.Firewall,
		IPv4Tipo: req.Network.IPv4.Type, IPv4Address: req.Network.IPv4.Address,
		IPv4Mask: req.Network.IPv4.Mask, IPv4Gateway: req.Network.IPv4.Gateway,
	}, true)
}

func normalizarTipoInstancia(tipo string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(tipo)) {
	case "VM":
		return ports.TipoInstanciaQemu, nil
	case "LXC":
		return ports.TipoInstanciaLXC, nil
	default:
		return "", fmt.Errorf("tipo debe ser VM o LXC")
	}
}

func validarCreacionNormalizada(input ports.CrearInstanciaProxmoxInput, exigirPasswordLXC bool) (ports.CrearInstanciaProxmoxInput, error) {
	input.Nodo = strings.TrimSpace(input.Nodo)
	input.Nombre = strings.TrimSpace(input.Nombre)
	input.Descripcion = strings.TrimSpace(input.Descripcion)
	input.StorageID = strings.TrimSpace(input.StorageID)
	input.TemplateID = strings.TrimSpace(input.TemplateID)
	input.Bridge = strings.TrimSpace(input.Bridge)
	input.IPv4Tipo = strings.ToLower(strings.TrimSpace(input.IPv4Tipo))
	input.IPv4Address = strings.TrimSpace(input.IPv4Address)
	input.IPv4Mask = strings.TrimSpace(input.IPv4Mask)
	input.IPv4Gateway = strings.TrimSpace(input.IPv4Gateway)

	if input.Nodo == "" || input.Nombre == "" {
		return ports.CrearInstanciaProxmoxInput{}, fmt.Errorf("nodo y nombre son obligatorios")
	}
	if input.Cores < 1 || input.MemoriaMB < 16 {
		return ports.CrearInstanciaProxmoxInput{}, fmt.Errorf("cores debe ser al menos 1 y la RAM al menos 16 MiB")
	}
	if input.StorageID == "" || input.DiscoGB < 1 {
		return ports.CrearInstanciaProxmoxInput{}, fmt.Errorf("storageId es obligatorio y discoGb debe ser al menos 1")
	}
	if input.Bridge == "" || input.TemplateID == "" {
		return ports.CrearInstanciaProxmoxInput{}, fmt.Errorf("bridge y template son obligatorios")
	}
	if input.VLAN != nil && (*input.VLAN < 1 || *input.VLAN > 4094) {
		return ports.CrearInstanciaProxmoxInput{}, fmt.Errorf("vlan debe estar entre 1 y 4094")
	}
	if input.IPv4Tipo != "dhcp" && input.IPv4Tipo != "static" {
		return ports.CrearInstanciaProxmoxInput{}, fmt.Errorf("el modo de red debe ser dhcp o static")
	}
	if input.IPv4Tipo == "static" {
		mask, err := strconv.Atoi(input.IPv4Mask)
		if net.ParseIP(input.IPv4Address) == nil || net.ParseIP(input.IPv4Gateway) == nil || err != nil || mask < 0 || mask > 32 {
			return ports.CrearInstanciaProxmoxInput{}, fmt.Errorf("la red estática requiere IP, máscara IPv4 (0-32) y gateway válidos")
		}
	}
	if exigirPasswordLXC && input.Tipo == ports.TipoInstanciaLXC && strings.TrimSpace(input.Password) == "" {
		return ports.CrearInstanciaProxmoxInput{}, fmt.Errorf("password es obligatorio para LXC")
	}
	return input, nil
}

func separarIPv4YMascara(ip, mascara string) (string, string) {
	ip = strings.TrimSpace(ip)
	mascara = strings.TrimSpace(mascara)
	if host, red, err := net.ParseCIDR(ip); err == nil && host.To4() != nil {
		unos, bits := red.Mask.Size()
		if bits == 32 {
			return host.String(), strconv.Itoa(unos)
		}
	}
	if mascara == "" && net.ParseIP(ip) != nil {
		mascara = "24"
	}
	return ip, mascara
}

func esContratoPlanoCreacion(campos map[string]json.RawMessage) bool {
	for _, campo := range []string{"tipo", "nombre", "cores", "ramMb", "discoGb", "storageId", "templateOso", "red"} {
		if _, existe := campos[campo]; existe {
			return true
		}
	}
	return false
}

// CrearInstancia reserva el siguiente VMID en Proxmox, dispara qmcreate o
// vzcreate y registra inmediatamente el UPID con el usuario autenticado.
//
// @Summary      Crear una VM o contenedor
// @Description  ADMIN ONLY. El contrato plano RF-07 descrito en el body es el formato canónico. Durante la migración del frontend también se acepta el payload anidado anterior con `type`, `node`, `name`, `cpu.cores`, `ram.memoryMb`, `disk.storageId/sizeGb`, `network.bridge/vlan/firewall/ipv4` y `os.templateId`; para LXC ese formato requiere `password`. CuotasService usa el lock Redis `lock:instance:provisioning` con TTL de 30 s y hasta 2 s de espera. Consulta la capacidad cacheada (o Proxmox si falta), valida CPU lógica, RAM con 15% de headroom y disco contra `avail` del storage, y registra una reserva temporal. Luego obtiene el VMID mediante `/cluster/nextid`, despacha la creación QEMU o LXC y solo responde 202 con vmid, tareaId y upid después de persistir la tarea RUNNING y su auditoría inicial. El resultado llega por TASK_FINISHED con accion CREATE; al completar correctamente (OK o WARNINGS), el creador recibe FULL_ACCESS y se vuelve a invalidar la capacidad.
// @Tags         Instancias Proxmox
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body body CrearInstanciaRequest true "Configuración de la instancia"
// @Success      202 {object} CreacionAceptadaResponse
// @Failure      400 {object} ErrorResponse "INVALID_REQUEST"
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "WRONG_TOKEN_TYPE | 2FA_REQUIRED | INSUFFICIENT_PERMISSIONS"
// @Failure      409 {object} ErrorResponse "RESOURCE_QUOTA_EXCEEDED"
// @Failure      500 {object} ErrorResponse "TASK_TRACKING_UNAVAILABLE"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE"
// @Failure      503 {object} ErrorResponse "PROVISIONING_COORDINATION_UNAVAILABLE"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT"
// @Router       /instances [post]
func (h *InstanceHandler) CrearInstancia(c *gin.Context) {
	var campos map[string]json.RawMessage
	if err := c.ShouldBindBodyWith(&campos, binding.JSON); err != nil {
		SendError(c, http.StatusBadRequest, "INVALID_REQUEST", "El cuerpo de la solicitud no es un JSON válido.")
		return
	}
	var (
		input ports.CrearInstanciaProxmoxInput
		err   error
	)
	if esContratoPlanoCreacion(campos) {
		var plano CrearInstanciaRequest
		if err := c.ShouldBindBodyWith(&plano, binding.JSON); err != nil {
			SendError(c, http.StatusBadRequest, "INVALID_REQUEST", "El cuerpo no cumple el esquema de creación de instancias.")
			return
		}
		input, err = normalizarCreacionPlana(plano, h.nodoPorDefecto)
	} else {
		var legado crearInstanciaWizardPayload
		if err := c.ShouldBindBodyWith(&legado, binding.JSON); err != nil {
			SendError(c, http.StatusBadRequest, "INVALID_REQUEST", "El cuerpo de la solicitud no es un JSON válido.")
			return
		}
		input, err = normalizarCreacionWizard(legado)
	}
	if err != nil {
		SendError(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if h.aprovisionamiento == nil {
		SendError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "El aprovisionamiento no está disponible.")
		return
	}
	resultado, err := h.aprovisionamiento.AprovisionarInstancia(c.Request.Context(), input)
	if err != nil {
		mapearErrorProxmox(c, err)
		return
	}
	metadatos := map[string]any{
		"instanciaNombre": input.Nombre,
		"resource_type":   input.Tipo,
		"reserva_id":      resultado.ReservaID,
	}
	tareaID, err := h.seguimiento.Seguir(c.Request.Context(), extraerUserID(c), resultado.VMID, resultado.Accion, resultado.UPID, metadatos)
	if err != nil {
		log.Printf("[TAREAS] Proxmox aceptó la creación %s de %d pero no se pudo registrar su seguimiento: %v", resultado.UPID, resultado.VMID, err)
		SendError(c, http.StatusInternalServerError, "TASK_TRACKING_UNAVAILABLE", "Proxmox aceptó la creación, pero no se pudo registrar su seguimiento. Verifique el estado antes de reintentar.")
		return
	}
	c.JSON(http.StatusAccepted, CreacionAceptadaResponse{VMID: resultado.VMID, TareaID: tareaID.String(), Upid: resultado.UPID})
}

// ==========================================
// GET /api/instances
// ==========================================

// ListarInstancias devuelve el inventario de VMs y contenedores visible para
// el usuario autenticado con campos de telemetría extendidos y resumen del cluster.
// Un ADMIN ve todo el cluster; un OPERATOR solo sus instancias asignadas.
//
// @Summary      Listar instancias
// @Description  OPERATIVO. Lee en vivo el inventario de Proxmox VE (VMs y contenedores). Un ADMIN recibe el cluster completo; un OPERATOR recibe únicamente las instancias que tiene asignadas. Incluye telemetría (CPU en porcentaje 0-100, RAM en bytes y MaxRam en bytes), nivel de acceso y tarea activa. Si la instancia está apagada (stopped), cpuUsage y ramUsage serán estrictamente null. ip puede ser null; activeTask es el objeto { tareaId, action, status } de la tarea RUNNING de la instancia, o null si no tiene ninguna.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Success      200 {array} InstanciaInventarioResponse
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      500 {object} ErrorResponse "INTERNAL_ERROR — error al consultar los permisos del usuario"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE — Proxmox caído, sin red o token rechazado"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — Proxmox no respondió a tiempo; la acción puede haberse aplicado"
// @Router       /instances [get]
func (h *InstanceHandler) ListarInstancias(c *gin.Context) {
	instancias, err := h.proxmox.ListarInstancias(c.Request.Context())
	if err != nil {
		mapearErrorProxmox(c, err)
		return
	}

	rolVal, _ := c.Get(middleware.ContextKeyRol)
	rol, _ := rolVal.(string)

	// nivelPorVmid mapea vmid → nivelAcceso para el usuario autenticado.
	// Para ADMIN está vacío (nivel implícito: acceso total, omitempty en JSON).
	nivelPorVmid := map[int]string{}

	if rol != "ADMIN" {
		userID := extraerUserID(c)
		permisos, err := h.userRepo.ListarPermisosConNivel(c.Request.Context(), userID)
		if err != nil {
			SendError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Error al consultar los permisos del usuario.")
			return
		}
		for _, p := range permisos {
			nivelPorVmid[p.Vmid] = p.NivelAcceso
		}
		// Filtrar solo las instancias con permiso explícito.
		filtradas := make([]ports.InstanciaProxmoxDTO, 0, len(instancias))
		for _, inst := range instancias {
			if _, ok := nivelPorVmid[inst.Vmid]; ok {
				filtradas = append(filtradas, inst)
			}
		}
		instancias = filtradas
	}

	// Resolver las IPs en paralelo, solo de las instancias visibles para el usuario.
	// Nunca falla: la que no se pueda resolver (apagada, sin agente) queda en null.
	ips := h.inventario.ResolverIPs(c.Request.Context(), instancias)

	// Obtener tareas activas en un único query sobre la lista visible.
	vmids := make([]int, 0, len(instancias))
	for _, inst := range instancias {
		vmids = append(vmids, inst.Vmid)
	}
	tareasActivas, err := h.tareas.BuscarTareasActivasPorVmids(c.Request.Context(), vmids)
	if err != nil {
		// No crítico: loguear y continuar sin datos de tareas.
		log.Printf("⚠️  [INSTANCES] error al buscar tareas activas: %v", err)
		tareasActivas = map[int]ports.ActiveTaskDTO{}
	}

	// Construir respuesta final con todos los campos.
	resultado := make([]ports.InstanciaListadaDTO, 0, len(instancias))
	for _, inst := range instancias {
		tipo := inst.Tipo
		if tipo == ports.TipoInstanciaQemu {
			tipo = "vm"
		}

		var activeTask *ports.ActiveTaskDTO
		if tarea, ok := tareasActivas[inst.Vmid]; ok {
			activeTask = &tarea
		}

		var nivel string
		if rol == "ADMIN" {
			nivel = ports.NivelAccesoFullAccess
		} else {
			nivel = nivelPorVmid[inst.Vmid]
		}

		var cpuUsage *float64
		var ramUsage, maxRam *int64

		if inst.Estado == "running" {
			cpu := math.Round(inst.Cpu*10000) / 100 // CPU en porcentaje con 2 decimales
			cpuUsage = &cpu
			mem := inst.Mem
			ramUsage = &mem
		}

		// maxRam siempre debe enviarse independientemente del estado,
		// salvo que Proxmox devuelva 0 (en cuyo caso dejamos el puntero nil).
		if inst.MaxMem > 0 {
			max := inst.MaxMem
			maxRam = &max
		}

		resultado = append(resultado, ports.InstanciaListadaDTO{
			ID:          inst.Vmid,
			Name:        inst.Nombre,
			Type:        tipo,
			Node:        inst.Nodo,
			Status:      inst.Estado,
			Ip:          ips[inst.Vmid],
			CpuUsage:    cpuUsage,
			RamUsage:    ramUsage,
			MaxRam:      maxRam,
			NivelAcceso: nivel,
			ActiveTask:  activeTask,
		})
	}

	c.JSON(http.StatusOK, resultado)
}

// ==========================================
// GET /api/instances/:vmid
// ==========================================

// ObtenerInstancia devuelve el estado actual de una instancia Proxmox.
//
// @Summary      Obtener instancia
// @Description  Devuelve el estado actual (running/stopped, tipo, nodo, telemetría) de una VM o contenedor leído en vivo desde Proxmox VE.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Param        vmid path int true "VMID de la instancia"
// @Success      200 {object} ports.InstanciaProxmoxDTO
// @Failure      400 {object} ErrorResponse "INVALID_VMID — el vmid de la ruta no es un número entero"
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "WRONG_TOKEN_TYPE | 2FA_REQUIRED | PASSWORD_CHANGE_REQUIRED | NO_ROLE | NO_USER | INVALID_USER_ID | INSTANCE_ACCESS_DENIED — el OPERATOR no tiene este vmid asignado"
// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND — la instancia no existe en Proxmox"
// @Failure      500 {object} ErrorResponse "INTERNAL_ERROR — al verificar permisos de acceso o error inesperado de Proxmox"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE — Proxmox caído, sin red o token rechazado"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — Proxmox no respondió a tiempo"
// @Router       /instances/{vmid} [get]
func (h *InstanceHandler) ObtenerInstancia(c *gin.Context) {
	vmid, ok := extraerVmid(c)
	if !ok {
		return
	}
	instancia, err := h.proxmox.ObtenerInstancia(c.Request.Context(), vmid)
	if err != nil {
		mapearErrorProxmox(c, err)
		return
	}
	c.JSON(http.StatusOK, instancia)
}

// ==========================================
// POST /api/instances/:vmid/start
// ==========================================

// IniciarInstancia arranca una instancia Proxmox.
//
// @Summary      Iniciar instancia
// @Description  OPERATIVO. Dispara el arranque de una VM o contenedor en Proxmox VE. La operación es asíncrona: responde 202 Accepted con el UPID de la tarea creada en Proxmox. No espera a que la instancia quede encedida.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Param        vmid path int true "VMID de la instancia"
// @Success      202 {object} AccionAceptadaResponse "Acción aceptada: upid de la tarea en Proxmox y tareaId. tareaId se omite si no se pudo registrar la tarea"
// @Failure      400 {object} ErrorResponse "INVALID_VMID — el vmid de la ruta no es un número entero"
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "WRONG_TOKEN_TYPE | 2FA_REQUIRED | PASSWORD_CHANGE_REQUIRED | NO_ROLE | NO_USER | INVALID_USER_ID | INSTANCE_ACCESS_DENIED — el OPERATOR no tiene FULL_ACCESS sobre este vmid | INSTANCE_PROTECTED — infraestructura de El Centinela"
// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND — la instancia no existe en Proxmox"
// @Failure      409 {object} ErrorResponse "INSTANCE_INVALID_STATE: La instancia se encuentra en un estado incompatible para la acción solicitada | INSTANCE_BUSY — la instancia está ejecutando otra tarea; reintentar al recibir TASK_FINISHED"
// @Failure      500 {object} ErrorResponse "INTERNAL_ERROR — al verificar permisos de acceso o error inesperado de Proxmox"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE — Proxmox caído, sin red o token rechazado; la orden NO llegó a aplicarse"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — Proxmox no respondió a tiempo; la acción puede haberse aplicado"
// @Router       /instances/{vmid}/start [post]
func (h *InstanceHandler) IniciarInstancia(c *gin.Context) {
	vmid, ok := extraerVmid(c)
	if !ok {
		return
	}
	instancia, ok := h.validarEstadoParaAccion(c, vmid, "start")
	if !ok {
		return
	}
	upid, err := h.proxmox.IniciarInstancia(c.Request.Context(), vmid)
	if err != nil {
		h.registrarAuditVM(c, vmid, instancia.Nombre, ports.AccionIniciarVM, ports.ResultadoFalla, map[string]any{
			"action": "start", "error": err.Error(),
		})
		mapearErrorProxmox(c, err)
		return
	}
	h.responderTarea(c, vmid, "start", upid, map[string]any{"instanciaNombre": instancia.Nombre, "resource_type": instancia.Tipo})
}

// ==========================================
// POST /api/instances/:vmid/stop
// ==========================================

// DetenerInstancia detiene (apagado forzado) una instancia Proxmox.
//
// @Summary      Detener instancia
// @Description  OPERATIVO. Dispara el apagado forzado de una VM o contenedor en Proxmox VE. La operación es asíncrona: responde 202 Accepted con el UPID de la tarea creada en Proxmox. No espera a que la instancia quede detenida.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Param        vmid path int true "VMID de la instancia"
// @Success      202 {object} AccionAceptadaResponse "Acción aceptada: upid de la tarea en Proxmox y tareaId. tareaId se omite si no se pudo registrar la tarea"
// @Failure      400 {object} ErrorResponse "INVALID_VMID — el vmid de la ruta no es un número entero"
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "WRONG_TOKEN_TYPE | 2FA_REQUIRED | PASSWORD_CHANGE_REQUIRED | NO_ROLE | NO_USER | INVALID_USER_ID | INSTANCE_ACCESS_DENIED | INSTANCE_PROTECTED — infraestructura de El Centinela"
// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND — la instancia no existe en Proxmox"
// @Failure      409 {object} ErrorResponse "INSTANCE_INVALID_STATE: La instancia se encuentra en un estado incompatible para la acción solicitada | INSTANCE_BUSY — la instancia está ejecutando otra tarea; reintentar al recibir TASK_FINISHED"
// @Failure      500 {object} ErrorResponse "INTERNAL_ERROR — al verificar permisos de acceso o error inesperado de Proxmox"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE — Proxmox caído, sin red o token rechazado; la orden NO llegó a aplicarse"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — Proxmox no respondió a tiempo; la acción puede haberse aplicado"
// @Router       /instances/{vmid}/stop [post]
func (h *InstanceHandler) DetenerInstancia(c *gin.Context) {
	vmid, ok := extraerVmid(c)
	if !ok {
		return
	}
	instancia, ok := h.validarEstadoParaAccion(c, vmid, "stop")
	if !ok {
		return
	}
	upid, err := h.proxmox.DetenerInstancia(c.Request.Context(), vmid)
	if err != nil {
		h.registrarAuditVM(c, vmid, instancia.Nombre, ports.AccionDetenerVM, ports.ResultadoFalla, map[string]any{
			"action": "stop", "error": err.Error(),
		})
		mapearErrorProxmox(c, err)
		return
	}
	h.responderTarea(c, vmid, "stop", upid, map[string]any{"instanciaNombre": instancia.Nombre, "resource_type": instancia.Tipo})
}

// ==========================================
// POST /api/instances/:vmid/status/:action
// ==========================================

// accionesValidas son las acciones soportadas por el endpoint genérico de ciclo de vida.
var accionesValidas = map[string]bool{
	"start":    true,
	"stop":     true,
	"shutdown": true,
	"reboot":   true,
}

// PausarInstancia documenta el contrato objetivo de la acción Pause. NO OPERATIVO:
// la ruta NO está registrada en el router y hoy responde 404 NOT_FOUND. Se
// documenta junto a las acciones reales para que el frontend no la confunda con
// shutdown ni con reboot. Las anotaciones cuelgan de esta función (no del cuerpo
// de CambiarEstado) porque swag solo genera operaciones desde declaraciones.
//
// @Summary      Pausar instancia (planificado)
// @Description  PLANIFICADO; NO OPERATIVO. Contrato objetivo para pausar una VM o contenedor en Proxmox VE. La ruta no está registrada y actualmente responde 404 NOT_FOUND; pause tampoco es una acción válida en POST /instances/{vmid}/status/{action} (responde 400 INVALID_ACTION).
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Param        vmid path int true "VMID de la instancia"
// @Success      202 {object} AccionAceptadaResponse "Acción aceptada: upid de la tarea en Proxmox y tareaId"
// @Failure      400 {object} ErrorResponse "INVALID_VMID"
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "WRONG_TOKEN_TYPE | 2FA_REQUIRED | PASSWORD_CHANGE_REQUIRED | NO_ROLE | NO_USER | INVALID_USER_ID | INSTANCE_ACCESS_DENIED | INSTANCE_PROTECTED"
// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND"
// @Failure      409 {object} ErrorResponse "INSTANCE_BUSY"
// @Failure      500 {object} ErrorResponse "INTERNAL_ERROR"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT"
// @x-implementation-status "planned"
// @Router       /instances/{vmid}/pause [post]
func (h *InstanceHandler) PausarInstancia(c *gin.Context) {
	SendError(c, http.StatusNotFound, "NOT_FOUND", "La acción pause no está implementada.")
}

// CambiarEstado ejecuta una acción de ciclo de vida (start/stop/shutdown/reboot) sobre
// una instancia. Complementa los endpoints /start y /stop (que se mantienen
// por retrocompatibilidad) y agrega soporte para reboot y shutdown.
//
// @Summary      Cambiar estado de instancia
// @Description  OPERATIVO. Ejecuta una acción de ciclo de vida (start, stop, shutdown, reboot) sobre una VM o contenedor. El endpoint genérico complementa a /start y /stop manteniendo retrocompatibilidad. La acción Pause NO está implementada: use POST /instances/{vmid}/pause, que hoy responde 404 NOT_FOUND.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Param        vmid   path int    true "VMID de la instancia"
// @Param        action path string true "Acción a ejecutar (start | stop | shutdown | reboot). pause NO es válida: responde 400 INVALID_ACTION"
// @Success      202 {object} AccionAceptadaResponse "Acción aceptada: upid de la tarea en Proxmox y tareaId. tareaId se omite si no se pudo registrar la tarea"
// @Failure      400 {object} ErrorResponse "INVALID_VMID | INVALID_ACTION — acción distinta de start, stop, shutdown o reboot"
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "WRONG_TOKEN_TYPE | 2FA_REQUIRED | PASSWORD_CHANGE_REQUIRED | NO_ROLE | NO_USER | INVALID_USER_ID | INSTANCE_ACCESS_DENIED | INSTANCE_PROTECTED"
// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND — la instancia no existe en Proxmox"
// @Failure      409 {object} ErrorResponse "INSTANCE_INVALID_STATE: La instancia se encuentra en un estado incompatible para la acción solicitada | INSTANCE_BUSY — la instancia está ejecutando otra tarea; reintentar al recibir TASK_FINISHED"
// @Failure      500 {object} ErrorResponse "INTERNAL_ERROR — al verificar permisos de acceso o error inesperado de Proxmox"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE — Proxmox caído, sin red o token rechazado; la orden NO llegó a aplicarse"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — Proxmox no respondió a tiempo; la acción puede haberse aplicado"
// @Router       /instances/{vmid}/status/{action} [post]
func (h *InstanceHandler) CambiarEstado(c *gin.Context) {
	vmid, ok := extraerVmid(c)
	if !ok {
		return
	}

	accion := strings.ToLower(c.Param("action"))
	if !accionesValidas[accion] {
		SendError(c, http.StatusBadRequest, "INVALID_ACTION",
			fmt.Sprintf("Acción '%s' no válida. Opciones: start, stop, shutdown, reboot.", accion))
		return
	}

	// Obtener la instancia primero para saber su nodo y tipo (necesario para shutdown y reboot)
	// y validar la matriz de estados antes de cualquier orden de escritura.
	instancia, ok := h.validarEstadoParaAccion(c, vmid, accion)
	if !ok {
		return
	}
	var err error

	var (
		upid        string
		accionAudit string
	)

	switch accion {
	case "start":
		upid, err = h.proxmox.IniciarInstancia(c.Request.Context(), vmid)
		accionAudit = ports.AccionIniciarVM
	case "stop":
		upid, err = h.proxmox.DetenerInstancia(c.Request.Context(), vmid)
		accionAudit = ports.AccionDetenerVM
	case "shutdown":
		upid, err = h.proxmox.Shutdown(c.Request.Context(), instancia.Nodo, vmid, instancia.Tipo)
		accionAudit = ports.AccionApagarVM
	case "reboot":
		upid, err = h.proxmox.Reboot(c.Request.Context(), instancia.Nodo, vmid, instancia.Tipo)
		accionAudit = ports.AccionReiniciarVM
	}

	if err != nil {
		h.registrarAuditVM(c, vmid, instancia.Nombre, accionAudit, ports.ResultadoFalla, map[string]any{
			"action": accion, "error": err.Error(),
		})
		mapearErrorProxmox(c, err)
		return
	}
	h.responderTarea(c, vmid, accion, upid, map[string]any{"instanciaNombre": instancia.Nombre, "resource_type": instancia.Tipo})
}

// ==========================================
// DELETE /api/instances/:vmid
// ==========================================

// EliminarInstancia elimina permanentemente una instancia de Proxmox.
// Solo puede ser invocado por ADMIN (el middleware RequireRole lo garantiza).
// Rechaza la operación con 409 si la instancia está encendida.
//
// @Summary      Eliminar instancia
// @Description  OPERATIVO. Solo ADMIN. Elimina permanentemente una VM o contenedor de Proxmox. La instancia debe estar detenida (stopped); si no, responde 409. La operación es asíncrona, igual que las acciones de energía: responde 202 Accepted con el UPID de la tarea de borrado y el tareaId; el resultado llega en el evento TASK_FINISHED con accion DELETE.
// @Tags         Instancias Proxmox
// @Produce      json
// @Security     BearerAuth
// @Param        vmid path int true "VMID de la instancia"
// @Success      202 {object} AccionAceptadaResponse "Borrado aceptado: upid de la tarea en Proxmox y tareaId. tareaId se omite si no se pudo registrar la tarea"
// @Failure      400 {object} ErrorResponse "INVALID_VMID — el vmid de la ruta no es un número entero"
// @Failure      401 {object} ErrorResponse "MISSING_TOKEN | INVALID_TOKEN | TOKEN_REVOKED"
// @Failure      403 {object} ErrorResponse "WRONG_TOKEN_TYPE | 2FA_REQUIRED | PASSWORD_CHANGE_REQUIRED | NO_ROLE | INVALID_ROLE | INSUFFICIENT_PERMISSIONS | INVALID_VMID | INSTANCE_PROTECTED — solo ADMIN puede eliminar instancias"
// @Failure      404 {object} ErrorResponse "INSTANCE_NOT_FOUND — la instancia no existe en Proxmox"
// @Failure      409 {object} ErrorResponse "INSTANCE_INVALID_STATE — la instancia debe estar detenida | INSTANCE_BUSY — la instancia está ejecutando otra tarea"
// @Failure      500 {object} ErrorResponse "INTERNAL_ERROR — error inesperado de Proxmox"
// @Failure      502 {object} ErrorResponse "PROXMOX_UNAVAILABLE — Proxmox caído, sin red o token rechazado; la orden NO llegó a aplicarse"
// @Failure      504 {object} ErrorResponse "PROXMOX_TIMEOUT — Proxmox no respondió a tiempo; el borrado puede haberse aplicado"
// @Router       /instances/{vmid} [delete]
func (h *InstanceHandler) EliminarInstancia(c *gin.Context) {
	vmid, ok := extraerVmid(c)
	if !ok {
		return
	}

	if h.tieneTareaActiva(c, vmid) {
		SendError(c, http.StatusConflict, "INSTANCE_BUSY",
			"La instancia se encuentra ejecutando otra tarea. Aguarde a que finalice.")
		return
	}

	// Verificar el estado actual de la instancia antes de intentar eliminarla.
	instancia, err := h.proxmox.ObtenerInstancia(c.Request.Context(), vmid)
	if err != nil {
		mapearErrorProxmox(c, err)
		return
	}

	// Proxmox rechazará el DELETE si la instancia está encendida; prevenimos
	// con un 409 explícito para dar un mensaje de error claro al frontend.
	if instancia.Estado != "stopped" {
		SendError(c, http.StatusConflict, "INSTANCE_INVALID_STATE",
			"La instancia debe estar detenida antes de poder eliminarla.")
		return
	}

	upid, err := h.proxmox.EliminarInstancia(c.Request.Context(), instancia.Nodo, vmid, instancia.Tipo)
	if err != nil {
		h.registrarAuditVM(c, vmid, instancia.Nombre, ports.AccionEliminarVM, ports.ResultadoFalla, map[string]any{
			"action": "delete", "error": err.Error(), "resource_type": instancia.Tipo,
		})
		mapearErrorProxmox(c, err)
		return
	}

	h.responderTarea(c, vmid, "delete", upid, map[string]any{"instanciaNombre": instancia.Nombre, "resource_type": instancia.Tipo})
}
