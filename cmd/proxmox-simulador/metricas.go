package main

import (
	"math"
	"net/http"
	"time"
)

// ruido devuelve un valor "aleatorio" entre 0 y 1 que siempre es el mismo para
// la misma instancia y el mismo segundo: las métricas varían de forma creíble
// pero son reproducibles (útil para tests y para comparar pantallas).
func ruido(vmid int, t int64) float64 {
	x := math.Sin(float64(vmid)*12.9898+float64(t)*78.233) * 43758.5453
	return x - math.Floor(x)
}

// onda suaviza el ruido: una oscilación lenta (período ~10 min) más un poco de ruido.
func onda(vmid int, t int64) float64 {
	return 0.5 + 0.35*math.Sin(float64(t)/95+float64(vmid)) + 0.15*(ruido(vmid, t)-0.5)
}

// metricas son los valores "de ahora" que aparecen en cluster/resources y status/current.
type metricas struct {
	cpu                                float64
	mem, memhost, disk                 int64
	netin, netout, diskread, diskwrite int64
}

// metricasActuales calcula las métricas de una instancia en un instante.
// Apagada: todo en 0 salvo el disco usado de los lxc (igual que en las capturas).
// Los contadores de red y disco crecen con el uptime, como en Proxmox.
func (s *Simulador) metricasActuales(inst *instancia, ahora time.Time) metricas {
	m := metricas{}
	if inst.Tipo == tipoLXC {
		m.disk = inst.DiscoUso
	}
	if !inst.encendida() {
		return m
	}
	t := ahora.Unix()
	up := inst.uptime(ahora)
	m.cpu = 0.004 + 0.03*onda(inst.Vmid, t)
	m.mem = int64(float64(inst.MemUso) * (0.9 + 0.2*onda(inst.Vmid+7, t)))
	if inst.Tipo == tipoQemu {
		m.memhost = int64(float64(m.mem) * 1.52)
	}
	m.netin = 260 + up*330
	m.netout = up * 11
	m.diskread = 89780350 + up*1200
	m.diskwrite = up * 160000
	return m
}

// ==========================================
// GET /nodes/{node}/{tipo}/{vmid}/rrddata?timeframe=... (RF-05)
// ==========================================

// pasoPorTimeframe: 60 puntos por consulta, como en las capturas (hour = 1 punto por minuto).
var pasoPorTimeframe = map[string]int64{
	"hour":  60,
	"day":   24 * 60 * 60 / 60,
	"week":  7 * 24 * 60 * 60 / 60,
	"month": 30 * 24 * 60 * 60 / 60,
	"year":  365 * 24 * 60 * 60 / 60,
}

const puntosRRD = 60

func (s *Simulador) rrddata(w http.ResponseWriter, r *http.Request) {
	timeframe := r.URL.Query().Get("timeframe")
	if timeframe == "" {
		errorParametros(w, map[string]string{"timeframe": "property is missing and it is not optional"})
		return
	}
	paso, ok := pasoPorTimeframe[timeframe]
	if !ok {
		errorParametros(w, map[string]string{"timeframe": "value '" + timeframe + "' does not have a value in the enumeration 'hour, day, week, month, year'"})
		return
	}
	if cf := r.URL.Query().Get("cf"); cf != "" && cf != "AVERAGE" && cf != "MAX" {
		errorParametros(w, map[string]string{"cf": "value '" + cf + "' does not have a value in the enumeration 'AVERAGE, MAX'"})
		return
	}

	inst := s.buscarInstancia(w, r)
	if inst == nil {
		return
	}
	defer s.mu.Unlock()

	ahora := s.ahora().Unix()
	ultimo := ahora - ahora%paso
	puntos := make([]map[string]any, 0, puntosRRD)
	for i := int64(puntosRRD - 1); i >= 0; i-- {
		t := ultimo - i*paso
		puntos = append(puntos, s.puntoRRD(inst, t))
	}
	responder(w, puntos)
}

// puntoRRD arma un punto con los campos exactos de las capturas: si la
// instancia estaba encendida en ese momento trae todos los valores; si estaba
// apagada, solo time, maxcpu, maxmem, maxdisk y disk.
func (s *Simulador) puntoRRD(inst *instancia, t int64) map[string]any {
	p := map[string]any{
		"time":    t,
		"maxcpu":  inst.Cores,
		"maxmem":  int64(inst.MemoriaMB) * mib,
		"maxdisk": inst.MaxDisk,
		"disk":    int64(0),
	}
	if inst.Tipo == tipoLXC {
		p["disk"] = inst.DiscoUso
	}
	if !s.estabaEncendida(inst, t) {
		return p
	}

	p["cpu"] = 0.004 + 0.03*onda(inst.Vmid, t)
	p["mem"] = float64(inst.MemUso) * (0.9 + 0.2*onda(inst.Vmid+7, t))
	// En rrddata red y disco son tasas (bytes/s), no contadores.
	p["netin"] = math.Round(ruido(inst.Vmid+1, t)*12*100) / 100
	p["netout"] = math.Round(ruido(inst.Vmid+2, t)*1.5*100) / 100
	p["diskread"] = 0
	p["diskwrite"] = 0
	if ruido(inst.Vmid+3, t) > 0.7 {
		p["diskwrite"] = 273.066666666667
	}
	for _, campo := range []string{"pressurecpusome", "pressurecpufull", "pressureiosome", "pressureiofull", "pressurememorysome", "pressurememoryfull"} {
		p[campo] = 0
	}
	if inst.Tipo == tipoQemu {
		p["memhost"] = p["mem"].(float64) * 1.52
	}
	return p
}

// estabaEncendida estima si la instancia estaba encendida en el instante t:
// el simulador solo recuerda el último encendido y el último apagado.
func (s *Simulador) estabaEncendida(inst *instancia, t int64) bool {
	if inst.encendida() {
		return t >= inst.Iniciada.Unix()
	}
	return !inst.Iniciada.IsZero() && t >= inst.Iniciada.Unix() && t <= inst.DetenidaEn.Unix()
}

// ==========================================
// GET /nodes/{node}/rrddata?timeframe=... (nodo)
// ==========================================

// discoUsadoNodoEn es el rootfs del nodo en el instante t. Antes era una
// constante hardcodeada, y por eso el gráfico de almacenamiento del Dashboard
// no podía moverse nunca: la serie era siempre el mismo valor. Ahora deriva
// lento (como los logs y temporales de un host real) y suma el disco de las
// instancias que estaban encendidas en ese momento.
func (s *Simulador) discoUsadoNodoEn(t int64) int64 {
	const base = int64(5477761024)
	deriva := int64(50 * 1024 * 1024 * onda(99, t)) // hasta ~50 MB
	var deInstancias int64
	for _, inst := range s.instancias {
		if s.estabaEncendida(inst, t) {
			deInstancias += inst.DiscoUso / 20
		}
	}
	return base + deriva + deInstancias
}

// resumenNodoEn evalúa el resumen del nodo en un instante cualquiera, para
// poder reconstruir la serie histórica. La lectura "de ahora" del endpoint de
// estado sale de acá, así que el valor vivo y el último punto del histórico
// siempre coinciden.
func (s *Simulador) resumenNodoEn(t int64) (cpu float64, memUsada, discoUsado int64) {
	memUsada = int64(2339323904)
	cpu = 0.004 + 0.003*ruido(0, t)
	for _, inst := range s.instancias {
		if !s.estabaEncendida(inst, t) {
			continue
		}
		cpu += (0.004 + 0.03*onda(inst.Vmid, t)) * float64(inst.Cores) / 12
		memUsada += int64(float64(inst.MemUso) * (0.9 + 0.2*onda(inst.Vmid+7, t)))
	}
	discoUsado = s.discoUsadoNodoEn(t)
	return cpu, memUsada, discoUsado
}

// rrddataNodo responde el histórico del nodo con los mismos campos que Proxmox
// VE en /nodes/{node}/rrddata. Es la fuente del backfill que hace que el
// gráfico del Dashboard ya esté formado al entrar, en vez de arrancar de cero.
func (s *Simulador) rrddataNodo(w http.ResponseWriter, r *http.Request) {
	timeframe := r.URL.Query().Get("timeframe")
	if timeframe == "" {
		errorParametros(w, map[string]string{"timeframe": "property is missing and it is not optional"})
		return
	}
	paso, ok := pasoPorTimeframe[timeframe]
	if !ok {
		errorParametros(w, map[string]string{"timeframe": "value '" + timeframe + "' does not have a value in the enumeration 'hour, day, week, month, year'"})
		return
	}
	if cf := r.URL.Query().Get("cf"); cf != "" && cf != "AVERAGE" && cf != "MAX" {
		errorParametros(w, map[string]string{"cf": "value '" + cf + "' does not have a value in the enumeration 'AVERAGE, MAX'"})
		return
	}
	if !s.validarNodoYTipo(w, r) {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	ahora := s.ahora().Unix()
	ultimo := ahora - ahora%paso
	puntos := make([]map[string]any, 0, puntosRRD)
	for i := int64(puntosRRD - 1); i >= 0; i-- {
		puntos = append(puntos, s.puntoRRDNodo(ultimo-i*paso))
	}
	responder(w, puntos)
}

// puntoRRDNodo arma un punto del nodo con los campos de la captura de Proxmox:
// cpu en fracción [0,1] sobre los hilos del host, memoria usada y total, y el
// rootfs del nodo.
func (s *Simulador) puntoRRDNodo(t int64) map[string]any {
	cpu, memUsada, discoUsado := s.resumenNodoEn(t)
	return map[string]any{
		"time":      t,
		"cpu":       cpu,
		"memused":   float64(memUsada),
		"memtotal":  float64(16423432192),
		"rootused":  float64(discoUsado),
		"roottotal": float64(72722055168),
		"netin":     math.Round(ruido(1, t)*12*100) / 100,
		"netout":    math.Round(ruido(2, t)*1.5*100) / 100,
		"diskread":  float64(0),
		"diskwrite": math.Round(ruido(3, t)*273*100) / 100,
		"loadavg":   math.Round(ruido(4, t)*40) / 100,
		"iowait":    math.Round(ruido(5, t)*20) / 100,
	}
}
