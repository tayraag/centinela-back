package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adaptersHttp "el-centinela/internal/adapters/primary/http"
	"el-centinela/internal/adapters/primary/http/middleware"
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
