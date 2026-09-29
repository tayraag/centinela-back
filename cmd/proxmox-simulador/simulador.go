package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config es la configuración del simulador (se arma desde el .env en main.go).
type Config struct {
	Nodo          string        // nombre del nodo (PROXMOX_NODE)
	TokenID       string        // PROXMOX_TOKEN_ID, ej. "centinela-api@pve!backend-token"
	TokenSecret   string        // PROXMOX_TOKEN_SECRET
	DuracionTarea time.Duration // cuánto tarda cada tarea asíncrona (start, stop, snapshot...)
	Falla         string        // modo falla para probar errores: "", "caido", "token", "lento"
}

// Modos de falla (PROXMOX_SIM_FALLA), para probar cómo reacciona el backend:
//   - caido: todo responde 500, como un Proxmox con problemas.
//   - token: todo responde 401, como un token inválido o revocado.
//   - lento: cada respuesta tarda 15 s (más que el timeout de 10 s del backend → 504).
var fallasValidas = map[string]bool{"caido": true, "token": true, "lento": true}

// demoraModoLento es cuánto tarda cada respuesta en el modo falla "lento".
var demoraModoLento = 15 * time.Second

// Simulador implementa http.Handler imitando la API REST de Proxmox VE.
type Simulador struct {
	cfg    Config
	ahora  func() time.Time // reemplazable en los tests
	inicio time.Time

	mu         sync.Mutex
	instancias map[int]*instancia
	tareas     map[string]*tarea
	proximoPID int

	mux *http.ServeMux
}

// NuevoSimulador arma el simulador con el inventario inicial de las capturas.
func NuevoSimulador(cfg Config) *Simulador {
	s := &Simulador{
		cfg:        cfg,
		ahora:      time.Now,
		instancias: map[int]*instancia{},
		tareas:     map[string]*tarea{},
		proximoPID: 540000,
	}
	s.inicio = s.ahora()
	for _, inst := range inventarioInicial(s.inicio) {
		s.instancias[inst.Vmid] = inst
	}
	s.rutas()
	return s
}

// rutas registra los endpoints que imita el simulador (ver docs/simulador-proxmox.md).
func (s *Simulador) rutas() {
	m := http.NewServeMux()
	const base = "/api2/json"

	// Inventario y nodo (RF-02, RF-03, RF-07)
	m.HandleFunc("GET "+base+"/cluster/resources", s.clusterResources)
	m.HandleFunc("GET "+base+"/nodes/{node}/status", s.estadoNodo)
	m.HandleFunc("GET "+base+"/nodes/{node}/{tipo}", s.listarPorTipo)

	// Estado, acciones y métricas por instancia (RF-04, RF-05, RF-11)
	m.HandleFunc("GET "+base+"/nodes/{node}/{tipo}/{vmid}/status/current", s.estadoActual)
	m.HandleFunc("POST "+base+"/nodes/{node}/{tipo}/{vmid}/status/{accion}", s.accionEstado)
	m.HandleFunc("GET "+base+"/nodes/{node}/{tipo}/{vmid}/rrddata", s.rrddata)

	// Tareas asíncronas (RF-04, RF-11)
	m.HandleFunc("GET "+base+"/nodes/{node}/tasks/{upid}/status", s.estadoTarea)

	// Snapshots (RF-06)
	m.HandleFunc("GET "+base+"/nodes/{node}/{tipo}/{vmid}/snapshot", s.listarSnapshots)
	m.HandleFunc("POST "+base+"/nodes/{node}/{tipo}/{vmid}/snapshot", s.crearSnapshot)
	m.HandleFunc("POST "+base+"/nodes/{node}/{tipo}/{vmid}/snapshot/{snap}/rollback", s.rollbackSnapshot)
	m.HandleFunc("DELETE "+base+"/nodes/{node}/{tipo}/{vmid}/snapshot/{snap}", s.borrarSnapshot)

	// Creación y configuración (RF-07, RF-10)
	m.HandleFunc("POST "+base+"/nodes/{node}/{tipo}", s.crearInstancia)
	m.HandleFunc("GET "+base+"/nodes/{node}/{tipo}/{vmid}/config", s.leerConfig)
	m.HandleFunc("PUT "+base+"/nodes/{node}/{tipo}/{vmid}/config", s.editarConfig)

	// Cualquier otra ruta: Proxmox responde 501 "Method ... not implemented".
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		errorPVE(w, http.StatusNotImplemented, fmt.Sprintf("Method '%s %s' not implemented", r.Method, strings.TrimPrefix(r.URL.Path, base)))
	})
	s.mux = m
}

// ServeHTTP aplica, en este orden, el modo falla, la autenticación por API
// Token y el ruteo. Cada request loguea método, ruta y status.
func (s *Simulador) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rw := &registroStatus{ResponseWriter: w, status: http.StatusOK}
	defer func() { log.Printf("%s %s %s → %d", emojiStatus(rw.status), r.Method, r.URL.Path, rw.status) }()

	switch s.cfg.Falla {
	case "caido":
		errorPVE(rw, http.StatusInternalServerError, "simulador en modo falla 'caido'")
		return
	case "token":
		errorPVE(rw, http.StatusUnauthorized, "invalid token value!")
		return
	case "lento":
		time.Sleep(demoraModoLento)
	}

	esperado := fmt.Sprintf("PVEAPIToken=%s=%s", s.cfg.TokenID, s.cfg.TokenSecret)
	if r.Header.Get("Authorization") != esperado {
		errorPVE(rw, http.StatusUnauthorized, "invalid token value!")
		return
	}

	s.mux.ServeHTTP(rw, r)
}

// ==========================================
// Resolución de instancias desde la ruta
// ==========================================

// buscarInstancia valida {node}, {tipo} y {vmid} y devuelve la instancia con el
// lock tomado. Si algo no cuadra ya respondió el error y devuelve nil.
// Quien la llama debe hacer s.mu.Unlock() cuando la instancia no es nil.
func (s *Simulador) buscarInstancia(w http.ResponseWriter, r *http.Request) *instancia {
	if !s.validarNodoYTipo(w, r) {
		return nil
	}
	tipo := r.PathValue("tipo")
	vmid, err := strconv.Atoi(r.PathValue("vmid"))
	if err != nil {
		errorParametros(w, map[string]string{"vmid": fmt.Sprintf("type check ('integer') failed - got '%s'", r.PathValue("vmid"))})
		return nil
	}

	s.mu.Lock()
	s.finalizarTareas()
	inst := s.instancias[vmid]
	if inst == nil || inst.Tipo != tipo {
		s.mu.Unlock()
		carpeta := "qemu-server"
		if tipo == tipoLXC {
			carpeta = "lxc"
		}
		errorPVE(w, http.StatusInternalServerError, fmt.Sprintf("Configuration file 'nodes/%s/%s/%d.conf' does not exist", s.cfg.Nodo, carpeta, vmid))
		return nil
	}
	return inst
}

// validarNodoYTipo responde el error que da Proxmox si el nodo no existe o si
// {tipo} no es qemu/lxc.
func (s *Simulador) validarNodoYTipo(w http.ResponseWriter, r *http.Request) bool {
	if nodo := r.PathValue("node"); nodo != s.cfg.Nodo {
		errorPVE(w, http.StatusInternalServerError, fmt.Sprintf("hostname lookup '%s' failed - failed to get address info for: %s: Name or service not known", nodo, nodo))
		return false
	}
	if tipo := r.PathValue("tipo"); tipo != "" && tipo != tipoQemu && tipo != tipoLXC {
		errorPVE(w, http.StatusNotImplemented, fmt.Sprintf("Method '%s %s' not implemented", r.Method, strings.TrimPrefix(r.URL.Path, "/api2/json")))
		return false
	}
	return true
}

// ==========================================
// Respuestas con el formato de Proxmox
// ==========================================

// responder envuelve el payload en {"data": ...}, como toda respuesta de Proxmox.
func responder(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// errorPVE imita los errores de Proxmox: {"data": null, "message": "..."}.
func errorPVE(w http.ResponseWriter, status int, mensaje string) {
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "message": mensaje + "\n"})
}

// errorParametros imita el 400 de validación de Proxmox, con el detalle por campo.
func errorParametros(w http.ResponseWriter, errores map[string]string) {
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "errors": errores, "message": "Parameter verification failed.\n"})
}

type registroStatus struct {
	http.ResponseWriter
	status int
}

func (r *registroStatus) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func emojiStatus(status int) string {
	switch {
	case status < 300:
		return "✅"
	case status == http.StatusUnauthorized:
		return "🔑"
	case status < 500:
		return "⚠️ "
	default:
		return "❌"
	}
}
