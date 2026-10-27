package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	adaptersHttp "el-centinela/internal/adapters/primary/http"
	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/adapters/secondary/memoria"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// snapshotsFalso valida el nombre como el servicio real y registra lo creado.
type snapshotsFalso struct {
	creados []string
	vmid    int
}

func (f *snapshotsFalso) Listar(_ context.Context, vmid int) ([]ports.SnapshotDTO, error) {
	f.vmid = vmid
	return []ports.SnapshotDTO{
		{Nombre: "snap_pre_upgrade", SnapTime: 1774358400, Descripcion: "Punto previo a upgrade", Padre: "base"},
		{Nombre: "base", SnapTime: 1774300000},
	}, nil
}

func (f *snapshotsFalso) Crear(_ context.Context, _ uuid.UUID, vmid int, nombre, _ string) (*ports.SnapshotCreado, error) {
	if !services.NombreSnapshotValido(nombre) {
		return nil, ports.ErrSnapshotNombreInvalido
	}
	if nombre == "existente" {
		return nil, ports.ErrSnapshotExistente
	}
	f.vmid = vmid
	f.creados = append(f.creados, nombre)
	return &ports.SnapshotCreado{TareaID: uuid.MustParse("0192e4a1-4f89-7e23-9a0c-0305e82c3301"), Nombre: nombre}, nil
}

func (f *snapshotsFalso) Revertir(_ context.Context, _ uuid.UUID, vmid int, nombre string) (uuid.UUID, error) {
	return f.orden(vmid, "rollback:"+nombre)
}

func (f *snapshotsFalso) Eliminar(_ context.Context, _ uuid.UUID, vmid int, nombre string) (uuid.UUID, error) {
	return f.orden(vmid, "delete:"+nombre)
}

func (f *snapshotsFalso) orden(vmid int, orden string) (uuid.UUID, error) {
	nombre := orden[strings.Index(orden, ":")+1:]
	if !services.NombreSnapshotValido(nombre) {
		return uuid.Nil, ports.ErrSnapshotNombreInvalido
	}
	if nombre == "inexistente" {
		return uuid.Nil, ports.ErrSnapshotNoEncontrado
	}
	f.vmid = vmid
	f.creados = append(f.creados, orden)
	return uuid.MustParse("0192e4a1-4f89-7e23-9a0c-0305e82c3302"), nil
}

// permisoSnapshots da a cualquier OPERATOR el nivel indicado sobre la instancia 110.
type permisoSnapshots struct {
	ports.InstanceRepository
	nivel string
}

func (p permisoSnapshots) VerificarAcceso(_ context.Context, _ uuid.UUID, vmid int, requerido string) (bool, error) {
	if vmid != 110 || p.nivel == "" {
		return false, nil
	}
	return requerido == ports.NivelAccesoReadOnly || p.nivel == ports.NivelAccesoFullAccess, nil
}

// pedirSnapshots arma la misma cadena de middlewares que cmd/api/main.go.
func pedirSnapshots(svc ports.SnapshotService, rol, nivel, metodo, ruta, cuerpo string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	h := adaptersHttp.NewSnapshotHandler(svc)
	repo := permisoSnapshots{nivel: nivel}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.ContextKeyRol, rol)
		c.Set(middleware.ContextKeyUserID, uuid.New().String())
	})
	instancia := r.Group("/api/instances/:vmid", adaptersHttp.ResolverPrueba)
	instancia.GET("/snapshots", middleware.RequireInstanceAccess(repo, ports.NivelAccesoReadOnly), h.ListarSnapshots)
	instancia.POST("/snapshots", middleware.RequireInstanceAccess(repo, ports.NivelAccesoFullAccess), h.CrearSnapshot)
	instancia.POST("/snapshots/:snapname/rollback", middleware.RequireInstanceAccess(repo, ports.NivelAccesoFullAccess), h.RevertirSnapshot)
	instancia.DELETE("/snapshots/:snapname", middleware.RequireInstanceAccess(repo, ports.NivelAccesoFullAccess), h.EliminarSnapshot)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(metodo, ruta, strings.NewReader(cuerpo)))
	return w
}

func TestSnapshots_ListadoConElContrato(t *testing.T) {
	svc := &snapshotsFalso{}
	w := pedirSnapshots(svc, "ADMIN", "", http.MethodGet, adaptersHttp.ConRef("/api/instances/110/snapshots"), "")
	var lista []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &lista); err != nil || w.Code != http.StatusOK || len(lista) != 2 || svc.vmid != 110 {
		t.Fatalf("%d %v %s", w.Code, err, w.Body.String())
	}
	esperado := map[string]any{"name": "snap_pre_upgrade", "snaptime": float64(1774358400), "description": "Punto previo a upgrade", "parent": "base", "vmstate": false}
	for k, v := range esperado {
		if lista[0][k] != v {
			t.Errorf("%s = %v, se esperaba %v", k, lista[0][k], v)
		}
	}
	if len(lista[0]) != len(esperado) || lista[1]["parent"] != "" {
		t.Errorf("Claves del contrato (parent \"\" en el primero de la cadena): %v", lista)
	}
}

// DoD: la creación responde 202 con tareaId y snapname, nunca el UPID.
func TestSnapshots_Creacion202(t *testing.T) {
	svc := &snapshotsFalso{}
	w := pedirSnapshots(svc, "ADMIN", "", http.MethodPost, adaptersHttp.ConRef("/api/instances/110/snapshots"), `{"snapname":"snap_pre_upgrade","description":"x"}`)
	var cuerpo map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &cuerpo)
	if w.Code != http.StatusAccepted || cuerpo["tareaId"] != "0192e4a1-4f89-7e23-9a0c-0305e82c3301" || cuerpo["snapname"] != "snap_pre_upgrade" || len(cuerpo) != 2 {
		t.Errorf("Se esperaba 202 {tareaId, snapname}: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(strings.ToLower(w.Body.String()), "upid") {
		t.Error("El UPID nunca sale de la API")
	}
}

// DoD: nombres con espacios o caracteres especiales → 400 INVALID_SNAPSHOT_NAME.
func TestSnapshots_NombreInvalido400(t *testing.T) {
	for _, nombre := range []string{"snap test!", "snap test", "1snap", "current"} {
		svc := &snapshotsFalso{}
		cuerpo, _ := json.Marshal(map[string]string{"snapname": nombre})
		w := pedirSnapshots(svc, "ADMIN", "", http.MethodPost, adaptersHttp.ConRef("/api/instances/110/snapshots"), string(cuerpo))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"errorCode":"INVALID_SNAPSHOT_NAME"`) || len(svc.creados) != 0 {
			t.Errorf("%q: se esperaba 400 INVALID_SNAPSHOT_NAME, vino %d %s", nombre, w.Code, w.Body.String())
		}
	}
	w := pedirSnapshots(&snapshotsFalso{}, "ADMIN", "", http.MethodPost, adaptersHttp.ConRef("/api/instances/110/snapshots"), `{"description":"sin nombre"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"errorCode":"INVALID_REQUEST"`) {
		t.Errorf("Sin snapname: se esperaba 400 INVALID_REQUEST, vino %d %s", w.Code, w.Body.String())
	}
	w = pedirSnapshots(&snapshotsFalso{}, "ADMIN", "", http.MethodPost, adaptersHttp.ConRef("/api/instances/110/snapshots"), `{"snapname":"existente"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"errorCode":"SNAPSHOT_ALREADY_EXISTS"`) {
		t.Errorf("Repetido: se esperaba 409 SNAPSHOT_ALREADY_EXISTS, vino %d %s", w.Code, w.Body.String())
	}
}

// DoD: un OPERATOR con READ_ONLY lista, pero recibe 403 al crear; con FULL_ACCESS crea.
func TestSnapshots_PermisosDelOperator(t *testing.T) {
	ruta := adaptersHttp.ConRef("/api/instances/110/snapshots")
	if w := pedirSnapshots(&snapshotsFalso{}, "OPERATOR", ports.NivelAccesoReadOnly, http.MethodGet, ruta, ""); w.Code != http.StatusOK {
		t.Errorf("READ_ONLY lista: %d %s", w.Code, w.Body.String())
	}
	svc := &snapshotsFalso{}
	w := pedirSnapshots(svc, "OPERATOR", ports.NivelAccesoReadOnly, http.MethodPost, ruta, `{"snapname":"snap1"}`)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "INSTANCE_ACCESS_DENIED") || len(svc.creados) != 0 {
		t.Errorf("READ_ONLY no crea: %d %s", w.Code, w.Body.String())
	}
	if w := pedirSnapshots(&snapshotsFalso{}, "OPERATOR", ports.NivelAccesoFullAccess, http.MethodPost, ruta, `{"snapname":"snap1"}`); w.Code != http.StatusAccepted {
		t.Errorf("FULL_ACCESS crea: %d %s", w.Code, w.Body.String())
	}
	if w := pedirSnapshots(&snapshotsFalso{}, "OPERATOR", "", http.MethodGet, ruta, ""); w.Code != http.StatusForbidden {
		t.Errorf("Sin permiso no lista: %d", w.Code)
	}
}

func TestSnapshots_RollbackYBorrado202(t *testing.T) {
	for metodo, ruta := range map[string]string{
		http.MethodPost:   "/api/instances/110/snapshots/snap_1/rollback",
		http.MethodDelete: "/api/instances/110/snapshots/snap_1",
	} {
		svc := &snapshotsFalso{}
		w := pedirSnapshots(svc, "ADMIN", "", metodo, adaptersHttp.ConRef(ruta), "")
		var cuerpo map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &cuerpo)
		if w.Code != http.StatusAccepted || cuerpo["tareaId"] != "0192e4a1-4f89-7e23-9a0c-0305e82c3302" || len(cuerpo) != 1 || len(svc.creados) != 1 || svc.vmid != 110 {
			t.Errorf("%s %s: se esperaba 202 {tareaId}: %d %s", metodo, ruta, w.Code, w.Body.String())
		}
	}
}

// DoD: un OPERATOR con READ_ONLY recibe 403 en rollback y en borrado.
func TestSnapshots_RollbackYBorradoConReadOnly403(t *testing.T) {
	for metodo, ruta := range map[string]string{
		http.MethodPost:   "/api/instances/110/snapshots/snap_1/rollback",
		http.MethodDelete: "/api/instances/110/snapshots/snap_1",
	} {
		svc := &snapshotsFalso{}
		w := pedirSnapshots(svc, "OPERATOR", ports.NivelAccesoReadOnly, metodo, adaptersHttp.ConRef(ruta), "")
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "INSTANCE_ACCESS_DENIED") || len(svc.creados) != 0 {
			t.Errorf("%s %s con READ_ONLY: se esperaba 403, vino %d %s", metodo, ruta, w.Code, w.Body.String())
		}
		if w := pedirSnapshots(&snapshotsFalso{}, "OPERATOR", ports.NivelAccesoFullAccess, metodo, adaptersHttp.ConRef(ruta), ""); w.Code != http.StatusAccepted {
			t.Errorf("%s %s con FULL_ACCESS: %d %s", metodo, ruta, w.Code, w.Body.String())
		}
	}
}

func TestSnapshots_RollbackNombreInvalidoOInexistente(t *testing.T) {
	casos := map[string]struct {
		status int
		codigo string
	}{
		"/api/instances/110/snapshots/1snap/rollback":       {http.StatusBadRequest, "INVALID_SNAPSHOT_NAME"},
		"/api/instances/110/snapshots/current/rollback":     {http.StatusBadRequest, "INVALID_SNAPSHOT_NAME"},
		"/api/instances/110/snapshots/inexistente/rollback": {http.StatusNotFound, "SNAPSHOT_NOT_FOUND"},
	}
	for ruta, esperado := range casos {
		svc := &snapshotsFalso{}
		w := pedirSnapshots(svc, "ADMIN", "", http.MethodPost, adaptersHttp.ConRef(ruta), "")
		if w.Code != esperado.status || !strings.Contains(w.Body.String(), `"errorCode":"`+esperado.codigo+`"`) || len(svc.creados) != 0 {
			t.Errorf("%s: se esperaba %d %s, vino %d %s", ruta, esperado.status, esperado.codigo, w.Code, w.Body.String())
		}
	}
}

// tareasEnCurso es tareas_asincronas en memoria: una orden aceptada deja su
// instancia con una tarea RUNNING, como hace el seguimiento real.
type tareasEnCurso struct {
	ports.TareaRepository
	mu      sync.Mutex
	activas map[int]ports.ActiveTaskDTO
}

func (r *tareasEnCurso) BuscarTareasActivasPorVmids(_ context.Context, vmids []int) (map[int]ports.ActiveTaskDTO, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	resultado := map[int]ports.ActiveTaskDTO{}
	for _, v := range vmids {
		if t, ok := r.activas[v]; ok {
			resultado[v] = t
		}
	}
	return resultado, nil
}

// snapshotsQueOcupan registra la tarea RUNNING al aceptar la orden.
type snapshotsQueOcupan struct {
	snapshotsFalso
	tareas   *tareasEnCurso
	mu       sync.Mutex
	llamadas int
}

func (s *snapshotsQueOcupan) Revertir(_ context.Context, _ uuid.UUID, vmid int, _ string) (uuid.UUID, error) {
	s.mu.Lock()
	s.llamadas++
	s.mu.Unlock()
	time.Sleep(20 * time.Millisecond) // Proxmox tarda en aceptar la orden
	s.tareas.mu.Lock()
	s.tareas.activas[vmid] = ports.ActiveTaskDTO{TareaID: uuid.NewString(), Action: ports.AccionTareaRollbackSnapshot, Status: ports.TareaRunning}
	s.tareas.mu.Unlock()
	return uuid.New(), nil
}

func (s *snapshotsQueOcupan) Eliminar(ctx context.Context, u uuid.UUID, vmid int, n string) (uuid.UUID, error) {
	return s.Revertir(ctx, u, vmid, n)
}

// routerConExclusion arma las rutas con la guarda real RequireInstanceIdle.
func routerConExclusion(svc ports.SnapshotService, tareas ports.TareaRepository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := adaptersHttp.NewSnapshotHandler(svc)
	exclusion := services.NewExclusionInstancias(memoria.Nuevo(), tareas)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.ContextKeyRol, "ADMIN")
		c.Set(middleware.ContextKeyUserID, uuid.New().String())
	})
	instancia := r.Group("/api/instances/:vmid", adaptersHttp.ResolverPrueba)
	instancia.POST("/snapshots/:snapname/rollback", middleware.RequireInstanceIdle(exclusion), h.RevertirSnapshot)
	instancia.DELETE("/snapshots/:snapname", middleware.RequireInstanceIdle(exclusion), h.EliminarSnapshot)
	return r
}

// DoD: con una tarea RUNNING de la instancia, rollback y borrado responden
// 409 INSTANCE_BUSY sin llegar al servicio (ni a Proxmox).
func TestSnapshots_ConTareaActiva409(t *testing.T) {
	tareas := &tareasEnCurso{activas: map[int]ports.ActiveTaskDTO{110: {TareaID: uuid.NewString(), Action: "START", Status: ports.TareaRunning}}}
	svc := &snapshotsQueOcupan{tareas: tareas}
	r := routerConExclusion(svc, tareas)
	for metodo, ruta := range map[string]string{
		http.MethodPost:   "/api/instances/110/snapshots/snap_1/rollback",
		http.MethodDelete: "/api/instances/110/snapshots/snap_1",
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(metodo, adaptersHttp.ConRef(ruta), nil))
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"errorCode":"INSTANCE_BUSY"`) {
			t.Errorf("%s %s: se esperaba 409 INSTANCE_BUSY, vino %d %s", metodo, ruta, w.Code, w.Body.String())
		}
	}
	if svc.llamadas != 0 {
		t.Errorf("No debe llegar al servicio: %d llamadas", svc.llamadas)
	}
}

// DoD: ante una ráfaga de rollbacks y borrados simultáneos sobre la misma
// instancia pasa uno solo; el resto recibe 409 sin llegar a Proxmox.
func TestSnapshots_RafagaConcurrenteSoloUnoPasa(t *testing.T) {
	tareas := &tareasEnCurso{activas: map[int]ports.ActiveTaskDTO{}}
	svc := &snapshotsQueOcupan{tareas: tareas}
	r := routerConExclusion(svc, tareas)
	var (
		wg              sync.WaitGroup
		mu              sync.Mutex
		aceptados, busy int
		otros           []int
	)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			metodo, ruta := http.MethodPost, "/api/instances/110/snapshots/snap_1/rollback"
			if i%2 == 1 {
				metodo, ruta = http.MethodDelete, "/api/instances/110/snapshots/snap_1"
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(metodo, adaptersHttp.ConRef(ruta), nil))
			mu.Lock()
			defer mu.Unlock()
			switch w.Code {
			case http.StatusAccepted:
				aceptados++
			case http.StatusConflict:
				busy++
			default:
				otros = append(otros, w.Code)
			}
		}(i)
	}
	wg.Wait()
	if aceptados != 1 || busy != 9 || len(otros) != 0 || svc.llamadas != 1 {
		t.Errorf("Se esperaba 1 aceptado y 9 en 409 con una sola llamada al servicio: aceptados=%d busy=%d otros=%v llamadas=%d", aceptados, busy, otros, svc.llamadas)
	}
}
