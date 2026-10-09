package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
)

// ventanaRecuperacion: al arrancar la API, las tareas RUNNING más viejas que
// esto no se reanudan: se consultan una sola vez y, si no terminaron, se
// cierran como FAILED con motivo TIMEOUT (RNF-04).
const ventanaRecuperacion = 3 * time.Minute

// timeoutConsulta acota cada consulta a Proxmox. La consulta usa su propio
// contexto: el apagado de la API no la corta a la mitad.
const timeoutConsulta = 15 * time.Second

// nombresAccion traduce la acción al texto del mensaje del evento.
var nombresAccion = map[string]string{"start": "encendido", "stop": "apagado", "shutdown": "apagado ordenado", "reboot": "reinicio", "delete": "eliminación"}

// mensajeTimeout es el error de una tarea que Proxmox no dio por terminada
// dentro del límite.
func mensajeTimeout(limite time.Duration) string {
	return fmt.Sprintf("Excedido el límite máximo de ejecución de %d minutos", int(limite.Minutes()))
}

// resultadoTarea es el desenlace de una tarea:
//   - COMPLETED: exitStatus "OK" (o "WARNINGS: N" cuando Proxmox la dio por
//     buena con advertencias), sin motivo ni error.
//   - FAILED por Proxmox: motivo PROXMOX_ERROR; exitStatus y error, el texto de Proxmox.
//   - FAILED por tiempo: motivo TIMEOUT, sin exitStatus (Proxmox no la cerró).
type ResultadoTarea struct {
	Estado     string
	ExitStatus string // "" = null
	Motivo     string // "" = null
	Error      string // "" = null
}

func completada() ResultadoTarea {
	return ResultadoTarea{Estado: ports.TareaCompleted, ExitStatus: "OK"}
}

// completadaConAdvertencias cierra una tarea que Proxmox dio por buena pero con
// advertencias (exitstatus "WARNINGS: N"). El texto se conserva tal cual; no es
// un fallo ni lleva motivo/error.
func completadaConAdvertencias(exitStatus string) ResultadoTarea {
	return ResultadoTarea{Estado: ports.TareaCompleted, ExitStatus: exitStatus}
}

// clasificarExitStatus traduce el exitstatus de Proxmox al desenlace de la tarea:
//   - "OK": completada sin observaciones.
//   - "WARNINGS: N": completada con advertencias (p. ej. "WARN: Systemd 257
//     detected. You may need to enable nesting."); Proxmox la considera exitosa.
//   - cualquier otro valor: el texto del error que reportó Proxmox.
func clasificarExitStatus(exitStatus string) ResultadoTarea {
	if exitStatus == "OK" {
		return completada()
	}
	if strings.HasPrefix(exitStatus, "WARNINGS") {
		return completadaConAdvertencias(exitStatus)
	}
	return falloProxmox(exitStatus)
}

func falloProxmox(exitStatus string) ResultadoTarea {
	return ResultadoTarea{Estado: ports.TareaFailed, ExitStatus: exitStatus, Motivo: ports.MotivoProxmoxError, Error: exitStatus}
}

func vencida(limite time.Duration) ResultadoTarea {
	return ResultadoTarea{Estado: ports.TareaFailed, Motivo: ports.MotivoTimeout, Error: mensajeTimeout(limite)}
}

// metadatos es lo que se guarda en tareas_asincronas.metadatos: {motivo, error}
// en una tarea FAILED, nada en una COMPLETED.
func (r ResultadoTarea) metadatos() map[string]any {
	if r.Estado != ports.TareaFailed {
		return nil
	}
	return map[string]any{"motivo": r.Motivo, "error": r.Error}
}

// detalles arma detalles.{tareaId, accion, estado, exitstatus, motivo, error}
// del TASK_FINISHED (y de la auditoría): siempre las seis claves, con null
// cuando no aplican. accion va en mayúsculas, igual que en la auditoría.
func (r ResultadoTarea) Detalles(tarea *domain.TareaAsincrona) map[string]any {
	return map[string]any{
		"tareaId": tarea.ID.String(), "accion": strings.ToUpper(tarea.Accion), "estado": r.Estado,
		"exitstatus": nulo(r.ExitStatus), "motivo": nulo(r.Motivo), "error": nulo(r.Error),
	}
}

func nulo(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ConfigSeguimiento configura el pool de seguimiento de tareas.
type ConfigSeguimiento struct {
	Workers              int           // consultas simultáneas máximas a Proxmox (UPID_WORKERS, default 8)
	Buffer               int           // capacidad de la cola (tareasChan, default 100)
	Intervalo            time.Duration // cada cuánto se vuelve a consultar una tarea (default 1 s)
	IntervaloReconciliar time.Duration // cada cuánto el reconciliador revisa la base (default 5 s)
	VentanaRecuperacion  time.Duration // al arrancar, edad máxima para reanudar una tarea (default 3 min)
	UpidTimeout          time.Duration // límite de seguimiento de tarea asíncrona (UPID_TIMEOUT, default 3m)
	OnTaskFinished       func(ctx context.Context, tarea *domain.TareaAsincrona, recursoTipo string, r ResultadoTarea)
}

func (c ConfigSeguimiento) conDefaults() ConfigSeguimiento {
	if c.Workers <= 0 {
		c.Workers = 8
	}
	if c.Buffer <= 0 {
		c.Buffer = 100
	}
	if c.Intervalo <= 0 {
		c.Intervalo = time.Second
	}
	if c.IntervaloReconciliar <= 0 {
		c.IntervaloReconciliar = 5 * time.Second
	}
	if c.VentanaRecuperacion <= 0 {
		c.VentanaRecuperacion = ventanaRecuperacion
	}
	if c.UpidTimeout <= 0 {
		c.UpidTimeout = 3 * time.Minute
	}
	return c
}

// TareaSeguimiento es una tarea dentro del pool.
type TareaSeguimiento struct {
	Tarea       domain.TareaAsincrona
	Vmid          int
	recursoTipo   string // VM | LXC, se resuelve en la primera consulta
	intentosFallo int    // reintentos de red o errores transitorios
}

// PoolSeguimiento implementa ports.SeguimientoTareas con un worker pool acotado:
//   - Solo los Workers consultan a Proxmox, así que nunca hay más de Workers
//     consultas simultáneas.
//   - Cada worker consulta una tarea UNA vez; si no terminó, la vuelve a encolar
//     para dentro de Intervalo. Así todas las tareas avanzan a la par.
//   - Seguir() nunca bloquea: si tareasChan está llena, la tarea queda RUNNING
//     en tareas_asincronas y la toma el reconciliador.
//   - El reconciliador revisa la base cada IntervaloReconciliar y encola las
//     tareas RUNNING que no se están siguiendo (cola llena, o reinicio de la API).
type PoolSeguimiento struct {
	proxmox ports.ProxmoxPort
	tareas  ports.TareaRepository
	cfg     ConfigSeguimiento
	ahora   func() time.Time

	tareasChan chan *TareaSeguimiento

	mu      sync.Mutex
	enCurso map[uuid.UUID]bool // tareas dentro del pool (en la cola o esperando reconsulta)
	activo  bool

	wg  sync.WaitGroup
	fin chan struct{}
	una sync.Once
}

var _ ports.SeguimientoTareas = (*PoolSeguimiento)(nil)

// NewSeguimientoTareas crea el pool. Hay que arrancarlo con Iniciar.
func NewSeguimientoTareas(proxmox ports.ProxmoxPort, tareas ports.TareaRepository, cfg ConfigSeguimiento) *PoolSeguimiento {
	cfg = cfg.conDefaults()
	return &PoolSeguimiento{
		proxmox: proxmox, tareas: tareas, cfg: cfg, ahora: time.Now,
		tareasChan: make(chan *TareaSeguimiento, cfg.Buffer),
		enCurso:    map[uuid.UUID]bool{},
		fin:        make(chan struct{}),
	}
}

// Iniciar arranca los workers, recupera las tareas que quedaron RUNNING de
// un arranque anterior (ver recuperar) y arranca el reconciliador. Cuando ctx
// se cancela (SIGINT o SIGTERM) dejan de tomar trabajo nuevo; la consulta que
// esté en curso termina igual. Las tareas sin terminar quedan RUNNING en la
// base y se recuperan en el próximo arranque.
func (p *PoolSeguimiento) Iniciar(ctx context.Context) {
	p.mu.Lock()
	p.activo = true
	p.mu.Unlock()

	for i := 0; i < p.cfg.Workers; i++ {
		p.wg.Add(1)
		go p.worker()
	}
	p.recuperar()
	p.wg.Add(1)
	go p.reconciliador()

	go func() {
		<-ctx.Done()
		p.mu.Lock()
		p.activo = false
		p.mu.Unlock()
		p.una.Do(func() { close(p.fin) })
	}()
	log.Printf("🔁 Seguimiento de tareas iniciado: %d workers, cola de %d, consulta cada %s", p.cfg.Workers, p.cfg.Buffer, p.cfg.Intervalo)
}

// Esperar bloquea hasta que terminan los workers y el reconciliador (después de
// cancelar el contexto de Iniciar), o hasta que vence ctx.
func (p *PoolSeguimiento) Esperar(ctx context.Context) error {
	listo := make(chan struct{})
	go func() { p.wg.Wait(); close(listo) }()
	select {
	case <-listo:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Seguir registra la tarea en tareas_asincronas (RUNNING) y la encola sin
// bloquear. Si la cola está llena, la deja para el reconciliador.
func (p *PoolSeguimiento) Seguir(ctx context.Context, usuarioID uuid.UUID, vmid int, accion, upid string, metadatos ...map[string]any) (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, err
	}
	tarea := domain.TareaAsincrona{
		ID: id, UsuarioID: usuarioID, UpidProxmox: upid,
		InstanciaID: strconv.Itoa(vmid), Accion: accion, Estado: ports.TareaRunning,
		FechaCreacion: p.ahora(),
	}

	if len(metadatos) > 0 && metadatos[0] != nil {
		if crudo, err := json.Marshal(metadatos[0]); err == nil {
			str := string(crudo)
			tarea.Metadatos = &str
		}
	}
	if err := p.tareas.Crear(ctx, &tarea); err != nil {
		return uuid.Nil, err
	}
	if !p.encolar(&TareaSeguimiento{Tarea: tarea, Vmid: vmid}) {
		log.Printf("[TAREAS] cola llena: la tarea %s queda RUNNING para el reconciliador", id)
	}
	return id, nil
}

// encolar intenta meter la tarea en tareasChan sin bloquear. Devuelve false si
// la cola está llena o el pool se está apagando.
func (p *PoolSeguimiento) encolar(t *TareaSeguimiento) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.activo {
		delete(p.enCurso, t.Tarea.ID)
		return false
	}
	select {
	case p.tareasChan <- t:
		p.enCurso[t.Tarea.ID] = true
		return true
	default:
		delete(p.enCurso, t.Tarea.ID)
		return false
	}
}

// worker toma tareas de la cola y las consulta una vez cada una.
func (p *PoolSeguimiento) worker() {
	defer p.wg.Done()
	for {
		select {
		case <-p.fin:
			return
		case t := <-p.tareasChan:
			p.consultar(t)
		}
	}
}

// recuperar retoma, al arrancar la API, las tareas que quedaron RUNNING de un
// arranque anterior (caída, reinicio o deploy). Se llama desde Iniciar, antes
// de que el servidor HTTP acepte requests:
//   - creadas hace VentanaRecuperacion (3 min) o menos: se reencolan y siguen
//     el sondeo normal;
//   - más viejas: se consultan UNA vez en segundo plano (verificarUnaVez). Si
//     Proxmox ya las terminó se registra el resultado real; si siguen
//     corriendo o Proxmox no responde, quedan FAILED con motivo TIMEOUT.
func (p *PoolSeguimiento) recuperar() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pendientes, err := p.tareas.ListarEnCurso(ctx)
	if err != nil {
		log.Printf("[TAREAS] no se pudieron recuperar las tareas en curso (las retoma el reconciliador): %v", err)
		return
	}
	var reanudadas, vencidas []*TareaSeguimiento
	for _, tarea := range pendientes {
		vmid, err := strconv.Atoi(tarea.InstanciaID)
		if err != nil {
			continue
		}
		t := &TareaSeguimiento{Tarea: tarea, Vmid: vmid}
		if p.ahora().Sub(tarea.FechaCreacion) > p.cfg.VentanaRecuperacion {
			vencidas = append(vencidas, t)
		} else {
			reanudadas = append(reanudadas, t)
		}
	}
	if len(pendientes) > 0 {
		log.Printf("[TAREAS] recuperación al arrancar: %d tarea(s) RUNNING: %d se reanudan, %d superaron %s y se verifican una vez",
			len(pendientes), len(reanudadas), len(vencidas), p.cfg.VentanaRecuperacion)
	}

	// Las vencidas quedan marcadas en curso para que el reconciliador no las
	// encole mientras se verifican. Como mucho Workers consultas a la vez.
	p.mu.Lock()
	for _, t := range vencidas {
		p.enCurso[t.Tarea.ID] = true
	}
	p.mu.Unlock()
	turnos := make(chan struct{}, p.cfg.Workers)
	for _, t := range vencidas {
		p.wg.Add(1)
		go func(t *TareaSeguimiento) {
			defer p.wg.Done()
			turnos <- struct{}{}
			defer func() { <-turnos }()
			p.verificarUnaVez(t)
		}(t)
	}
	for _, t := range reanudadas {
		if !p.encolar(t) {
			log.Printf("[TAREAS] cola llena: la tarea %s queda para el reconciliador", t.Tarea.ID)
		}
	}
}

// verificarUnaVez hace la única consulta de una tarea que superó la ventana de
// recuperación y la cierra con lo que responda Proxmox.
func (p *PoolSeguimiento) verificarUnaVez(t *TareaSeguimiento) {
	ctx, cancel := context.WithTimeout(context.Background(), timeoutConsulta)
	defer cancel()
	p.resolverRecurso(ctx, t)

	resultado, err := p.proxmox.EstadoTarea(ctx, t.Tarea.UpidProxmox)
	switch {
	case err == nil && resultado.Terminada:
		p.finalizar(t, clasificarExitStatus(resultado.ExitStatus))
	default:
		if err != nil {
			log.Printf("[TAREAS] la tarea %s superó %s y Proxmox no respondió: %v", t.Tarea.ID, p.cfg.VentanaRecuperacion, err)
		}
		p.finalizar(t, vencida(p.cfg.VentanaRecuperacion))
	}
}

// resolverRecurso averigua si la instancia es VM o LXC (para recursoTipo del
// evento). Si no se puede (por ejemplo, ya se borró), queda VM.
func (p *PoolSeguimiento) resolverRecurso(ctx context.Context, t *TareaSeguimiento) {
	if t.recursoTipo != "" {
		return
	}
	if t.Tarea.Metadatos != nil {
		var meta map[string]any
		if err := json.Unmarshal([]byte(*t.Tarea.Metadatos), &meta); err == nil {
			if rt, ok := meta["resource_type"].(string); ok {
				t.recursoTipo = ports.RecursoVM
				if rt == ports.TipoInstanciaLXC {
					t.recursoTipo = ports.RecursoLXC
				}
				return
			}
		}
	}
	t.recursoTipo = ports.RecursoVM
	if inst, err := p.proxmox.ObtenerInstancia(ctx, t.Vmid); err == nil && inst.Tipo == ports.TipoInstanciaLXC {
		t.recursoTipo = ports.RecursoLXC
	}
}

// consultar hace UNA consulta del estado de la tarea. Si terminó (o venció el
// límite) la cierra; si no, la reprograma para dentro de Intervalo (con backoff si falla).
func (p *PoolSeguimiento) consultar(t *TareaSeguimiento) {
	ctx, cancel := context.WithTimeout(context.Background(), timeoutConsulta)
	defer cancel()

	p.resolverRecurso(ctx, t)

	resultado, err := p.proxmox.EstadoTarea(ctx, t.Tarea.UpidProxmox)
	switch {
	case err == nil && resultado.Terminada:
		p.finalizar(t, clasificarExitStatus(resultado.ExitStatus))
		return
	case err != nil:
		t.intentosFallo++
		log.Printf("[TAREAS] no se pudo consultar la tarea %s (intento %d): %v", t.Tarea.ID, t.intentosFallo, err)
	case err == nil && !resultado.Terminada:
		t.intentosFallo = 0 // reset al recuperar conectividad
	}

	if p.ahora().Sub(t.Tarea.FechaCreacion) > p.cfg.UpidTimeout {
		p.finalizar(t, vencida(p.cfg.UpidTimeout))
		return
	}

	intervalo := p.cfg.Intervalo
	if t.intentosFallo > 0 {
		// Backoff exponencial: 1s, 2s, 4s, tope 5s
		backoffSecs := 1 << (t.intentosFallo - 1)
		intervalo = time.Duration(backoffSecs) * time.Second
		if intervalo > 5*time.Second {
			intervalo = 5 * time.Second
		}
	}

	// Reprogramar sin ocupar un worker mientras espera.
	time.AfterFunc(intervalo, func() {
		if !p.encolar(t) {
			// Cola llena o apagándose: queda RUNNING y la retoma el reconciliador.
			log.Printf("[TAREAS] la tarea %s vuelve al reconciliador", t.Tarea.ID)
		}
	})
}

// finalizar guarda el estado final, audita y publica TASK_FINISHED.
func (p *PoolSeguimiento) finalizar(t *TareaSeguimiento, r ResultadoTarea) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer func() {
		p.mu.Lock()
		delete(p.enCurso, t.Tarea.ID)
		p.mu.Unlock()
	}()

	if err := p.tareas.ActualizarEstado(ctx, t.Tarea.ID, r.Estado, r.metadatos()); err != nil {
		log.Printf("[TAREAS] %v", err)
	}

	log.Printf("[TAREAS] tarea %s (%s sobre %d) terminó: %s", t.Tarea.ID, t.Tarea.Accion, t.Vmid, r.Estado)
	
	if p.cfg.OnTaskFinished != nil {
		p.cfg.OnTaskFinished(ctx, &t.Tarea, t.recursoTipo, r)
	}
}

// reconciliador encola las tareas RUNNING de la base que no están en el pool:
// las que no entraron por cola llena y las que quedaron de un reinicio.
func (p *PoolSeguimiento) reconciliador() {
	defer p.wg.Done()
	ticker := time.NewTicker(p.cfg.IntervaloReconciliar)
	defer ticker.Stop()
	for {
		p.reconciliar()
		select {
		case <-p.fin:
			return
		case <-ticker.C:
		}
	}
}

func (p *PoolSeguimiento) reconciliar() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pendientes, err := p.tareas.ListarEnCurso(ctx)
	if err != nil {
		log.Printf("[TAREAS] reconciliador: %v", err)
		return
	}
	for _, tarea := range pendientes {
		p.mu.Lock()
		ya := p.enCurso[tarea.ID]
		p.mu.Unlock()
		if ya {
			continue
		}
		vmid, err := strconv.Atoi(tarea.InstanciaID)
		if err != nil {
			continue
		}
		if !p.encolar(&TareaSeguimiento{Tarea: tarea, Vmid: vmid}) {
			return // cola llena: el resto, en la próxima vuelta
		}
	}
}

// EventoTareaFinalizada arma el TASK_FINISHED de docs/contrato-eventos.md.
func EventoTareaFinalizada(tarea *domain.TareaAsincrona, recursoTipo string, r ResultadoTarea) ports.RealtimeEvent {
	nombre := nombresAccion[tarea.Accion]
	if nombre == "" {
		nombre = tarea.Accion
	}
	if recursoTipo == "" {
		recursoTipo = ports.RecursoVM
	}
	severidad, mensaje := ports.SeveridadInfo, fmt.Sprintf("La tarea de %s finalizó correctamente", nombre)
	if r.Estado != ports.TareaCompleted {
		severidad, mensaje = ports.SeveridadWarning, fmt.Sprintf("La tarea de %s falló", nombre)
	}
	// NewRealtimeEvent solo falla con tipo/severidad/mensaje inválidos, y acá son constantes.
	evento, _ := ports.NewRealtimeEvent(ports.EventoTareaFinalizada, severidad, mensaje)
	return evento.ConRecurso(recursoTipo, tarea.InstanciaID).ConDetalles(r.Detalles(tarea))
}
