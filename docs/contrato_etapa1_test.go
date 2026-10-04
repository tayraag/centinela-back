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

	// ==========================================================
	// T02 — Acciones aceptadas y catálogo completo de errores
	// ==========================================================

	const acceptedDef = "http.AccionAceptadaResponse"

	// El sobre de error real del servidor es { errorCode, message }.
	errorResponse := objeto(t, definitions, "http.ErrorResponse")
	for _, campo := range []string{"errorCode", "message"} {
		prop := objeto(t, objeto(t, errorResponse, "properties"), campo)
		if tipo := cadena(t, prop, "type"); tipo != "string" {
			t.Errorf("ErrorResponse.%s debe ser string, obtuvo %q", campo, tipo)
		}
	}

	// 202: upid obligatorio, tareaId opcional (se omite si el registro falla).
	verificarObjetoRequerido(t, definitions, acceptedDef, []string{"upid"})
	tareaID := propiedad(t, definitions, acceptedDef, "tareaId")
	if tipo := cadena(t, tareaID, "type"); tipo != "string" {
		t.Errorf("tareaId debe ser string, obtuvo %q", tipo)
	}
	if requeridosDe(t, objeto(t, definitions, acceptedDef))["tareaId"] {
		t.Error("tareaId no debe ser obligatorio: se omite cuando no se puede registrar la tarea")
	}

	// Los ocho pares error/status observados en las acciones aceptadas.
	pares := []struct{ codigo, errorCode string }{
		{"400", "INVALID_VMID"},
		{"401", "MISSING_TOKEN"},
		{"403", "INSTANCE_ACCESS_DENIED"},
		{"404", "INSTANCE_NOT_FOUND"},
		{"409", "INSTANCE_BUSY"},
		{"500", "INTERNAL_ERROR"},
		{"502", "PROXMOX_UNAVAILABLE"},
		{"504", "PROXMOX_TIMEOUT"},
	}

	for _, ruta := range []string{"/instances/{vmid}/start", "/instances/{vmid}/stop"} {
		item := objeto(t, paths, ruta)
		operacion := objeto(t, item, "post")
		verificarBearer(t, operacion)
		verificarRespuesta(t, operacion, "202", acceptedDef)

		respuestas := objeto(t, operacion, "responses")
		if len(respuestas) != len(pares)+1 {
			t.Errorf("%s POST documenta %d respuestas, se esperaban %d (202 más ocho errores)",
				ruta, len(respuestas), len(pares)+1)
		}
		for _, par := range pares {
			if _, existe := respuestas[par.codigo]; !existe {
				t.Errorf("%s POST no documenta el estado %s (%s)", ruta, par.codigo, par.errorCode)
				continue
			}
			verificarRespuesta(t, operacion, par.codigo, "http.ErrorResponse")
			descripcion := cadena(t, objeto(t, respuestas, par.codigo), "description")
			if !strings.Contains(descripcion, par.errorCode) {
				t.Errorf("%s %s no nombra %s: %q", ruta, par.codigo, par.errorCode, descripcion)
			}
		}

		// No se inventan verbos: start y stop son POST y nada más.
		for _, verbo := range []string{"get", "put", "patch", "delete"} {
			if _, existe := item[verbo]; existe {
				t.Errorf("%s no debe declarar el verbo %s: el servidor solo registra POST", ruta, verbo)
			}
		}

		// Los códigos de autenticación reales no incluyen los inventados.
		for _, inventado := range []string{"TOKEN_MISSING", "TOKEN_INVALID", "TOKEN_EXPIRED"} {
			if strings.Contains(cadena(t, objeto(t, respuestas, "401"), "description"), inventado) {
				t.Errorf("%s 401 no debe documentar %s: el servidor no lo emite", ruta, inventado)
			}
		}
	}

	// La ambigüedad de PROXMOX_TIMEOUT debe quedar explícita en 504.
	timeout := descripcionRespuesta(t, paths, "/instances/{vmid}/start", "post", "504")
	if !strings.Contains(timeout, "puede haberse aplicado") {
		t.Errorf("504 debe advertir que la acción pudo aplicarse: %q", timeout)
	}

	// INSTANCE_PROTECTED solo existe donde RejectProtectedInstance está registrado: stop.
	stop403 := descripcionRespuesta(t, paths, "/instances/{vmid}/stop", "post", "403")
	if !strings.Contains(stop403, "INSTANCE_PROTECTED") {
		t.Errorf("stop 403 debe documentar INSTANCE_PROTECTED: %q", stop403)
	}
	start403 := descripcionRespuesta(t, paths, "/instances/{vmid}/start", "post", "403")
	if strings.Contains(start403, "INSTANCE_PROTECTED") {
		t.Errorf("start no pasa por RejectProtectedInstance, no debe documentar INSTANCE_PROTECTED: %q", start403)
	}

	// Pause: contrato objetivo, todavía no registrado.
	pause := objeto(t, objeto(t, paths, "/instances/{vmid}/pause"), "post")
	if estado := cadena(t, pause, "x-implementation-status"); estado != "planned" {
		t.Errorf("POST pause debe marcarse planned, obtuvo %q", estado)
	}
	if descripcion := cadena(t, pause, "description"); !strings.Contains(descripcion, "NO OPERATIVO") {
		t.Errorf("POST pause no advierte su estado planificado: %q", descripcion)
	}
	verificarRespuesta(t, pause, "202", acceptedDef)

	contenidoContrato, err := os.ReadFile("contrato-etapa1.md")
	if err != nil {
		t.Fatalf("leer contrato-etapa1.md: %v", err)
	}
	contrato := string(contenidoContrato)
	for _, fragmento := range []string{
		// T01 — lecturas.
		"GET /api/node/status", "planificada", "404 NOT_FOUND", "GET /api/instances", "operativa",
		"usagePercent", "instancesSummary", "fetchedAt", "nivelAcceso", "activeTask",
		// T02 — acciones aceptadas.
		"POST /api/instances/:vmid/start", "POST /api/instances/:vmid/stop",
		"202", "upid", "tareaId", "pause",
		// T02 — los ocho pares error/status.
		"INVALID_VMID", "MISSING_TOKEN", "INSTANCE_ACCESS_DENIED", "INSTANCE_PROTECTED",
		"INSTANCE_NOT_FOUND", "INSTANCE_BUSY", "INTERNAL_ERROR",
		"PROXMOX_UNAVAILABLE", "PROXMOX_TIMEOUT",
		// T02 — sobre real y diferencias con lo solicitado.
		`{ "errorCode", "message" }`, "204",
	} {
		if !strings.Contains(contrato, fragmento) {
			t.Errorf("contrato-etapa1.md no contiene %q", fragmento)
		}
	}
}

// descripcionRespuesta devuelve la descripción de un estado de una operación.
func descripcionRespuesta(t *testing.T, paths map[string]any, ruta, verbo, codigo string) string {
	t.Helper()
	operacion := objeto(t, objeto(t, paths, ruta), verbo)
	respuesta := objeto(t, objeto(t, operacion, "responses"), codigo)
	return cadena(t, respuesta, "description")
}

func requeridosDe(t *testing.T, definicion map[string]any) map[string]bool {
	t.Helper()
	requeridos := map[string]bool{}
	requeridosRaw, ok := definicion["required"].([]any)
	if !ok {
		return requeridos
	}
	for _, valor := range requeridosRaw {
		if campo, ok := valor.(string); ok {
			requeridos[campo] = true
		}
	}
	return requeridos
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
