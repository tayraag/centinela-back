package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// tarea es una operación asíncrona de Proxmox (start, stop, snapshot, create...).
// Proxmox responde enseguida con el UPID y la operación termina después; el
// cliente consulta GET /nodes/{node}/tasks/{upid}/status hasta ver "stopped".
type tarea struct {
	UPID      string
	Tipo      string // qmstart, vzstop, qmsnapshot, hastart...
	ID        string // vmid como string
	Vmid      int
	PID       int
	PStart    int64
	Inicio    time.Time
	Fin       time.Time
	Terminada bool
	Resultado string // "OK" o el mensaje de error, como el exitstatus de Proxmox

	// alTerminar aplica el efecto de la tarea (encender, crear el snapshot...).
	// Devuelve "OK" o el mensaje de error que queda en exitstatus.
	alTerminar func() string
}

// crearTarea registra una tarea nueva y devuelve su UPID. Si lock no está
// vacío, la config de la instancia queda bloqueada hasta que termine.
// Debe llamarse con s.mu tomado.
func (s *Simulador) crearTarea(tipo string, inst *instancia, lock string, alTerminar func() string) string {
	ahora := s.ahora()
	s.proximoPID += 17
	t := &tarea{
		Tipo:       tipo,
		ID:         fmt.Sprint(inst.Vmid),
		Vmid:       inst.Vmid,
		PID:        s.proximoPID,
		PStart:     int64(ahora.Sub(s.inicio)/(10*time.Millisecond)) + 20000000,
		Inicio:     ahora,
		Fin:        ahora.Add(s.cfg.DuracionTarea),
		alTerminar: alTerminar,
	}
	// Mismo formato que las capturas: UPID:pve:0008380E:0131F1BF:6A84EA54:vzreboot:101:root@pam!pruebalucas:
	t.UPID = fmt.Sprintf("UPID:%s:%08X:%08X:%08X:%s:%s:%s:", s.cfg.Nodo, t.PID, t.PStart, ahora.Unix(), t.Tipo, t.ID, s.cfg.TokenID)
	s.tareas[t.UPID] = t
	if lock != "" {
		inst.Lock = lock
	}
	if s.cfg.DuracionTarea == 0 {
		s.finalizarTareas()
	}
	return t.UPID
}

// finalizarTareas aplica el efecto de las tareas cuyo tiempo ya pasó. Se llama
// al principio de cada request, así no hacen falta goroutines ni timers.
// Debe llamarse con s.mu tomado.
func (s *Simulador) finalizarTareas() {
	ahora := s.ahora()
	for _, t := range s.tareas {
		if t.Terminada || ahora.Before(t.Fin) {
			continue
		}
		t.Terminada = true
		t.Resultado = t.alTerminar()
		if inst := s.instancias[t.Vmid]; inst != nil && !s.tareaActiva(inst.Vmid) {
			inst.Lock = ""
		}
	}
}

// tareaActiva indica si la instancia tiene alguna tarea sin terminar.
func (s *Simulador) tareaActiva(vmid int) bool {
	for _, t := range s.tareas {
		if t.Vmid == vmid && !t.Terminada {
			return true
		}
	}
	return false
}

// verificarLibre responde el error de Proxmox si la instancia está ocupada por
// otra tarea. Devuelve false si respondió el error.
func (s *Simulador) verificarLibre(w http.ResponseWriter, inst *instancia) bool {
	if inst.Lock != "" {
		errorPVE(w, http.StatusInternalServerError, fmt.Sprintf("%s is locked (%s)", inst.etiqueta(), inst.Lock))
		return false
	}
	if s.tareaActiva(inst.Vmid) {
		carpeta := "qemu-server"
		if inst.Tipo == tipoLXC {
			carpeta = "lxc"
		}
		errorPVE(w, http.StatusInternalServerError, fmt.Sprintf("can't lock file '/var/lock/%s/lock-%d.conf' - got timeout", carpeta, inst.Vmid))
		return false
	}
	return true
}

// GET /nodes/{node}/tasks/{upid}/status
func (s *Simulador) estadoTarea(w http.ResponseWriter, r *http.Request) {
	if !s.validarNodoYTipo(w, r) {
		return
	}
	upid := r.PathValue("upid")
	if !strings.HasPrefix(upid, "UPID:") || !strings.HasSuffix(upid, ":") {
		// Ver nota de la captura RF-04: el ":" final es parte obligatoria del UPID.
		errorParametros(w, map[string]string{"upid": "unable to parse worker upid '" + upid + "'"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.finalizarTareas()
	t := s.tareas[upid]
	if t == nil {
		errorPVE(w, http.StatusInternalServerError, "no such task")
		return
	}

	usuario, token, _ := strings.Cut(s.cfg.TokenID, "!")
	resp := map[string]any{
		"upid":      t.UPID,
		"type":      t.Tipo,
		"id":        t.ID,
		"node":      s.cfg.Nodo,
		"pid":       t.PID,
		"pstart":    t.PStart,
		"starttime": t.Inicio.Unix(),
		"user":      usuario,
		"tokenid":   token,
		"status":    "running",
	}
	if t.Terminada {
		resp["status"] = "stopped"
		resp["exitstatus"] = t.Resultado
	}
	responder(w, resp)
}
