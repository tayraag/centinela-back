package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"sync"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
)

// ConfigAlertas define los umbrales porcentuales y la persistencia del evaluador.
// La alerta se dispara tras CiclosParaAlertar ciclos consecutivos sobre el umbral
// y se resuelve cuando el consumo baja de UmbralRecuperacion.
type ConfigAlertas struct {
	UmbralHostRAM      float64
	UmbralHostCPU      float64
	UmbralHostDisco    float64
	UmbralInstanciaMem float64
	UmbralInstanciaCPU float64
	UmbralRecuperacion float64
	CiclosParaAlertar  int
	VentanaTareas      time.Duration
	Intervalo          time.Duration
}

// ConfigAlertasPorDefecto conserva la política operativa de RF-11.
func ConfigAlertasPorDefecto() ConfigAlertas {
	return ConfigAlertas{
		UmbralHostRAM: 85, UmbralHostCPU: 90, UmbralHostDisco: 90,
		UmbralInstanciaMem: 95, UmbralInstanciaCPU: 95, UmbralRecuperacion: 80,
		CiclosParaAlertar: 2, VentanaTareas: 10 * time.Minute, Intervalo: 30 * time.Second,
	}
}

// Validar evita configuraciones que impidan alertar o resolver la histéresis.
func (c ConfigAlertas) Validar() error {
	for _, umbral := range []struct {
		nombre string
		valor  float64
	}{
		{"UmbralHostRAM", c.UmbralHostRAM}, {"UmbralHostCPU", c.UmbralHostCPU},
		{"UmbralHostDisco", c.UmbralHostDisco}, {"UmbralInstanciaMem", c.UmbralInstanciaMem},
		{"UmbralInstanciaCPU", c.UmbralInstanciaCPU},
	} {
		if math.IsNaN(umbral.valor) || math.IsInf(umbral.valor, 0) || umbral.valor <= 0 || umbral.valor > 100 {
			return fmt.Errorf("%s debe ser un porcentaje finito mayor a 0 y menor o igual a 100", umbral.nombre)
		}
		if math.IsNaN(c.UmbralRecuperacion) || math.IsInf(c.UmbralRecuperacion, 0) || c.UmbralRecuperacion < 0 || c.UmbralRecuperacion >= umbral.valor {
			return fmt.Errorf("UmbralRecuperacion debe ser mayor o igual a 0 y menor que %s", umbral.nombre)
		}
	}
	if c.CiclosParaAlertar <= 0 {
		return fmt.Errorf("CiclosParaAlertar debe ser mayor a 0")
	}
	if c.VentanaTareas <= 0 {
		return fmt.Errorf("VentanaTareas debe ser mayor a 0")
	}
	if c.Intervalo <= 0 || c.Intervalo > time.Duration(math.MaxInt64)/3 {
		return fmt.Errorf("Intervalo debe ser mayor a 0 y permitir un TTL de tres ciclos")
	}
	if int64(c.CiclosParaAlertar) > math.MaxInt64/int64(c.Intervalo) {
		return fmt.Errorf("la duración de persistencia excede el rango permitido")
	}
	return nil
}

const (
	ttlAlertaActiva     = 24 * time.Hour
	ttlEstadoInstancias = 24 * time.Hour
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
	nodo                    ports.NodoService
	proxmox                 ports.ProxmoxPort
	tareas                  ports.TareaRepository
	kv                      ports.KeyValueStore
	eventos                 ports.EventosService
	audit                   ports.AuditService
	nombreNodo              string
	ignorarApagadosExternos bool
	config                  ConfigAlertas
	ahora                   func() time.Time

	destinatarios ports.DestinatariosAlertas // a quién mandar el correo de una alerta crítica
	email         ports.EmailService
	correos       sync.WaitGroup // correos en envío (el apagado los espera)
}

// NewEvaluadorAlertas crea el evaluador con una configuración validada.
// La opción final habilita ALERT_IGNORE_EXTERNAL_SHUTDOWN (false por defecto).
func NewEvaluadorAlertas(nodo ports.NodoService, proxmox ports.ProxmoxPort, tareas ports.TareaRepository, kv ports.KeyValueStore,
	eventos ports.EventosService, audit ports.AuditService, destinatarios ports.DestinatariosAlertas, email ports.EmailService,
	nombreNodo string, config ConfigAlertas, ignorarApagadosExternos ...bool) (*EvaluadorAlertas, error) {
	if err := config.Validar(); err != nil {
		return nil, fmt.Errorf("configuración de alertas inválida: %w", err)
	}
	return &EvaluadorAlertas{nodo: nodo, proxmox: proxmox, tareas: tareas, kv: kv, eventos: eventos, audit: audit,
		ignorarApagadosExternos: len(ignorarApagadosExternos) > 0 && ignorarApagadosExternos[0],
		destinatarios:           destinatarios, email: email, nombreNodo: nombreNodo, config: config, ahora: time.Now}, nil
}

// EsperarCorreos espera a que terminen los correos en envío.
func (e *EvaluadorAlertas) EsperarCorreos() { e.correos.Wait() }

// Iniciar arranca el ciclo en segundo plano. Termina cuando se cancela ctx
// (apagado ordenado de la API): no deja una evaluación a medias colgada y
// espera los correos que se estén enviando antes de cerrar el canal devuelto.
func (e *EvaluadorAlertas) Iniciar(ctx context.Context) <-chan struct{} {
	terminado := make(chan struct{})
	go func() {
		defer close(terminado)
		defer e.correos.Wait()
		ticker := time.NewTicker(e.config.Intervalo)
		defer ticker.Stop()
		log.Printf("🚨 Evaluador de alertas iniciado (cada %s)", e.config.Intervalo)
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
	tomado, err := e.kv.SetNX(ctx, claveTurnoAlertas, "1", e.config.Intervalo-e.config.Intervalo/10)
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
			{metrica: "ram", valor: estado.RAM.UsagePercent, umbral: e.config.UmbralHostRAM},
			{metrica: "cpu", valor: estado.CPU.UsagePercent, umbral: e.config.UmbralHostCPU},
			{metrica: "disk", valor: estado.Storage.UsagePercent, umbral: e.config.UmbralHostDisco},
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
		cpu.metrica, cpu.valor, cpu.umbral = "cpu", domain.PorcentajeCPU(inst.Cpu), e.config.UmbralInstanciaCPU
		e.evaluarMedicion(ctx, cpu)
		if inst.MaxMem > 0 {
			mem := base
			mem.metrica, mem.valor, mem.umbral = "mem", redondear(float64(inst.Mem)/float64(inst.MaxMem)*100), e.config.UmbralInstanciaMem
			e.evaluarMedicion(ctx, mem)
		}
	}
	if ctx.Err() != nil {
		return
	}
	e.evaluarCaidas(ctx, instancias)
}

// evaluarMedicion aplica la histéresis con deduplicación:
//   - valor ≥ umbral: suma un ciclo (INCR, vence en 3 intervalos). Al completar
//     ConfigAlertas.CiclosParaAlertar ciclos consecutivos se dispara una sola vez
//     gracias a SET NX sobre la alerta activa.
//   - valor < umbral: se pierde la racha (los ciclos tienen que ser seguidos).
//   - valor < umbralRecuperacion: si había alerta activa, se resuelve.
func (e *EvaluadorAlertas) evaluarMedicion(ctx context.Context, m medicion) {
	pendiente := fmt.Sprintf(formatoPendiente, m.clave, m.recursoID, m.metrica)
	activa := fmt.Sprintf(formatoActiva, m.clave, m.recursoID, m.metrica)
	if m.valor >= m.umbral {
		ciclos, err := e.kv.Incrementar(ctx, pendiente, time.Duration(e.config.CiclosParaAlertar)*e.config.Intervalo)
		if err != nil {
			log.Printf("[ALERTAS] no se pudo contar %s: %v", pendiente, err)
			return
		}
		if ciclos < int64(e.config.CiclosParaAlertar) {
			return
		}
		e.disparar(ctx, activa, m, tipoDeAlerta(m), mensajeSaturacion(m))
		return
	}
	if err := e.kv.Del(ctx, pendiente); err != nil {
		log.Printf("[ALERTAS] no se pudo limpiar %s: %v", pendiente, err)
	}
	if m.valor < e.config.UmbralRecuperacion {
		e.resolver(ctx, activa, m, tipoDeAlerta(m), mensajeNormalizado(m))
	}
}

// estadoInstancia es lo que se recuerda de cada instancia entre ciclos.
type estadoInstancia struct {
	Estado string `json:"estado"`
	Nombre string `json:"nombre"`
	Tipo   string `json:"tipo"`
	Nodo   string `json:"nodo"`
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
		actual[inst.Vmid] = estadoInstancia{Estado: inst.Estado, Nombre: inst.Nombre, Tipo: inst.Tipo, Nodo: inst.Nodo}
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
			if ahora.Estado == "stopped" && e.explicadaPorProxmox(ctx, vmid, ahora, m) {
				continue
			}
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
	hubo, err := e.tareas.HuboTareaDesde(ctx, vmid, e.ahora().Add(-e.config.VentanaTareas))
	if err != nil {
		log.Printf("[ALERTAS] no se pudo consultar las tareas de %d: %v", vmid, err)
		return true
	}
	return hubo
}

// explicadaPorProxmox concilia únicamente paradas con una tarea externa confirmada.
// Sin evidencia (incluido un error de API), se conserva la alerta de caída.
func (e *EvaluadorAlertas) explicadaPorProxmox(ctx context.Context, vmid int, instancia estadoInstancia, m medicion) bool {
	if !e.ignorarApagadosExternos || instancia.Nodo == "" {
		return false
	}
	ahora := e.ahora()
	desde := ahora.Add(-e.config.VentanaTareas)
	tareas, err := e.proxmox.ListarTareasInstancia(ctx, instancia.Nodo, vmid, desde)
	if err != nil {
		log.Printf("[ALERTAS] no se pudieron conciliar las tareas externas de %d: %v", vmid, err)
		return false
	}
	for _, tarea := range tareas {
		if tarea.ID != strconv.Itoa(vmid) || tarea.Inicio < desde.Unix() || tarea.Inicio > ahora.Unix() {
			continue
		}
		switch tarea.Tipo {
		case "qmstop", "qmshutdown", "vzstop", "vzdump":
		default:
			continue
		}
		finalizadaOK := tarea.Estado == "OK"
		enCurso := tarea.Estado == "RUNNING" && tarea.Fin == 0
		if !finalizadaOK && !enCurso {
			continue
		}
		e.registrarAuditoria(ctx, ports.AccionInstanciaDetenidaExternamente, m,
			map[string]any{"severidad": ports.SeveridadInfo, "tipoTarea": tarea.Tipo},
			fmt.Sprintf("La instancia %s se detuvo por una tarea externa de Proxmox (%s).", instancia.Nombre, tarea.Tipo), ahora, nil)
		return true
	}
	return false
}

// disparar emite la alerta si no estaba activa (SET NX): publica
// ALERT_TRIGGERED en el bus (llega por SSE en el momento), la registra en la
// auditoría con los destinatarios del correo y manda el correo en segundo
// plano, sin demorar el ciclo.
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
	ahora := e.ahora()
	detalles := detallesAlerta(alertaID.String(), tipo, m)
	if m.metrica != metricaCaida {
		detalles["duracionPersistencia"] = fmt.Sprintf("%ds", int((time.Duration(e.config.CiclosParaAlertar) * e.config.Intervalo).Seconds()))
	}
	evento, err := ports.NewRealtimeEvent(ports.EventoAlerta, ports.SeveridadCritical, mensaje)
	if err == nil {
		err = e.eventos.Publicar(ctx, evento.ConRecurso(m.recursoTipo, recursoIDEvento(m)).ConDetalles(detalles))
	}
	if err != nil {
		log.Printf("[ALERTAS] no se pudo publicar la alerta %s: %v", activa, err)
	}

	destinatarios, err := e.destinatarios.EmailsParaAlerta(ctx, m.vmid)
	if err != nil {
		log.Printf("[ALERTAS] no se pudieron obtener los destinatarios del correo de %s: %v", activa, err)
	}
	e.registrarAuditoria(ctx, ports.AccionAlertaDisparada, m, detalles, mensaje, ahora, destinatarios)
	log.Printf("🚨 [ALERTAS] %s", mensaje)
	if len(destinatarios) == 0 {
		return
	}
	recurso := m.nombre
	if m.recursoTipo == ports.RecursoHost {
		recurso = e.nombreNodo
	}
	correo := ports.AlertaCorreo{AlertaID: alertaID.String(), Tipo: tipo, RecursoTipo: m.recursoTipo, Recurso: recurso,
		Metrica: nombresMetrica[m.metrica], Valor: m.valor, Umbral: m.umbral, Mensaje: mensaje, FechaHora: ahora}
	e.correos.Add(1)
	go func() {
		defer e.correos.Done()
		if err := e.email.EnviarAlertaCritica(destinatarios, correo); err != nil {
			log.Printf("[ALERTAS] falló el envío del correo de la alerta %s: %v", correo.AlertaID, err)
			return
		}
		log.Printf("📧 [ALERTAS] correo de la alerta %s enviado a %d destinatario(s)", correo.AlertaID, len(destinatarios))
	}()
}

// resolver, si la alerta estaba activa, la borra, publica ALERT_RESOLVED con
// el mismo alertId y lo registra en la auditoría.
func (e *EvaluadorAlertas) resolver(ctx context.Context, activa string, m medicion, tipo, mensaje string) {
	alertaID, err := e.kv.GetDel(ctx, activa)
	if errors.Is(err, ports.ErrClaveNoEncontrada) {
		return
	}
	if err != nil {
		log.Printf("[ALERTAS] no se pudo resolver %s: %v", activa, err)
		return
	}
	detalles := detallesAlerta(alertaID, tipo, m)
	if m.metrica != metricaCaida {
		detalles["umbralRecuperacion"] = e.config.UmbralRecuperacion
	}
	evento, err := ports.NewRealtimeEvent(ports.EventoAlertaResuelta, ports.SeveridadInfo, mensaje)
	if err == nil {
		err = e.eventos.Publicar(ctx, evento.ConRecurso(m.recursoTipo, recursoIDEvento(m)).ConDetalles(detalles))
	}
	if err != nil {
		log.Printf("[ALERTAS] no se pudo publicar la resolución %s: %v", activa, err)
	}
	e.registrarAuditoria(ctx, ports.AccionAlertaResuelta, m, detalles, mensaje, e.ahora(), nil)
	log.Printf("✅ [ALERTAS] %s", mensaje)
}

// registrarAuditoria deja la fila inmutable de la alerta o su resolución (sin
// usuario: la genera el sistema). recurso_id es el VMID o "host".
func (e *EvaluadorAlertas) registrarAuditoria(ctx context.Context, accion string, m medicion, detalles map[string]any, mensaje string, cuando time.Time, destinatarios []string) {
	instanciaID, recursoID := "", "host"
	if m.vmid > 0 {
		instanciaID, recursoID = m.recursoID, m.recursoID
	}
	traza := make(map[string]any, len(detalles)+3)
	for k, v := range detalles {
		traza[k] = v
	}
	traza["mensaje"] = mensaje
	traza["timestamp"] = cuando.UTC().Format(time.RFC3339)
	if accion == ports.AccionAlertaDisparada {
		if destinatarios == nil {
			destinatarios = []string{}
		}
		traza["destinatarios"] = destinatarios
	}
	e.audit.Registrar(ctx, ports.RegistrarAuditoriaInput{
		Accion: accion, InstanciaID: instanciaID, InstanciaNombre: m.nombre,
		RecursoTipo: m.recursoTipo, RecursoID: recursoID, Resultado: ports.ResultadoSuccess, Detalles: traza,
	})
}

func detallesAlerta(alertaID, tipo string, m medicion) map[string]any {
	detalles := map[string]any{"alertId": alertaID, "type": tipo, "metrica": m.metrica, "acknowledged": false}
	if m.metrica != metricaCaida {
		detalles["valorActual"], detalles["umbralCritico"] = m.valor, m.umbral
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

var nombresMetrica = map[string]string{"ram": "Memoria RAM", "mem": "memoria", "cpu": "CPU", "disk": "almacenamiento"}

func mensajeSaturacion(m medicion) string {
	if m.recursoTipo == ports.RecursoHost {
		return fmt.Sprintf("Saturación crítica: Uso de %s en el host al %.1f%% (límite %.0f%%)", nombresMetrica[m.metrica], m.valor, m.umbral)
	}
	return fmt.Sprintf("Saturación crítica: Uso de %s en la instancia %s al %.1f%% (límite %.0f%%)", nombresMetrica[m.metrica], m.nombre, m.valor, m.umbral)
}

func mensajeNormalizado(m medicion) string {
	if m.recursoTipo == ports.RecursoHost {
		return fmt.Sprintf("El uso de %s del host volvió a la normalidad (%.1f%%).", nombresMetrica[m.metrica], m.valor)
	}
	return fmt.Sprintf("El uso de %s de la instancia %s volvió a la normalidad (%.1f%%).", nombresMetrica[m.metrica], m.nombre, m.valor)
}
