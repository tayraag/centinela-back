package main

import (
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"regexp"
)

// snapshot es un punto de restauración de una instancia.
type snapshot struct {
	Nombre      string
	Descripcion string
	SnapTime    int64
	VMState     int    // qemu: 1 si se guardó también la RAM
	Padre       string // snapshot anterior en la cadena
	cores       int    // config guardada para el rollback
	memoriaMB   int
}

// Proxmox exige que el nombre empiece con letra y use solo letras, números, - y _.
var nombreSnapshotValido = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_\-]{1,39}$`)

// GET /nodes/{node}/{tipo}/{vmid}/snapshot
// Como en las capturas: cada snapshot + la entrada "current" ("You are here!").
func (s *Simulador) listarSnapshots(w http.ResponseWriter, r *http.Request) {
	inst := s.buscarInstancia(w, r)
	if inst == nil {
		return
	}
	defer s.mu.Unlock()

	lista := []map[string]any{}
	for _, snap := range inst.Snapshots {
		e := map[string]any{"name": snap.Nombre, "description": snap.Descripcion, "snaptime": snap.SnapTime}
		if inst.Tipo == tipoQemu {
			e["vmstate"] = snap.VMState
		}
		if snap.Padre != "" {
			e["parent"] = snap.Padre
		}
		lista = append(lista, e)
	}
	actual := map[string]any{
		"name":        "current",
		"description": "You are here!",
		"running":     map[bool]int{true: 1, false: 0}[inst.encendida()],
		"digest":      inst.configCompleta(false)["digest"],
	}
	if inst.Actual != "" {
		actual["parent"] = inst.Actual
	}
	responder(w, append(lista, actual))
}

// POST /nodes/{node}/{tipo}/{vmid}/snapshot  (form: snapname, description, vmstate)
func (s *Simulador) crearSnapshot(w http.ResponseWriter, r *http.Request) {
	params, ok := leerParametros(w, r)
	if !ok {
		return
	}
	nombre := params["snapname"]
	switch {
	case nombre == "":
		errorParametros(w, map[string]string{"snapname": "property is missing and it is not optional"})
		return
	case nombre == "current" || !nombreSnapshotValido.MatchString(nombre):
		errorParametros(w, map[string]string{"snapname": "invalid format - invalid configuration ID '" + nombre + "'"})
		return
	}

	inst := s.buscarInstancia(w, r)
	if inst == nil {
		return
	}
	defer s.mu.Unlock()
	if !s.verificarLibre(w, inst) {
		return
	}
	if inst.buscarSnapshot(nombre) != nil {
		errorPVE(w, http.StatusInternalServerError, fmt.Sprintf("snapshot name '%s' already used", nombre))
		return
	}

	vmstate := 0
	if params["vmstate"] == "1" && inst.Tipo == tipoQemu {
		vmstate = 1
	}
	snap := &snapshot{
		Nombre: nombre, Descripcion: params["description"], SnapTime: s.ahora().Unix(),
		VMState: vmstate, Padre: inst.Actual, cores: inst.Cores, memoriaMB: inst.MemoriaMB,
	}
	upid := s.crearTarea(prefijoTarea(inst)+"snapshot", inst, "snapshot", func() string {
		inst.Snapshots = append(inst.Snapshots, snap)
		inst.Actual = snap.Nombre
		return "OK"
	})
	responder(w, upid)
}

// POST /nodes/{node}/{tipo}/{vmid}/snapshot/{snap}/rollback
// Al terminar, la instancia vuelve a la config del snapshot y queda apagada.
func (s *Simulador) rollbackSnapshot(w http.ResponseWriter, r *http.Request) {
	inst := s.buscarInstancia(w, r)
	if inst == nil {
		return
	}
	defer s.mu.Unlock()
	if !s.verificarLibre(w, inst) {
		return
	}
	snap := inst.buscarSnapshot(r.PathValue("snap"))
	if snap == nil {
		errorPVE(w, http.StatusInternalServerError, fmt.Sprintf("snapshot '%s' does not exist", r.PathValue("snap")))
		return
	}

	upid := s.crearTarea(prefijoTarea(inst)+"rollback", inst, "rollback", func() string {
		if inst.encendida() {
			inst.Estado, inst.DetenidaEn, inst.PID = "stopped", s.ahora(), 0
		}
		inst.Cores, inst.MemoriaMB = snap.cores, snap.memoriaMB
		inst.Pendiente = map[string]string{}
		inst.Actual = snap.Nombre
		return "OK"
	})
	responder(w, upid)
}

// DELETE /nodes/{node}/{tipo}/{vmid}/snapshot/{snap}
// (No está en las capturas; imita el comportamiento documentado de Proxmox.)
func (s *Simulador) borrarSnapshot(w http.ResponseWriter, r *http.Request) {
	inst := s.buscarInstancia(w, r)
	if inst == nil {
		return
	}
	defer s.mu.Unlock()
	if !s.verificarLibre(w, inst) {
		return
	}
	snap := inst.buscarSnapshot(r.PathValue("snap"))
	if snap == nil {
		errorPVE(w, http.StatusInternalServerError, fmt.Sprintf("snapshot '%s' does not exist", r.PathValue("snap")))
		return
	}

	upid := s.crearTarea(prefijoTarea(inst)+"delsnapshot", inst, "snapshot-delete", func() string {
		restantes := inst.Snapshots[:0]
		for _, otro := range inst.Snapshots {
			if otro == snap {
				continue
			}
			if otro.Padre == snap.Nombre {
				otro.Padre = snap.Padre
			}
			restantes = append(restantes, otro)
		}
		inst.Snapshots = restantes
		if inst.Actual == snap.Nombre {
			inst.Actual = snap.Padre
		}
		return "OK"
	})
	responder(w, upid)
}

func (i *instancia) buscarSnapshot(nombre string) *snapshot {
	for _, snap := range i.Snapshots {
		if snap.Nombre == nombre {
			return snap
		}
	}
	return nil
}

func prefijoTarea(inst *instancia) string {
	if inst.Tipo == tipoLXC {
		return "vz"
	}
	return "qm"
}

// leerParametros lee el body como lo acepta Proxmox: x-www-form-urlencoded
// (lo que usan el backend y Postman) o JSON.
func leerParametros(w http.ResponseWriter, r *http.Request) (map[string]string, bool) {
	params := map[string]string{}
	if tipo, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); tipo == "application/json" {
		var crudo map[string]any
		if err := json.NewDecoder(r.Body).Decode(&crudo); err != nil {
			errorPVE(w, http.StatusBadRequest, "malformed JSON body")
			return nil, false
		}
		for k, v := range crudo {
			params[k] = fmt.Sprint(v)
		}
		return params, true
	}
	if err := r.ParseForm(); err != nil {
		errorPVE(w, http.StatusBadRequest, "malformed form body")
		return nil, false
	}
	for k := range r.PostForm {
		params[k] = r.PostForm.Get(k)
	}
	return params, true
}
