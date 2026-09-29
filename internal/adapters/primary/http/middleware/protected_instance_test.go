package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"el-centinela/internal/adapters/primary/http/middleware"

	"github.com/gin-gonic/gin"
)

func TestParseVmidsProtegidos_VacioUsaDefault(t *testing.T) {
	protegidos, err := middleware.ParseVmidsProtegidos("")
	if err != nil {
		t.Fatalf("Error inesperado: %v", err)
	}
	for vmid := 100; vmid <= 105; vmid++ {
		if !protegidos[vmid] {
			t.Errorf("El VMID %d debe estar protegido por defecto", vmid)
		}
	}
	if protegidos[106] {
		t.Error("El VMID 106 no debe estar protegido por defecto")
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
	if _, err := middleware.ParseVmidsProtegidos("100,1O1"); err == nil {
		t.Fatal("Se esperaba error por un VMID inválido")
	}
}

func TestRejectProtectedInstance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	protegidos, _ := middleware.ParseVmidsProtegidos("")

	r := gin.New()
	r.POST("/instances/:vmid/stop", middleware.RejectProtectedInstance(protegidos, "vmid"), func(c *gin.Context) {
		c.Status(http.StatusAccepted)
	})

	casos := []struct {
		vmid   string
		codigo int
	}{
		{"101", http.StatusForbidden},  // base de datos: protegida
		{"102", http.StatusForbidden},  // la propia API: protegida
		{"200", http.StatusAccepted},   // instancia común: pasa
		{"abc", http.StatusBadRequest}, // vmid inválido
	}
	for _, caso := range casos {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/instances/"+caso.vmid+"/stop", nil)
		r.ServeHTTP(w, req)
		if w.Code != caso.codigo {
			t.Errorf("vmid %s: código esperado %d, obtenido %d (%s)", caso.vmid, caso.codigo, w.Code, w.Body.String())
		}
	}
}
