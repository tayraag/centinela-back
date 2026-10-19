package services

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"sync"
	"time"

	"el-centinela/internal/core/ports"

	"golang.org/x/sync/singleflight"
)

// Claves de caché del estado del nodo en el almacén efímero (Redis):
//   - node:status:current     → lectura vigente, con TTL (ttlEstadoNodo)
//   - node:status:last_known  → última lectura buena, SIN TTL (respaldo stale)
const (
	claveNodoActual    = "node:status:current"
	claveNodoUltimo    = "node:status:last_known"
	ttlEstadoNodo      = 10 * time.Second
	pausaTrasFallaNodo = 5 * time.Second // después de una falla, no reintentar Proxmox en cada request
	bytesPorGB         = 1024 * 1024 * 1024
	bytesPorMB         = 1024 * 1024
	headroomRAMPercent = 15
	claveVuelo         = "estado-nodo"

	// Histórico del host para el gráfico del Dashboard. Mismo esquema de dos
	// claves que el estado: la vigente con TTL y un respaldo sin TTL, para poder
	// servir la última serie conocida si Proxmox no responde.
	claveNodoHistorial       = "node:status:history"
	claveNodoHistorialUltimo = "node:status:history:last_known"
	ttlHistorialNodo         = 60 * time.Second
	claveVueloHistorial      = "historial-nodo"

	// Proxmox no tiene un timeframe de 15 minutos: "hour" trae 60 puntos, uno por
	// minuto, que es la resolución nativa del RRD. El frontend completa lo reciente
	// con el sondeo de 10 s.
	timeframeHistorialNodo = "hour"
)

// nodoService implementa ports.NodoService.
type nodoService struct {
	proxmox ports.ProxmoxPort
	kv      ports.KeyValueStore
	nodo    string
	ahora   func() time.Time

	vuelo singleflight.Group // pedidos simultáneos comparten una sola consulta a Proxmox

	// El histórico va en su propio grupo: es una consulta distinta y más lenta,
	// y no tiene por qué bloquear la lectura del estado.
	vueloHistorial singleflight.Group

	mu          sync.Mutex
	ultimaFalla time.Time
	errFalla    error
}

// NewNodoService crea el servicio de estado del nodo (nodo = PROXMOX_NODE).
func NewNodoService(proxmox ports.ProxmoxPort, kv ports.KeyValueStore, nodo string) ports.NodoService {
	return NewNodoServiceConReloj(proxmox, kv, nodo, time.Now)
}

// NewNodoServiceConReloj permite inyectar el reloj (tests).
func NewNodoServiceConReloj(proxmox ports.ProxmoxPort, kv ports.KeyValueStore, nodo string, ahora func() time.Time) ports.NodoService {
	return &nodoService{proxmox: proxmox, kv: kv, nodo: nodo, ahora: ahora}
}

// ObtenerEstado:
//  1. node:status:current vigente → se devuelve (stale = false), sin tocar Proxmox.
//  2. Verificar respaldo previo en caché (node:status:last_known).
//  3. Si hay respaldo y hubo falla reciente → devolver respaldo stale (stale = true).
//  4. Consultar a Proxmox; si responde, actualizar caché y limpiar fallas.
//  5. Si falla Proxmox, devolver respaldo stale si existe, o el error.
func (s *nodoService) ObtenerEstado(ctx context.Context) (*ports.EstadoNodo, bool, error) {
	if estado, ok := s.leer(ctx, claveNodoActual); ok {
		return estado, false, nil
	}

	// Verificar si hay caché previa antes de aplicar heurística de falla
	estadoRespaldo, hayRespaldo := s.leer(ctx, claveNodoUltimo)

	// Si hay respaldo, y Proxmox acaba de fallar (cooldown activo), devolvemos stale
	if hayRespaldo {
		if err := s.fallaReciente(); err != nil {
			return estadoRespaldo, true, nil
		}
	}

	resultado, err, _ := s.vuelo.Do(claveVuelo, func() (any, error) {
		// Otro pedido pudo haber renovado la caché mientras este esperaba.
		if estado, ok := s.leer(ctx, claveNodoActual); ok {
			return estado, nil
		}
		return s.refrescar(ctx)
	})
	if err != nil {
		s.registrarFalla(err)
		if hayRespaldo {
			return estadoRespaldo, true, nil
		}
		return nil, false, err
	}
	return resultado.(*ports.EstadoNodo), false, nil
}

// refrescar lee Proxmox (estado del nodo + inventario para el resumen), normaliza
// y guarda las dos claves. Usa un contexto propio: si el request que lo disparó se
// cancela, la lectura compartida por los demás pedidos no se corta.
func (s *nodoService) refrescar(ctx context.Context) (*ports.EstadoNodo, error) {
	ctxProxmox := context.WithoutCancel(ctx)
	crudo, err := s.proxmox.ObtenerEstadoNodo(ctxProxmox, s.nodo)
	if err != nil {
		return nil, err
	}
	instancias, err := s.proxmox.ListarInstancias(ctxProxmox)
	if err != nil {
		return nil, err
	}

	estado := normalizarEstadoNodo(crudo, instancias, s.ahora())
	if err := s.kv.Set(ctxProxmox, claveNodoActual, estado, ttlEstadoNodo); err != nil {
		log.Printf("[NODO] no se pudo guardar %s: %v", claveNodoActual, err)
	}
	if err := s.kv.Set(ctxProxmox, claveNodoUltimo, estado, 0); err != nil {
		log.Printf("[NODO] no se pudo guardar %s: %v", claveNodoUltimo, err)
	}
	s.mu.Lock()
	s.ultimaFalla, s.errFalla = time.Time{}, nil
	s.mu.Unlock()
	return estado, nil
}

func (s *nodoService) leer(ctx context.Context, clave string) (*ports.EstadoNodo, bool) {
	valor, err := s.kv.Get(ctx, clave)
	if err != nil {
		if !errors.Is(err, ports.ErrClaveNoEncontrada) {
			log.Printf("[NODO] no se pudo leer %s: %v", clave, err)
		}
		return nil, false
	}
	var estado ports.EstadoNodo
	if err := json.Unmarshal([]byte(valor), &estado); err != nil {
		log.Printf("[NODO] %s con formato inválido: %v", clave, err)
		return nil, false
	}
	return &estado, true
}

func (s *nodoService) registrarFalla(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ultimaFalla, s.errFalla = s.ahora(), err
	log.Printf("[NODO] Proxmox no respondió el estado del nodo (se sirve la última lectura si existe): %v", err)
}

func (s *nodoService) fallaReciente() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.errFalla != nil && s.ahora().Sub(s.ultimaFalla) < pausaTrasFallaNodo {
		return s.errFalla
	}
	return nil
}

// normalizarEstadoNodo convierte la lectura cruda: CPU a porcentaje, bytes a GB
// (1024³) con 2 decimales y porcentajes de uso. El resumen cuenta las instancias
// de todo el cluster (el estado del nodo no se filtra por permisos).
func normalizarEstadoNodo(crudo *ports.NodeStatusDTO, instancias []ports.InstanciaProxmoxDTO, ahora time.Time) *ports.EstadoNodo {
	estado := &ports.EstadoNodo{
		CPU:           ports.MetricaCPU{UsagePercent: redondear(crudo.CPU * 100), Cores: crudo.CPUs},
		RAM:           capacidad(crudo.MemUsada, crudo.MemTotal),
		Storage:       capacidad(crudo.DiscoUsado, crudo.DiscoTotal),
		Allocatable:   calcularCuotaAsignable(crudo),
		UptimeSeconds: crudo.UptimeSegs,
		FetchedAt:     ahora.UTC().Truncate(time.Second),
	}
	for _, inst := range instancias {
		resumen := &estado.InstancesSummary.VMs
		if inst.Tipo == ports.TipoInstanciaLXC {
			resumen = &estado.InstancesSummary.LXC
		}
		switch inst.Estado {
		case "running":
			resumen.Running++
		case "stopped":
			resumen.Stopped++
		case "paused":
			resumen.Paused++
		}
		resumen.Total++
	}
	return estado
}

// calcularCuotaAsignable reserva al menos el 15% de la RAM física para
// Proxmox. La reserva se redondea hacia arriba al byte y las cuotas públicas
// hacia abajo a MiB/GiB, para no anunciar capacidad que no existe.
func calcularCuotaAsignable(crudo *ports.NodeStatusDTO) ports.CuotaAsignable {
	totalRAM := max(int64(0), crudo.MemTotal)
	usadaRAM := max(int64(0), crudo.MemUsada)
	reservadaRAM := porcentajeRedondeadoHaciaArriba(totalRAM, headroomRAMPercent)
	libreSeguraRAM := max(int64(0), totalRAM-usadaRAM-reservadaRAM)
	storageDisponible := max(int64(0), crudo.DiscoDisponible)

	return ports.CuotaAsignable{
		MaxCPUCores:     max(0, crudo.PhysicalCores),
		FreeRamMB:       libreSeguraRAM / bytesPorMB,
		FreeStorageGB:   storageDisponible / bytesPorGB,
		HeadroomPercent: headroomRAMPercent,
	}
}

func porcentajeRedondeadoHaciaArriba(total int64, porcentaje int64) int64 {
	if total <= 0 || porcentaje <= 0 {
		return 0
	}
	// Separar cociente y resto evita desbordar total*porcentaje.
	return (total/100)*porcentaje + (total%100*porcentaje+99)/100
}

func capacidad(usado, total int64) ports.MetricaCapacidad {
	m := ports.MetricaCapacidad{
		UsedGb:  redondear(float64(usado) / bytesPorGB),
		TotalGb: redondear(float64(total) / bytesPorGB),
	}
	if total > 0 {
		m.UsagePercent = redondear(float64(usado) / float64(total) * 100)
	}
	return m
}

func redondear(v float64) float64 { return math.Round(v*100) / 100 }

// ObtenerHistorial devuelve la serie histórica del host para el gráfico del
// Dashboard. La fuente es el RRD de Proxmox y el resultado se cachea en Redis:
// así el gráfico ya está formado al entrar en vez de arrancar de cero, y Proxmox
// recibe como mucho una consulta por minuto en lugar de una por visita.
func (s *nodoService) ObtenerHistorial(ctx context.Context) ([]ports.PuntoHistorialNodo, error) {
	if serie, ok := s.leerHistorial(ctx, claveNodoHistorial); ok {
		return serie, nil
	}

	resultado, err, _ := s.vueloHistorial.Do(claveVueloHistorial, func() (any, error) {
		// Otro pedido pudo renovar la caché mientras este esperaba.
		if serie, ok := s.leerHistorial(ctx, claveNodoHistorial); ok {
			return serie, nil
		}
		return s.refrescarHistorial(ctx)
	})
	if err != nil {
		if serie, ok := s.leerHistorial(ctx, claveNodoHistorialUltimo); ok {
			log.Printf("[NODO] Proxmox no respondió el histórico (se sirve la última serie): %v", err)
			return serie, nil
		}
		return nil, err
	}
	return resultado.([]ports.PuntoHistorialNodo), nil
}

// refrescarHistorial lee el RRD del nodo y guarda la serie normalizada, con el
// mismo esquema de dos claves que el estado del nodo.
func (s *nodoService) refrescarHistorial(ctx context.Context) ([]ports.PuntoHistorialNodo, error) {
	ctxProxmox := context.WithoutCancel(ctx)
	muestras, err := s.proxmox.ObtenerMetricasNodoRRD(ctxProxmox, s.nodo, timeframeHistorialNodo)
	if err != nil {
		return nil, err
	}
	serie := normalizarHistorialNodo(muestras)
	if err := s.kv.Set(ctxProxmox, claveNodoHistorial, serie, ttlHistorialNodo); err != nil {
		log.Printf("[NODO] no se pudo guardar %s: %v", claveNodoHistorial, err)
	}
	if err := s.kv.Set(ctxProxmox, claveNodoHistorialUltimo, serie, 0); err != nil {
		log.Printf("[NODO] no se pudo guardar %s: %v", claveNodoHistorialUltimo, err)
	}
	return serie, nil
}

func (s *nodoService) leerHistorial(ctx context.Context, clave string) ([]ports.PuntoHistorialNodo, bool) {
	valor, err := s.kv.Get(ctx, clave)
	if err != nil {
		if !errors.Is(err, ports.ErrClaveNoEncontrada) {
			log.Printf("[NODO] no se pudo leer %s: %v", clave, err)
		}
		return nil, false
	}
	var serie []ports.PuntoHistorialNodo
	if err := json.Unmarshal([]byte(valor), &serie); err != nil {
		log.Printf("[NODO] %s con formato inválido: %v", clave, err)
		return nil, false
	}
	if len(serie) == 0 {
		return nil, false
	}
	return serie, true
}

// normalizarHistorialNodo pasa la muestra cruda a los tres porcentajes que
// dibuja el gráfico, acotados a 0–100 para que un valor raro de Proxmox no
// rompa la escala.
func normalizarHistorialNodo(muestras []ports.MuestraNodoRRD) []ports.PuntoHistorialNodo {
	serie := make([]ports.PuntoHistorialNodo, 0, len(muestras))
	for _, m := range muestras {
		punto := ports.PuntoHistorialNodo{
			Timestamp:  m.Timestamp,
			CPUPercent: porcentajeAcotado(m.CPU * 100),
		}
		if m.MemTotal > 0 {
			punto.RAMPercent = porcentajeAcotado(float64(m.MemUsada) / float64(m.MemTotal) * 100)
		}
		if m.DiscoTotal > 0 {
			punto.StoragePercent = porcentajeAcotado(float64(m.DiscoUsado) / float64(m.DiscoTotal) * 100)
		}
		serie = append(serie, punto)
	}
	return serie
}

// porcentajeAcotado redondea a 2 decimales y recorta a 0–100.
func porcentajeAcotado(v float64) float64 {
	return redondear(math.Max(0, math.Min(100, v)))
}
