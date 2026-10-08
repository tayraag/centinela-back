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
	claveVuelo         = "estado-nodo"
)

// nodoService implementa ports.NodoService.
type nodoService struct {
	proxmox ports.ProxmoxPort
	kv      ports.KeyValueStore
	nodo    string
	ahora   func() time.Time

	vuelo singleflight.Group // pedidos simultáneos comparten una sola consulta a Proxmox

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
