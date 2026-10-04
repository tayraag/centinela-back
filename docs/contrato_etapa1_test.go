package docs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestContratoEtapa1(t *testing.T) {
	contenidoSwagger, err := os.ReadFile("swagger.json")
	if err != nil {
		t.Fatalf("leer swagger.json: %v", err)
	}

	var swagger map[string]any
	if err := json.Unmarshal(contenidoSwagger, &swagger); err != nil {
		t.Fatalf("decodificar swagger.json: %v", err)
	}

	paths := objeto(t, swagger, "paths")
	nodeStatus := objeto(t, objeto(t, paths, "/node/status"), "get")
	if estado := cadena(t, nodeStatus, "x-implementation-status"); estado != "planned" {
		t.Fatalf("GET /node/status debe marcarse planned, obtuvo %q", estado)
	}
	if descripcion := cadena(t, nodeStatus, "description"); !strings.Contains(descripcion, "NO OPERATIVO") {
		t.Fatalf("GET /node/status no advierte su estado planificado: %q", descripcion)
	}
	verificarBearer(t, nodeStatus)
	verificarRespuesta(t, nodeStatus, "200", "http.EstadoNodeResponse")

	definitions := objeto(t, swagger, "definitions")
	verificarObjetoRequerido(t, definitions, "http.EstadoNodeResponse", []string{
		"cpu", "ram", "storage", "uptimeSeconds", "instancesSummary", "stale", "fetchedAt",
	})
	verificarObjetoRequerido(t, definitions, "http.MetricaCPUNode", []string{
		"usagePercent", "cores",
	})
	verificarObjetoRequerido(t, definitions, "http.MetricaCapacidadNode", []string{
		"usedGb", "totalGb", "usagePercent",
	})
	verificarObjetoRequerido(t, definitions, "http.ResumenInstanciasNode", []string{
		"vms", "lxc",
	})
	verificarObjetoRequerido(t, definitions, "http.ResumenEstadoInstancias", []string{
		"running", "stopped", "paused", "total",
	})

	fetchedAt := propiedad(t, definitions, "http.EstadoNodeResponse", "fetchedAt")
	if formato := cadena(t, fetchedAt, "format"); formato != "date-time" {
		t.Fatalf("fetchedAt debe usar formato date-time/RFC3339, obtuvo %q", formato)
	}

	inventario := objeto(t, objeto(t, paths, "/instances"), "get")
	if descripcion := cadena(t, inventario, "description"); !strings.Contains(descripcion, "OPERATIVO") {
		t.Fatalf("GET /instances no declara estado operativo: %q", descripcion)
	}
	verificarBearer(t, inventario)
	respuestaInventario := objeto(t, objeto(t, objeto(t, inventario, "responses"), "200"), "schema")
	if tipo := cadena(t, respuestaInventario, "type"); tipo != "array" {
		t.Fatalf("GET /instances debe responder array, obtuvo %q", tipo)
	}
	items := objeto(t, respuestaInventario, "items")
	if ref := cadena(t, items, "$ref"); ref != "#/definitions/http.InstanciaInventarioResponse" {
		t.Fatalf("GET /instances usa definición inesperada: %q", ref)
	}

	const inventarioDef = "http.InstanciaInventarioResponse"
	verificarObjetoRequerido(t, definitions, inventarioDef, []string{
		"id", "name", "type", "node", "status", "ip", "cpuUsage", "ramUsage", "maxRam", "nivelAcceso", "activeTask",
	})
	for _, campo := range []string{"ip", "activeTask"} {
		if nullable, ok := propiedad(t, definitions, inventarioDef, campo)["x-nullable"].(bool); !ok || !nullable {
			t.Errorf("%s debe declarar x-nullable: true", campo)
		}
	}

	contenidoContrato, err := os.ReadFile("contrato-etapa1.md")
	if err != nil {
		t.Fatalf("leer contrato-etapa1.md: %v", err)
	}
	contrato := string(contenidoContrato)
	for _, fragmento := range []string{
		"GET /api/node/status", "planificada", "404 NOT_FOUND", "GET /api/instances", "operativa",
		"usagePercent", "instancesSummary", "fetchedAt", "nivelAcceso", "activeTask",
	} {
		if !strings.Contains(contrato, fragmento) {
			t.Errorf("contrato-etapa1.md no contiene %q", fragmento)
		}
	}
}

func objeto(t *testing.T, padre map[string]any, clave string) map[string]any {
	t.Helper()
	valor, ok := padre[clave].(map[string]any)
	if !ok {
		t.Fatalf("%q no existe o no es un objeto", clave)
	}
	return valor
}

func cadena(t *testing.T, padre map[string]any, clave string) string {
	t.Helper()
	valor, ok := padre[clave].(string)
	if !ok {
		t.Fatalf("%q no existe o no es string", clave)
	}
	return valor
}

func verificarBearer(t *testing.T, operacion map[string]any) {
	t.Helper()
	seguridad, ok := operacion["security"].([]any)
	if !ok || len(seguridad) != 1 {
		t.Fatalf("security debe contener BearerAuth: %#v", operacion["security"])
	}
	if _, ok := seguridad[0].(map[string]any)["BearerAuth"]; !ok {
		t.Fatalf("security no contiene BearerAuth: %#v", seguridad)
	}
}

func verificarRespuesta(t *testing.T, operacion map[string]any, codigo, definicion string) {
	t.Helper()
	respuesta := objeto(t, objeto(t, operacion, "responses"), codigo)
	esquema := objeto(t, respuesta, "schema")
	if ref := cadena(t, esquema, "$ref"); ref != "#/definitions/"+definicion {
		t.Fatalf("respuesta %s usa definición inesperada: %q", codigo, ref)
	}
}

func verificarObjetoRequerido(t *testing.T, definitions map[string]any, nombre string, campos []string) {
	t.Helper()
	definicion := objeto(t, definitions, nombre)
	propiedades := objeto(t, definicion, "properties")
	requeridosRaw, ok := definicion["required"].([]any)
	if !ok {
		t.Fatalf("%s no declara campos required", nombre)
	}
	requeridos := make(map[string]bool, len(requeridosRaw))
	for _, valor := range requeridosRaw {
		campo, ok := valor.(string)
		if ok {
			requeridos[campo] = true
		}
	}
	for _, campo := range campos {
		if _, ok := propiedades[campo]; !ok {
			t.Errorf("%s no define la propiedad %s", nombre, campo)
		}
		if !requeridos[campo] {
			t.Errorf("%s no exige la propiedad %s", nombre, campo)
		}
	}
}

func propiedad(t *testing.T, definitions map[string]any, definicion, campo string) map[string]any {
	t.Helper()
	return objeto(t, objeto(t, objeto(t, definitions, definicion), "properties"), campo)
}
