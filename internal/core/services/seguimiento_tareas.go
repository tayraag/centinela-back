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

// limiteSeguimiento: si Proxmox no da por terminada la tarea en este tiempo
// (contado desde que se creó), se marca como fallida.
const limiteSeguimiento = 10 * time.Minute

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
//   - COMPLETED: exitStatus "OK", sin motivo ni error.
//   - FAILED por Proxmox: motivo PROXMOX_ERROR; exitStatus y error, el texto de Proxmox.
//   - FAILED por tiempo: motivo TIMEOUT, sin exitStatus (Proxmox no la cerró).
type resultadoTarea struct {
	estado     string
	exitStatus string // "" = null
	motivo     string // "" = null
	error      string // "" = null
}

func completada() resultadoTarea {
	return resultadoTarea{estado: ports.TareaCompleted, exitStatus: "OK"}
}

func falloProxmox(exitStatus string) resultadoTarea {
	return resultadoTarea{estado: ports.TareaFailed, exitStatus: exitStatus, motivo: ports.MotivoProxmoxError, error: exitStatus}
}

func vencida(limite time.Duration) resultadoTarea {
	return resultadoTarea{estado: ports.TareaFailed, motivo: ports.MotivoTimeout, error: mensajeTimeout(limite)}
}

// metadatos es lo que se guarda en tareas_asincronas.metadatos: {motivo, error}
// en una tarea FAILED, nada en una COMPLETED.
func (r resultadoTarea) metadatos() map[string]any {
	if r.estado != ports.TareaFailed {
		return nil
	}
	return map[string]any{"motivo": r.motivo, "error": r.error}
}

// detalles arma detalles.{tareaId, accion, estado, exitstatus, motivo, error}
// del TASK_FINISHED (y de la auditoría): siempre las seis claves, con null
// cuando no aplican. accion va en mayúsculas, igual que en la auditoría.
func (r resultadoTarea) detalles(tarea *domain.TareaAsincrona) map[string]any {
	return map[string]any{
		"tareaId": tarea.ID.String(), "accion": strings.ToUpper(tarea.Accion), "estado": r.estado,
		"exitstatus": nulo(r.exitStatus), "motivo": nulo(r.motivo), "error": nulo(r.error),
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
	return c
}

// TareaSeguimiento es una tarea dentro del pool.
type TareaSeguimiento struct {
	Tarea       domain.TareaAsincrona
	Vmid        int
	recursoTipo string // VM | LXC, se resuelve en la primera consulta
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
	eventos ports.EventosService
	audit   ports.AuditService
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
func NewSeguimientoTareas(proxmox ports.ProxmoxPort, tareas ports.TareaRepository, eventos ports.EventosService, audit ports.AuditService, cfg ConfigSeguimiento) *PoolSeguimiento {
	cfg = cfg.conDefaults()
	return &PoolSeguimiento{
		proxmox: proxmox, tareas: tareas, eventos: eventos, audit: audit, cfg: cfg, ahora: time.Now,
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
	case err == nil && resultado.Terminada && resultado.ExitStatus == "OK":
		p.finalizar(t, completada())
	case err == nil && resultado.Terminada:
		p.finalizar(t, falloProxmox(resultado.ExitStatus))
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
// límite) la cierra; si no, la reprograma para dentro de Intervalo.
func (p *PoolSeguimiento) consultar(t *TareaSeguimiento) {
	ctx, cancel := context.WithTimeout(context.Background(), timeoutConsulta)
	defer cancel()

	p.resolverRecurso(ctx, t)

	resultado, err := p.proxmox.EstadoTarea(ctx, t.Tarea.UpidProxmox)
	switch {
	case err == nil && resultado.Terminada && resultado.ExitStatus == "OK":
		p.finalizar(t, completada())
		return
	case err == nil && resultado.Terminada:
		p.finalizar(t, falloProxmox(resultado.ExitStatus))
		return
	case err != nil:
		log.Printf("[TAREAS] no se pudo consultar la tarea %s (se reintenta): %v", t.Tarea.ID, err)
	}

	if p.ahora().Sub(t.Tarea.FechaCreacion) > limiteSeguimiento {
		p.finalizar(t, vencida(limiteSeguimiento))
		return
	}
	// Reprogramar sin ocupar un worker mientras espera.
	time.AfterFunc(p.cfg.Intervalo, func() {
		if !p.encolar(t) {
			// Cola llena o apagándose: queda RUNNING y la retoma el reconciliador.
			log.Printf("[TAREAS] la tarea %s vuelve al reconciliador", t.Tarea.ID)
		}
	})
}

// finalizar guarda el estado final, audita y publica TASK_FINISHED.
func (p *PoolSeguimiento) finalizar(t *TareaSeguimiento, r resultadoTarea) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer func() {
		p.mu.Lock()
		delete(p.enCurso, t.Tarea.ID)
		p.mu.Unlock()
	}()

	if err := p.tareas.ActualizarEstado(ctx, t.Tarea.ID, r.estado, r.metadatos()); err != nil {
		log.Printf("[TAREAS] %v", err)
	}

	resultado := ports.ResultadoExito
	if r.estado != ports.TareaCompleted {
		resultado = ports.ResultadoFalla
	}
	// Mismo código de acción que el registro PENDING del handler (START, STOP, DELETE, ...).
	p.audit.Registrar(ctx, ports.RegistrarAuditoriaInput{
		UsuarioID: t.Tarea.UsuarioID, Accion: strings.ToUpper(t.Tarea.Accion),
		InstanciaID: t.Tarea.InstanciaID, Resultado: resultado, Detalles: r.detalles(&t.Tarea),
	})

	log.Printf("[TAREAS] tarea %s (%s sobre %d) terminó: %s", t.Tarea.ID, t.Tarea.Accion, t.Vmid, r.estado)
	if err := p.eventos.Publicar(ctx, eventoTareaFinalizada(&t.Tarea, t.recursoTipo, r)); err != nil {
		log.Printf("[TAREAS] no se pudo publicar TASK_FINISHED de la tarea %s: %v", t.Tarea.ID, err)
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

// eventoTareaFinalizada arma el TASK_FINISHED de docs/contrato-eventos.md.
func eventoTareaFinalizada(tarea *domain.TareaAsincrona, recursoTipo string, r resultadoTarea) ports.RealtimeEvent {
	nombre := nombresAccion[tarea.Accion]
	if nombre == "" {
		nombre = tarea.Accion
	}
	if recursoTipo == "" {
		recursoTipo = ports.RecursoVM
	}
	severidad, mensaje := ports.SeveridadInfo, fmt.Sprintf("La tarea de %s finalizó correctamente", nombre)
	if r.estado != ports.TareaCompleted {
		severidad, mensaje = ports.SeveridadWarning, fmt.Sprintf("La tarea de %s falló", nombre)
	}
	// NewRealtimeEvent solo falla con tipo/severidad/mensaje inválidos, y acá son constantes.
	evento, _ := ports.NewRealtimeEvent(ports.EventoTareaFinalizada, severidad, mensaje)
	return evento.ConRecurso(recursoTipo, tarea.InstanciaID).ConDetalles(r.detalles(tarea))
}
