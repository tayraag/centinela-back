package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"el-centinela/internal/adapters/primary/http/middleware"

	"github.com/gin-gonic/gin"
)

// DoD: con PROXMOX_PROTECTED_VMIDS vacía no se protege ningún VMID por defecto.
func TestParseVmidsProtegidos_VacioNoProtegeNada(t *testing.T) {
	for _, raw := range []string{"", "   ", " , ,"} {
		protegidos, err := middleware.ParseVmidsProtegidos(raw)
		if err != nil || protegidos == nil || len(protegidos) != 0 {
			t.Errorf("%q: se esperaba un set vacío sin error, vino %v (%v)", raw, protegidos, err)
		}
	}
}

func TestParseVmidsProtegidos_ListaPersonalizada(t *testing.T) {
	protegidos, err := middleware.ParseVmidsProtegidos(" 200, 201 ,")
	if err != nil {
		t.Fatalf("Error inesperado: %v", err)
	}
	if len(protegidos) != 2 || !protegidos[200] || !protegidos[201] {
		t.Errorf("Set inesperado: %v", protegidos)
	}
}

func TestParseVmidsProtegidos_EntradaInvalida(t *testing.T) {
	for _, raw := range []string{"100,1O1", "200,0", "-5", "100-105"} {
		if _, err := middleware.ParseVmidsProtegidos(raw); err == nil {
			t.Errorf("%q: se esperaba error por un VMID inválido", raw)
		}
	}
}

// DoD: la lista de exclusión sale de PROXMOX_PROTECTED_VMIDS. Cambiar la
// variable cambia qué VMIDs responden 403 INSTANCE_PROTECTED, sin rangos fijos.
func TestRejectProtectedInstance_ListaDesdeEntorno(t *testing.T) {
	gin.SetMode(gin.TestMode)
	casos := []struct {
		entorno    string
		protegidos []int
		libres     []int
	}{
		{"200,201,202", []int{200, 201, 202}, []int{100, 101, 102, 103, 104, 105, 203}},
		{"101", []int{101}, []int{100, 102, 105, 200}},
		{"", nil, []int{100, 101, 102, 103, 104, 105, 200}},
	}
	for _, caso := range casos {
		t.Setenv("PROXMOX_PROTECTED_VMIDS", caso.entorno)
		protegidos, err := middleware.ParseVmidsProtegidos(os.Getenv("PROXMOX_PROTECTED_VMIDS"))
		if err != nil {
			t.Fatalf("%q: %v", caso.entorno, err)
		}
		r := gin.New()
		r.POST("/instances/:instanceRef/stop", middleware.ResolveInstanceRef(refsPrueba, "instanceRef"), middleware.RejectProtectedInstance(protegidos), func(c *gin.Context) {
			c.Status(http.StatusAccepted)
		})
		parar := func(instancia string) *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/instances/"+instancia+"/stop", nil)
			r.ServeHTTP(w, req)
			return w
		}
		for _, vmid := range caso.protegidos {
			if w := parar(ref(t, vmid)); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"errorCode":"INSTANCE_PROTECTED"`) {
				t.Errorf("PROXMOX_PROTECTED_VMIDS=%q, VMID %d: se esperaba 403 INSTANCE_PROTECTED, vino %d %s", caso.entorno, vmid, w.Code, w.Body.String())
			}
		}
		for _, vmid := range caso.libres {
			if w := parar(ref(t, vmid)); w.Code != http.StatusAccepted {
				t.Errorf("PROXMOX_PROTECTED_VMIDS=%q, VMID %d: debe poder operarse, vino %d %s", caso.entorno, vmid, w.Code, w.Body.String())
			}
		}
		if w := parar("101"); w.Code != http.StatusNotFound {
			t.Errorf("El VMID numérico no es una referencia: %d", w.Code)
		}
	}
}
