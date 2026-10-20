package http_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	adaptersHttp "el-centinela/internal/adapters/primary/http"
	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var formatoReferencia = regexp.MustCompile(`^inst_[0-9A-Za-z]{22}$`)

// DoD: GET /api/instances devuelve únicamente identificadores opacos en id.
func TestListarInstancias_SoloReferenciasOpacas(t *testing.T) {
	gin.SetMode(gin.TestMode)
	px := &mockProxmoxPort{instancias: []ports.InstanciaProxmoxDTO{
		{Vmid: 100, Nombre: "db", Tipo: "qemu", Nodo: "pve1", Estado: "running"},
		{Vmid: 101, Nombre: "api", Tipo: "qemu", Nodo: "pve1", Estado: "running"},
		{Vmid: 201, Nombre: "ct", Tipo: "lxc", Nodo: "pve1", Estado: "stopped"},
	}}
	handler := adaptersHttp.NewInstanceHandler(px, &mockUserRepository{}, seguimientoNulo{}, &mockTareaRepository{}, &mockAuditService{}, inventarioNulo{}, adaptersHttp.RefsPrueba)
	router := gin.New()
	router.GET("/api/instances", func(c *gin.Context) {
		c.Set(middleware.ContextKeyRol, "ADMIN")
		c.Set(middleware.ContextKeyUserID, uuid.New().String())
	}, handler.ListarInstancias)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/instances", nil))

	var inventario []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &inventario); err != nil || len(inventario) != 3 {
		t.Fatalf("%d %v %s", w.Code, err, w.Body.String())
	}
	for i, vmid := range []int{100, 101, 201} {
		id, esTexto := inventario[i]["id"].(string)
		if !esTexto || !formatoReferencia.MatchString(id) || id != adaptersHttp.RefInst(vmid) {
			t.Errorf("La instancia %d debe tener una referencia opaca en id, vino %#v", vmid, inventario[i]["id"])
		}
		if _, expone := inventario[i]["vmid"]; expone {
			t.Errorf("El inventario no debe exponer vmid: %v", inventario[i])
		}
	}
}

// DoD: un token alterado o inventado responde exactamente lo mismo que una
// instancia que no existe en Proxmox, y no llega a consultarse Proxmox.
func TestReferenciaInvalida_MismaRespuestaQueInstanciaInexistente(t *testing.T) {
	gin.SetMode(gin.TestMode)
	valida := adaptersHttp.RefInst(110)
	alterada := []byte(valida)
	if alterada[12] == 'Q' {
		alterada[12] = 'R'
	} else {
		alterada[12] = 'Q'
	}

	pedir := func(mock *mockProxmoxPort, metodo, ruta string) (int, string) {
		handler := adaptersHttp.NewInstanceHandler(mock, &mockUserRepository{}, seguimientoNulo{}, &mockTareaRepository{}, &mockAuditService{}, inventarioNulo{}, adaptersHttp.RefsPrueba)
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set(middleware.ContextKeyRol, "ADMIN")
			c.Set(middleware.ContextKeyUserID, uuid.New().String())
		})
		instancia := router.Group("/api/instances/:vmid", adaptersHttp.ResolverPrueba)
		instancia.GET("", handler.ObtenerInstancia)
		instancia.POST("/start", handler.IniciarInstancia)
		instancia.POST("/stop", handler.DetenerInstancia)
		instancia.POST("/status/:action", handler.CambiarEstado)
		instancia.DELETE("", handler.EliminarInstancia)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(metodo, ruta, nil))
		return w.Code, w.Body.String()
	}

	for _, ep := range []struct{ metodo, sufijo string }{
		{http.MethodGet, ""}, {http.MethodPost, "/start"}, {http.MethodPost, "/stop"},
		{http.MethodPost, "/status/reboot"}, {http.MethodDelete, ""},
	} {
		inexistente := &mockProxmoxPort{errDetalle: ports.ErrInstanciaNoEncontrada}
		codigoRef, cuerpoRef := pedir(inexistente, ep.metodo, "/api/instances/"+valida+ep.sufijo)
		if codigoRef != http.StatusNotFound || !strings.Contains(cuerpoRef, `"errorCode":"RESOURCE_NOT_FOUND"`) {
			t.Fatalf("%s %s de una instancia inexistente: se esperaba 404 RESOURCE_NOT_FOUND, vino %d %s", ep.metodo, ep.sufijo, codigoRef, cuerpoRef)
		}
		for _, token := range []string{string(alterada), "inst_k8Pq2v", "110", "inst_" + strings.Repeat("0", 22)} {
			mock := &mockProxmoxPort{estado: "stopped"}
			codigo, cuerpo := pedir(mock, ep.metodo, "/api/instances/"+token+ep.sufijo)
			if codigo != codigoRef || cuerpo != cuerpoRef {
				t.Errorf("%s %q%s: debe responder igual que una instancia inexistente (%d %s), vino %d %s", ep.metodo, token, ep.sufijo, codigoRef, cuerpoRef, codigo, cuerpo)
			}
			if mock.lecturas != 0 {
				t.Errorf("%s %q%s: una referencia inválida no debe consultar Proxmox", ep.metodo, token, ep.sufijo)
			}
		}
	}
}
