package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// usuariosFalso implementa lo que usan los endpoints de permisos, detalle,
// perfil y actividad; el resto de ports.UserService no se llama.
type usuariosFalso struct {
	ports.UserService
	asignados []ports.PermisoInstanciaInput
	asigno    bool
	err       error
}

func (f *usuariosFalso) AsignarPermisos(_ context.Context, _, _, _ uuid.UUID, permisos []ports.PermisoInstanciaInput) error {
	f.asigno, f.asignados = true, permisos
	return f.err
}

func (f *usuariosFalso) ObtenerPermisos(context.Context, uuid.UUID, uuid.UUID) ([]ports.PermisoInstanciaInput, error) {
	return []ports.PermisoInstanciaInput{{Vmid: 110, NivelAcceso: ports.NivelAccesoFullAccess}, {Vmid: 201, NivelAcceso: ports.NivelAccesoReadOnly}}, nil
}

func (f *usuariosFalso) detalle() *ports.UsuarioDetalleDTO {
	return &ports.UsuarioDetalleDTO{ID: uuid.New(), Rol: "OPERATOR", InstanciasPermitidas: []int{110},
		Permisos: []ports.PermisoInstanciaDTO{{VMID: 110, NivelAcceso: ports.NivelAccesoFullAccess}}}
}

func (f *usuariosFalso) ObtenerUsuario(context.Context, uuid.UUID, uuid.UUID) (*ports.UsuarioDetalleDTO, error) {
	return f.detalle(), nil
}

func (f *usuariosFalso) ObtenerPerfil(context.Context, uuid.UUID) (*ports.UsuarioDetalleDTO, error) {
	return f.detalle(), nil
}

func (f *usuariosFalso) ListarActividad(context.Context, uuid.UUID, uuid.UUID, ports.FiltrosActividad) ([]ports.ActividadDTO, error) {
	return []ports.ActividadDTO{
		{Accion: ports.AccionIniciarVM, InstanciaID: "110", Detalles: `{"error":"VM 110 is locked"}`},
		{Accion: ports.AccionInstancePermissionsPurged, InstanciaID: "201", Detalles: `{"vmid":201,"usuarios_afectados":[]}`},
		{Accion: ports.AccionLogin},
	}, nil
}

func routerUsuarios(f *usuariosFalso) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h, cuenta := NewUserHandler(f, RefsPrueba), NewAccountHandler(f, RefsPrueba)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.ContextKeyUserID, uuid.New().String())
		c.Set(middleware.ContextKeyOrgID, uuid.New().String())
	})
	r.PUT("/api/admin/users/:id/permissions", h.AsignarPermisos)
	r.GET("/api/admin/users/:id/permissions", h.ObtenerPermisos)
	r.GET("/api/admin/users/:id", h.ObtenerUsuario)
	r.GET("/api/admin/users/:id/activity", h.ListarActividad)
	r.GET("/api/account/profile", cuenta.ObtenerPerfil)
	return r
}

func pedirUsuarios(r *gin.Engine, metodo, ruta, cuerpo string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(metodo, ruta, strings.NewReader(cuerpo))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestAsignarPermisos_RecibeReferenciasYGuardaVmids(t *testing.T) {
	f := &usuariosFalso{}
	cuerpo := `{"permisos":[{"vmid":"` + RefInst(110) + `","nivelAcceso":"READ_ONLY"},{"vmid":"` + RefInst(201) + `"}]}`
	w := pedirUsuarios(routerUsuarios(f), http.MethodPut, "/api/admin/users/"+uuid.NewString()+"/permissions", cuerpo)
	if w.Code != http.StatusNoContent {
		t.Fatalf("Se esperaba 204, vino %d %s", w.Code, w.Body.String())
	}
	esperado := []ports.PermisoInstanciaInput{{Vmid: 110, NivelAcceso: ports.NivelAccesoReadOnly}, {Vmid: 201, NivelAcceso: ports.NivelAccesoFullAccess}}
	if len(f.asignados) != 2 || f.asignados[0] != esperado[0] || f.asignados[1] != esperado[1] {
		t.Errorf("El servicio debe recibir los VMID (y FULL_ACCESS por defecto): %+v", f.asignados)
	}
}

func TestAsignarPermisos_ReferenciaInvalidaEs400SinCambios(t *testing.T) {
	for _, cuerpo := range []string{
		`{"permisos":[{"vmid":"` + RefInst(110) + `"},{"vmid":"inst_k8Pq2v"}]}`,
		`{"permisos":[{"vmid":"110"}]}`,
		`{"permisos":[{"vmid":110,"nivelAcceso":"FULL_ACCESS"}]}`,
		`{"permisos":[{"instanciaId":"` + RefInst(110) + `"}]}`,
	} {
		f := &usuariosFalso{}
		w := pedirUsuarios(routerUsuarios(f), http.MethodPut, "/api/admin/users/"+uuid.NewString()+"/permissions", cuerpo)
		if w.Code != http.StatusBadRequest || f.asigno {
			t.Errorf("%s: se esperaba 400 sin tocar permisos, vino %d (asignó=%v) %s", cuerpo, w.Code, f.asigno, w.Body.String())
		}
	}
}

func TestAsignarPermisos_TraduceValidacionesDelCatalogo(t *testing.T) {
	casos := []struct {
		nombre string
		err    error
		status int
		codigo string
		msg    string
	}{
		{"inexistente", &ports.ErrorVMIDNoExiste{VMID: 999}, http.StatusBadRequest, "INVALID_REQUEST", "El VMID 999 no existe en el hipervisor."},
		{"protegida", &ports.ErrorVMIDProtegido{VMID: 100}, http.StatusForbidden, "PROTECTED_INSTANCE_CANNOT_BE_ASSIGNED", "La instancia 100 es de infraestructura protegida y no puede asignarse a operadores."},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			f := &usuariosFalso{err: caso.err}
			cuerpo := `{"permisos":[{"vmid":"` + RefInst(110) + `"}]}`
			w := pedirUsuarios(routerUsuarios(f), http.MethodPut, "/api/admin/users/"+uuid.NewString()+"/permissions", cuerpo)
			var respuesta map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &respuesta); err != nil {
				t.Fatal(err)
			}
			if w.Code != caso.status || respuesta["errorCode"] != caso.codigo || respuesta["message"] != caso.msg {
				t.Fatalf("respuesta inesperada: status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestPermisosDetalleYPerfil_DevuelvenReferencias(t *testing.T) {
	r := routerUsuarios(&usuariosFalso{})
	id := uuid.NewString()
	for _, ruta := range []string{"/api/admin/users/" + id + "/permissions", "/api/admin/users/" + id, "/api/account/profile"} {
		w := pedirUsuarios(r, http.MethodGet, ruta, "")
		cuerpo := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(cuerpo, `"vmid":"`+RefInst(110)+`"`) || strings.Contains(cuerpo, `"vmid":110`) || strings.Contains(cuerpo, "instanciaId") {
			t.Errorf("%s: los permisos deben traer vmid con la referencia, nunca el número: %d %s", ruta, w.Code, cuerpo)
		}
	}
	var detalle struct {
		InstanciasPermitidas []string `json:"instanciasPermitidas"`
	}
	_ = json.Unmarshal(pedirUsuarios(r, http.MethodGet, "/api/account/profile", "").Body.Bytes(), &detalle)
	if len(detalle.InstanciasPermitidas) != 1 || detalle.InstanciasPermitidas[0] != RefInst(110) {
		t.Errorf("instanciasPermitidas debe traer referencias: %v", detalle.InstanciasPermitidas)
	}
}

func TestListarActividad_TraduceInstanciaYDetalles(t *testing.T) {
	w := pedirUsuarios(routerUsuarios(&usuariosFalso{}), http.MethodGet, "/api/admin/users/"+uuid.NewString()+"/activity", "")
	var actividad []ports.ActividadDTO
	if err := json.Unmarshal(w.Body.Bytes(), &actividad); err != nil || len(actividad) != 3 {
		t.Fatalf("%d %v %s", w.Code, err, w.Body.String())
	}
	if actividad[0].InstanciaID != RefInst(110) || actividad[0].Detalles != `{"error":"VM `+RefInst(110)+` is locked"}` {
		t.Errorf("Inicio: %+v", actividad[0])
	}
	if actividad[1].InstanciaID != RefInst(201) || actividad[1].Detalles != `{"usuarios_afectados":[],"vmid":"`+RefInst(201)+`"}` {
		t.Errorf("Purga: %+v", actividad[1])
	}
	if actividad[2].InstanciaID != "" {
		t.Errorf("Un registro sin instancia queda vacío: %+v", actividad[2])
	}
}
