package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
)

// Umbrales del evaluador de alertas (RF-11), en porcentaje. Histéresis: una
// alerta se dispara cuando el valor llega al umbral alto durante
// ciclosParaAlertar ciclos seguidos, y recién se da por resuelta cuando baja
// de umbralRecuperacion. Entre los dos no cambia nada: oscilar alrededor del
// umbral alto no repite la alerta.
const (
	umbralHostRAM       = 85.0
	umbralHostCPU       = 90.0
	umbralHostDisco     = 90.0
	umbralInstanciaMem  = 95.0
	umbralInstanciaCPU  = 95.0
	umbralRecuperacion  = 80.0
	ciclosParaAlertar   = 2
	ttlAlertaActiva     = 24 * time.Hour
	ttlEstadoInstancias = 24 * time.Hour
	// ventanaTareasCentinela: una tarea de Centinela creada en este lapso
	// explica que una instancia se haya apagado (stop, rollback, borrado...).
	ventanaTareasCentinela = 10 * time.Minute
)

// Tipos de alerta (detalles.type del evento).
const (
	TipoAlertaHost      = "host_saturation"
	TipoAlertaInstancia = "resource_saturation"
	TipoAlertaCaida     = "instance_down"
)

// Claves de Redis del evaluador.
const (
	claveTurnoAlertas   = "alert:evaluator:turn"   // una sola réplica evalúa por ciclo
	claveEstadoAnterior = "alert:state:instances"  // estado de cada instancia en el ciclo anterior
	formatoPendiente    = "alert:pending:%s:%s:%s" // contador de ciclos sobre el umbral
	formatoActiva       = "alert:active:%s:%s:%s"  // alerta ya emitida (deduplicación)
	metricaCaida        = "down"                   // métrica de la alerta de caída
)

// EvaluadorAlertas es el worker que evalúa periódicamente la salud del host y
// de las instancias, y emite ALERT_TRIGGERED / ALERT_RESOLVED por el bus de
// eventos (centinela:events), con su auditoría.
type EvaluadorAlertas struct {
	nodo       ports.NodoService
	proxmox    ports.ProxmoxPort
	tareas     ports.TareaRepository
	kv         ports.KeyValueStore
	eventos    ports.EventosService
	audit      ports.AuditService
	nombreNodo string
	intervalo  time.Duration
	ahora      func() time.Time
}

// NewEvaluadorAlertas crea el evaluador. intervalo es ALERT_POLL_INTERVAL
// (30 s si no es positivo).
func NewEvaluadorAlertas(nodo ports.NodoService, proxmox ports.ProxmoxPort, tareas ports.TareaRepository, kv ports.KeyValueStore,
	eventos ports.EventosService, audit ports.AuditService, nombreNodo string, intervalo time.Duration) *EvaluadorAlertas {
	if intervalo <= 0 {
		intervalo = 30 * time.Second
	}
	return &EvaluadorAlertas{nodo: nodo, proxmox: proxmox, tareas: tareas, kv: kv, eventos: eventos, audit: audit,
		nombreNodo: nombreNodo, intervalo: intervalo, ahora: time.Now}
}

// Iniciar arranca el ciclo en segundo plano. Termina cuando se cancela ctx
// (apagado ordenado de la API): no deja una evaluación a medias colgada.
func (e *EvaluadorAlertas) Iniciar(ctx context.Context) <-chan struct{} {
	terminado := make(chan struct{})
	go func() {
		defer close(terminado)
		ticker := time.NewTicker(e.intervalo)
		defer ticker.Stop()
		log.Printf("🚨 Evaluador de alertas iniciado (cada %s)", e.intervalo)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				e.turno(ctx)
			}
		}
	}()
	return terminado
}

// turno evalúa solo si esta réplica tomó el turno del ciclo: con varias
// réplicas, cada una contaría el mismo ciclo y una sola lectura alcanzaría
// para disparar la alerta.
func (e *EvaluadorAlertas) turno(ctx context.Context) {
	tomado, err := e.kv.SetNX(ctx, claveTurnoAlertas, "1", e.intervalo*9/10)
	if err != nil {
		log.Printf("[ALERTAS] no se pudo tomar el turno de evaluación: %v", err)
		return
	}
	if tomado {
		e.Evaluar(ctx)
	}
}

// medicion es un valor contra su umbral.
type medicion struct {
	recursoTipo string // HOST | VM | LXC
	clave       string // host | instance (claves de Redis)
	recursoID   string // nodo o vmid
	vmid        int
	nombre      string
	metrica     string // ram | cpu | disk | mem
	valor       float64
	umbral      float64
}

// Evaluar hace un ciclo completo: host, saturación de instancias y caídas.
func (e *EvaluadorAlertas) Evaluar(ctx context.Context) {
	if estado, stale, err := e.nodo.ObtenerEstado(ctx); err != nil {
		log.Printf("[ALERTAS] sin estado del host: %v", err)
	} else if !stale {
		// Una lectura vieja no cuenta como un ciclo nuevo: se evalúa con datos frescos.
		for _, m := range []medicion{
			{metrica: "ram", valor: estado.RAM.UsagePercent, umbral: umbralHostRAM},
			{metrica: "cpu", valor: estado.CPU.UsagePercent, umbral: umbralHostCPU},
			{metrica: "disk", valor: estado.Storage.UsagePercent, umbral: umbralHostDisco},
		} {
			m.recursoTipo, m.clave, m.recursoID = ports.RecursoHost, "host", e.nombreNodo
			e.evaluarMedicion(ctx, m)
		}
	}
	if ctx.Err() != nil {
		return
	}

	instancias, err := e.proxmox.ListarInstancias(ctx)
	if err != nil {
		log.Printf("[ALERTAS] sin inventario de instancias: %v", err)
		return
	}
	for _, inst := range instancias {
		if inst.Estado != "running" {
			continue
		}
		base := medicion{recursoTipo: recursoDeInstancia(inst.Tipo), clave: "instance", recursoID: strconv.Itoa(inst.Vmid), vmid: inst.Vmid, nombre: inst.Nombre}
		cpu := base
		cpu.metrica, cpu.valor, cpu.umbral = "cpu", domain.PorcentajeCPU(inst.Cpu), umbralInstanciaCPU
		e.evaluarMedicion(ctx, cpu)
		if inst.MaxMem > 0 {
			mem := base
			mem.metrica, mem.valor, mem.umbral = "mem", redondear(float64(inst.Mem)/float64(inst.MaxMem)*100), umbralInstanciaMem
			e.evaluarMedicion(ctx, mem)
		}
	}
	if ctx.Err() != nil {
		return
	}
	e.evaluarCaidas(ctx, instancias)
}

// evaluarMedicion aplica la histéresis con deduplicación:
//   - valor ≥ umbral: suma un ciclo (INCR, vence en 3 intervalos). Con el
//     primer ciclo no pasa nada (pico transitorio); desde el segundo seguido
//     se dispara, una sola vez gracias a SET NX sobre la alerta activa.
//   - valor < umbral: se pierde la racha (los ciclos tienen que ser seguidos).
//   - valor < umbralRecuperacion: si había alerta activa, se resuelve.
func (e *EvaluadorAlertas) evaluarMedicion(ctx context.Context, m medicion) {
	pendiente := fmt.Sprintf(formatoPendiente, m.clave, m.recursoID, m.metrica)
	activa := fmt.Sprintf(formatoActiva, m.clave, m.recursoID, m.metrica)
	if m.valor >= m.umbral {
		ciclos, err := e.kv.Incrementar(ctx, pendiente, 3*e.intervalo)
		if err != nil {
			log.Printf("[ALERTAS] no se pudo contar %s: %v", pendiente, err)
			return
		}
		if ciclos < ciclosParaAlertar {
			return
		}
		e.disparar(ctx, activa, m, tipoDeAlerta(m), mensajeSaturacion(m))
		return
	}
	if err := e.kv.Del(ctx, pendiente); err != nil {
		log.Printf("[ALERTAS] no se pudo limpiar %s: %v", pendiente, err)
	}
	if m.valor < umbralRecuperacion {
		e.resolver(ctx, activa, m, tipoDeAlerta(m), mensajeNormalizado(m))
	}
}

// estadoInstancia es lo que se recuerda de cada instancia entre ciclos.
type estadoInstancia struct {
	Estado string `json:"estado"`
	Nombre string `json:"nombre"`
	Tipo   string `json:"tipo"`
}

// evaluarCaidas detecta instancias que estaban encendidas y ahora no lo están
// (o desaparecieron del inventario) sin una tarea de Centinela que lo explique.
// Se resuelve cuando la instancia vuelve a encender.
func (e *EvaluadorAlertas) evaluarCaidas(ctx context.Context, instancias []ports.InstanciaProxmoxDTO) {
	anterior := map[int]estadoInstancia{}
	hayAnterior := false
	if crudo, err := e.kv.Get(ctx, claveEstadoAnterior); err == nil {
		hayAnterior = json.Unmarshal([]byte(crudo), &anterior) == nil
	} else if !errors.Is(err, ports.ErrClaveNoEncontrada) {
		log.Printf("[ALERTAS] no se pudo leer el estado anterior de las instancias: %v", err)
		return
	}

	actual := make(map[int]estadoInstancia, len(instancias))
	for _, inst := range instancias {
		actual[inst.Vmid] = estadoInstancia{Estado: inst.Estado, Nombre: inst.Nombre, Tipo: inst.Tipo}
	}
	if err := e.kv.Set(ctx, claveEstadoAnterior, actual, ttlEstadoInstancias); err != nil {
		log.Printf("[ALERTAS] no se pudo guardar el estado de las instancias: %v", err)
	}
	if !hayAnterior {
		return // primer ciclo: no hay contra qué comparar
	}

	for vmid, ahora := range actual {
		m := medicion{recursoTipo: recursoDeInstancia(ahora.Tipo), clave: "instance", recursoID: strconv.Itoa(vmid), vmid: vmid, nombre: ahora.Nombre, metrica: metricaCaida}
		activa := fmt.Sprintf(formatoActiva, m.clave, m.recursoID, metricaCaida)
		if ahora.Estado == "running" {
			e.resolver(ctx, activa, m, TipoAlertaCaida, fmt.Sprintf("La instancia %s volvió a estar encendida.", ahora.Nombre))
			continue
		}
		if antes, ok := anterior[vmid]; ok && antes.Estado == "running" && !e.explicadaPorCentinela(ctx, vmid) {
			e.disparar(ctx, activa, m, TipoAlertaCaida, fmt.Sprintf("La instancia %s se detuvo sin una orden de El Centinela.", ahora.Nombre))
		}
	}
	for vmid, antes := range anterior {
		if _, sigue := actual[vmid]; sigue || antes.Estado != "running" || e.explicadaPorCentinela(ctx, vmid) {
			continue
		}
		m := medicion{recursoTipo: recursoDeInstancia(antes.Tipo), clave: "instance", recursoID: strconv.Itoa(vmid), vmid: vmid, nombre: antes.Nombre, metrica: metricaCaida}
		e.disparar(ctx, fmt.Sprintf(formatoActiva, m.clave, m.recursoID, metricaCaida), m, TipoAlertaCaida,
			fmt.Sprintf("La instancia %s dejó de aparecer en el inventario sin una orden de El Centinela.", antes.Nombre))
	}
}

// explicadaPorCentinela: hay una tarea de Centinela en curso o reciente. Ante
// un error de la base se asume que sí, para no alertar en falso.
func (e *EvaluadorAlertas) explicadaPorCentinela(ctx context.Context, vmid int) bool {
	hubo, err := e.tareas.HuboTareaDesde(ctx, vmid, e.ahora().Add(-ventanaTareasCentinela))
	if err != nil {
		log.Printf("[ALERTAS] no se pudo consultar las tareas de %d: %v", vmid, err)
		return true
	}
	return hubo
}

// disparar emite la alerta si no estaba activa (SET NX): publica
// ALERT_TRIGGERED y lo registra en la auditoría.
func (e *EvaluadorAlertas) disparar(ctx context.Context, activa string, m medicion, tipo, mensaje string) {
	alertaID := uuid.Must(uuid.NewV7())
	nueva, err := e.kv.SetNX(ctx, activa, alertaID.String(), ttlAlertaActiva)
	if err != nil {
		log.Printf("[ALERTAS] no se pudo registrar %s: %v", activa, err)
		return
	}
	if !nueva {
		return // ya se notificó: deduplicación
	}
	detalles := detallesAlerta(alertaID.String(), tipo, m)
	evento, err := ports.NewRealtimeEvent(ports.EventoAlerta, ports.SeveridadCritical, mensaje)
	if err == nil {
		err = e.eventos.Publicar(ctx, evento.ConRecurso(m.recursoTipo, recursoIDEvento(m)).ConDetalles(detalles))
	}
	if err != nil {
		log.Printf("[ALERTAS] no se pudo publicar la alerta %s: %v", activa, err)
	}
	instanciaID := ""
	if m.vmid > 0 {
		instanciaID = m.recursoID
	}
	e.audit.Registrar(ctx, ports.RegistrarAuditoriaInput{
		Accion: ports.AccionAlertaDisparada, InstanciaID: instanciaID, InstanciaNombre: m.nombre,
		RecursoTipo: m.recursoTipo, RecursoID: m.recursoID, Resultado: ports.ResultadoSuccess,
		Detalles: map[string]any{"alertId": alertaID.String(), "type": tipo, "metrica": m.metrica, "valor": m.valor, "umbral": m.umbral, "mensaje": mensaje},
	})
	log.Printf("🚨 [ALERTAS] %s", mensaje)
}

// resolver, si la alerta estaba activa, la borra y publica ALERT_RESOLVED con
// el mismo alertId.
func (e *EvaluadorAlertas) resolver(ctx context.Context, activa string, m medicion, tipo, mensaje string) {
	alertaID, err := e.kv.GetDel(ctx, activa)
	if errors.Is(err, ports.ErrClaveNoEncontrada) {
		return
	}
	if err != nil {
		log.Printf("[ALERTAS] no se pudo resolver %s: %v", activa, err)
		return
	}
	evento, err := ports.NewRealtimeEvent(ports.EventoAlertaResuelta, ports.SeveridadInfo, mensaje)
	if err == nil {
		detalles := detallesAlerta(alertaID, tipo, m)
		detalles["umbral"] = umbralRecuperacion
		err = e.eventos.Publicar(ctx, evento.ConRecurso(m.recursoTipo, recursoIDEvento(m)).ConDetalles(detalles))
	}
	if err != nil {
		log.Printf("[ALERTAS] no se pudo publicar la resolución %s: %v", activa, err)
	}
	log.Printf("✅ [ALERTAS] %s", mensaje)
}

func detallesAlerta(alertaID, tipo string, m medicion) map[string]any {
	detalles := map[string]any{"alertId": alertaID, "type": tipo, "metrica": m.metrica, "acknowledged": false}
	if m.metrica != metricaCaida {
		detalles["valor"], detalles["umbral"] = m.valor, m.umbral
	}
	return detalles
}

// recursoIDEvento: el VMID en VM y LXC (el handler del stream lo traduce a la
// referencia pública); vacío en el HOST, que no lleva recursoId.
func recursoIDEvento(m medicion) string {
	if m.recursoTipo == ports.RecursoHost {
		return ""
	}
	return m.recursoID
}

func recursoDeInstancia(tipo string) string {
	if tipo == ports.TipoInstanciaLXC {
		return ports.RecursoLXC
	}
	return ports.RecursoVM
}

func tipoDeAlerta(m medicion) string {
	if m.recursoTipo == ports.RecursoHost {
		return TipoAlertaHost
	}
	return TipoAlertaInstancia
}

var nombresMetrica = map[string]string{"ram": "RAM", "mem": "memoria", "cpu": "CPU", "disk": "almacenamiento"}

func mensajeSaturacion(m medicion) string {
	if m.recursoTipo == ports.RecursoHost {
		return fmt.Sprintf("El host superó el %.0f%% de uso de %s (%.1f%%).", m.umbral, nombresMetrica[m.metrica], m.valor)
	}
	return fmt.Sprintf("La instancia %s superó el %.0f%% de uso de %s (%.1f%%).", m.nombre, m.umbral, nombresMetrica[m.metrica], m.valor)
}

func mensajeNormalizado(m medicion) string {
	if m.recursoTipo == ports.RecursoHost {
		return fmt.Sprintf("El uso de %s del host volvió a la normalidad (%.1f%%).", nombresMetrica[m.metrica], m.valor)
	}
	return fmt.Sprintf("El uso de %s de la instancia %s volvió a la normalidad (%.1f%%).", nombresMetrica[m.metrica], m.nombre, m.valor)
}
