package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	adaptersHttp "el-centinela/internal/adapters/primary/http"
	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// proxmoxConTelemetria devuelve instancias con CPU en fracción, como Proxmox.
type proxmoxConTelemetria struct {
	mockProxmoxPort
	porVmid map[int]ports.InstanciaProxmoxDTO
}

func (p *proxmoxConTelemetria) ObtenerInstancia(_ context.Context, vmid int) (*ports.InstanciaProxmoxDTO, error) {
	inst, ok := p.porVmid[vmid]
	if !ok {
		return nil, ports.ErrInstanciaNoEncontrada
	}
	return &inst, nil
}

func (p *proxmoxConTelemetria) ListarInstancias(context.Context) ([]ports.InstanciaProxmoxDTO, error) {
	var lista []ports.InstanciaProxmoxDTO
	for _, inst := range p.porVmid {
		lista = append(lista, inst)
	}
	return lista, nil
}

// Convención de la PM para CPU y RAM: CPU en porcentaje (0 a 100) y null
// cuando la instancia está apagada, en el inventario y en el detalle.
func TestMetricas_PorcentajeYNullCuandoEstaApagada(t *testing.T) {
	gin.SetMode(gin.TestMode)
	px := &proxmoxConTelemetria{porVmid: map[int]ports.InstanciaProxmoxDTO{
		110: {Vmid: 110, Nombre: "web", Tipo: "qemu", Nodo: "pve", Estado: "running", Cpu: 0.2449, Mem: 1024, MaxMem: 4096, MaxCpu: 2},
		201: {Vmid: 201, Nombre: "db", Tipo: "lxc", Nodo: "pve", Estado: "stopped", Cpu: 0, Mem: 0, MaxMem: 2048, MaxCpu: 1},
	}}
	h := adaptersHttp.NewInstanceHandler(px, &mockUserRepository{}, seguimientoNulo{}, &mockTareaRepository{}, &mockAuditService{}, inventarioNulo{}, adaptersHttp.RefsPrueba, adaptersHttp.TopologiaPrueba)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyRol), "ADMIN")
		c.Set(string(middleware.ContextKeyUserID), uuid.New().String())
	})
	r.GET("/api/instances", h.ListarInstancias)
	r.GET("/api/instances/:vmid", adaptersHttp.ResolverPrueba, h.ObtenerInstancia)
	pedir := func(ruta string) any {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, ruta, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", ruta, w.Code, w.Body.String())
		}
		var cuerpo any
		_ = json.Unmarshal(w.Body.Bytes(), &cuerpo)
		return cuerpo
	}

	inventario := map[string]map[string]any{}
	for _, i := range pedir("/api/instances").([]any) {
		inst := i.(map[string]any)
		inventario[inst["id"].(string)] = inst
	}
	if inventario[adaptersHttp.RefInst(110)]["cpuUsage"] != 24.49 || inventario[adaptersHttp.RefInst(110)]["ramUsage"] != float64(1024) {
		t.Errorf("Inventario, encendida: cpuUsage en porcentaje y ramUsage: %v", inventario[adaptersHttp.RefInst(110)])
	}
	if v, ok := inventario[adaptersHttp.RefInst(201)]["cpuUsage"]; !ok || v != nil {
		t.Errorf("Inventario, apagada: cpuUsage debe ser null: %v", inventario[adaptersHttp.RefInst(201)])
	}
	if v, ok := inventario[adaptersHttp.RefInst(201)]["ramUsage"]; !ok || v != nil {
		t.Errorf("Inventario, apagada: ramUsage debe ser null: %v", inventario[adaptersHttp.RefInst(201)])
	}

	encendida := pedir(adaptersHttp.ConRef("/api/instances/110")).(map[string]any)
	if encendida["cpu"] != 24.49 || encendida["mem"] != float64(1024) || encendida["maxMem"] != float64(4096) {
		t.Errorf("Detalle, encendida: cpu en porcentaje: %v", encendida)
	}
	apagada := pedir(adaptersHttp.ConRef("/api/instances/201")).(map[string]any)
	if v, ok := apagada["cpu"]; !ok || v != nil {
		t.Errorf("Detalle, apagada: cpu debe ser null: %v", apagada)
	}
	if v, ok := apagada["mem"]; !ok || v != nil {
		t.Errorf("Detalle, apagada: mem debe ser null: %v", apagada)
	}
	if apagada["estado"] != "stopped" || apagada["maxMem"] != float64(2048) {
		t.Errorf("Detalle, apagada: el resto de los datos se conserva: %v", apagada)
	}
}
