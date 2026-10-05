package docs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"el-centinela/internal/core/ports"
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
	// GET /node/status pasó de planificado a operativo (adaptador de telemetría del nodo).
	nodeStatus := objeto(t, objeto(t, paths, "/node/status"), "get")
	if _, planificado := nodeStatus["x-implementation-status"]; planificado {
		t.Fatalf("GET /node/status ya está implementado: no debe marcarse x-implementation-status")
	}
	if descripcion := cadena(t, nodeStatus, "description"); !strings.Contains(descripcion, "OPERATIVO") || strings.Contains(descripcion, "NO OPERATIVO") {
		t.Fatalf("GET /node/status debe declarar estado operativo: %q", descripcion)
	}
	verificarBearer(t, nodeStatus)
	verificarRespuesta(t, nodeStatus, "200", "http.EstadoNodeResponse")
	verificarRespuesta(t, nodeStatus, "502", "http.ErrorResponse")
	verificarRespuesta(t, nodeStatus, "504", "http.ErrorResponse")

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

	// INSTANCE_PROTECTED existe donde RejectProtectedInstance está registrado en
	// cmd/api/main.go: start, stop y el endpoint genérico de estado (desde el
	// commit 8591e90, "VMIDs protegidos en acciones de energía").
	for _, ruta := range []string{"/instances/{vmid}/start", "/instances/{vmid}/stop", "/instances/{vmid}/status/{action}"} {
		if r403 := descripcionRespuesta(t, paths, ruta, "post", "403"); !strings.Contains(r403, "INSTANCE_PROTECTED") {
			t.Errorf("%s 403 debe documentar INSTANCE_PROTECTED: %q", ruta, r403)
		}
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

	// ==========================================================
	// T03 — Stream SSE: payload, ticket de un solo uso y framing
	// ==========================================================

	stream := objeto(t, objeto(t, paths, "/events"), "get")

	// El stream no usa BearerAuth: EventSource no manda Authorization y el
	// servidor no registra ese middleware en la ruta. El ticket lo reemplaza.
	if seguridad, existe := stream["security"]; existe {
		t.Errorf("GET /events no debe declarar security: se autentica con el ticket de un solo uso, no %#v", seguridad)
	}
	verificarProduce(t, stream, "text/event-stream")

	// El ticket viaja en la query porque es lo único que EventSource puede mandar.
	verificarParametro(t, stream, "ticket", "query", true)

	// El data: del stream se documenta en http.* como el resto del contrato,
	// no en ports.* como estaba referenciado antes.
	verificarRespuesta(t, stream, "200", "http.SSEEventPayload")
	verificarRespuesta(t, stream, "401", "http.ErrorResponse")
	verificarRespuesta(t, stream, "503", "http.ErrorResponse")

	// Los dos 401 y el 503 son los que el handler emite de verdad.
	missing := descripcionRespuesta(t, paths, "/events", "get", "401")
	for _, codigo := range []string{"EVENTS_TICKET_MISSING", "EVENTS_TICKET_INVALID"} {
		if !strings.Contains(missing, codigo) {
			t.Errorf("GET /events 401 no nombra %s: %q", codigo, missing)
		}
	}
	if inaccesible := descripcionRespuesta(t, paths, "/events", "get", "503"); !strings.Contains(inaccesible, "EVENTS_UNAVAILABLE") {
		t.Errorf("GET /events 503 debe documentar EVENTS_UNAVAILABLE: %q", inaccesible)
	}

	const payloadDef = "http.SSEEventPayload"
	verificarObjetoRequerido(t, definitions, payloadDef, []string{
		"id", "tipo", "severidad", "recursoTipo", "recursoId", "mensaje", "fechaHora",
	})

	// detalles se omite cuando el evento no trae datos extra.
	if requeridosDe(t, objeto(t, definitions, payloadDef))["detalles"] {
		t.Error("detalles no debe ser obligatorio: se omite con omitempty")
	}

	if formato := cadena(t, propiedad(t, definitions, payloadDef, "fechaHora"), "format"); formato != "date-time" {
		t.Errorf("fechaHora debe usar formato date-time/RFC3339, obtuvo %q", formato)
	}

	// Los tres enums traveling en el sobre, con los valores exactos de ports.
	verificarEnum(t, definitions, payloadDef, "tipo",
		"INSTANCE_STATE_CHANGED", "INSTANCE_CREATED", "RESOURCE_SATURATION", "TASK_FINISHED")
	verificarEnum(t, definitions, payloadDef, "severidad", "INFO", "WARNING", "CRITICAL")
	verificarEnum(t, definitions, payloadDef, "recursoTipo", "VM", "LXC", "NODE")

	// detalles es un objeto propio y todas sus claves son opcionales: su forma
	// depende del tipo de evento.
	detalles := propiedad(t, definitions, payloadDef, "detalles")
	if ref := cadena(t, detalles, "$ref"); ref != "#/definitions/http.SSEDetallesEvento" {
		t.Fatalf("detalles debe referenciar http.SSEDetallesEvento, obtuvo %q", ref)
	}
	const detallesDef = "http.SSEDetallesEvento"
	requeridosDetalles := requeridosDe(t, objeto(t, definitions, detallesDef))
	propiedadesDetalles := objeto(t, objeto(t, definitions, detallesDef), "properties")
	for _, campo := range []string{"tareaId", "estado", "accion", "error"} {
		if _, ok := propiedadesDetalles[campo]; !ok {
			t.Errorf("%s no define la propiedad %s", detallesDef, campo)
		}
		if requeridosDetalles[campo] {
			t.Errorf("%s.%s no debe ser obligatorio: el servidor lo omite según el tipo de evento", detallesDef, campo)
		}
	}
	verificarEnum(t, definitions, detallesDef, "estado", "RUNNING", "COMPLETED", "FAILED")

	// Las dos formas concretas de detalles en TASK_FINISHED.
	const successDef = "http.TaskSuccess"
	verificarObjetoRequerido(t, definitions, successDef, []string{"tareaId", "estado", "accion"})
	verificarEnum(t, definitions, successDef, "estado", "COMPLETED")
	if _, existe := objeto(t, objeto(t, definitions, successDef), "properties")["error"]; existe {
		t.Errorf("%s no debe definir error: el servidor solo lo agrega cuando la tarea falla", successDef)
	}

	const failedDef = "http.TaskFailed"
	verificarObjetoRequerido(t, definitions, failedDef, []string{"tareaId", "estado", "accion"})
	verificarEnum(t, definitions, failedDef, "estado", "FAILED")
	errorFallido := propiedad(t, definitions, failedDef, "error")
	if tipo := cadena(t, errorFallido, "type"); tipo != "string" {
		t.Errorf("%s.error debe ser string, obtuvo %q", failedDef, tipo)
	}
	if nullable, ok := errorFallido["x-nullable"].(bool); !ok || !nullable {
		t.Errorf("%s.error debe declarar x-nullable: true", failedDef)
	}
	if requeridosDe(t, objeto(t, definitions, failedDef))["error"] {
		t.Errorf("%s.error no debe ser obligatorio: puede venir vacío", failedDef)
	}

	// El corte del backend viaja como evento `cierre` con su propio motivo.
	const cierreDef = "http.SSECierrePayload"
	verificarObjetoRequerido(t, definitions, cierreDef, []string{"motivo"})
	verificarEnum(t, definitions, cierreDef, "motivo", "LOGOUT", "SESSIONS_REVOKED", "USER_INACTIVE")

	// Pedir el ticket sí va autenticado: ahí el Bearer funciona sin problema.
	ticket := objeto(t, objeto(t, paths, "/events/ticket"), "post")
	verificarBearer(t, ticket)
	verificarRespuesta(t, ticket, "200", "http.SSETicketResponse")
	verificarObjetoRequerido(t, definitions, "http.SSETicketResponse", []string{"ticket"})
	verificarRespuesta(t, ticket, "401", "http.ErrorResponse")
	verificarRespuesta(t, ticket, "503", "http.ErrorResponse")
	if revocado := descripcionRespuesta(t, paths, "/events/ticket", "post", "401"); !strings.Contains(revocado, "TOKEN_REVOKED") {
		t.Errorf("POST /events/ticket 401 debe documentar TOKEN_REVOKED: %q", revocado)
	}
	if inaccesible := descripcionRespuesta(t, paths, "/events/ticket", "post", "503"); !strings.Contains(inaccesible, "EVENTS_UNAVAILABLE") {
		t.Errorf("POST /events/ticket 503 debe documentar EVENTS_UNAVAILABLE: %q", inaccesible)
	}

	// El framing del stream queda escrito en la descripción de la operación.
	descripcionStream := cadena(t, stream, "description")
	for _, fragmento := range []string{
		"data:", "id:", "event: cierre", ": ping", ": conectado",
		"un solo uso", "30 s", "Cache-Control: no-cache", "X-Accel-Buffering: no",
	} {
		if !strings.Contains(descripcionStream, fragmento) {
			t.Errorf("la descripción de GET /events no documenta el framing %q: %q", fragmento, descripcionStream)
		}
	}

	// El stream ya no debe exponer el esquema crudo de ports.
	if _, existe := definitions["ports.RealtimeEvent"]; existe {
		t.Error("ports.RealtimeEvent no debe seguir en definitions: el contrato SSE vive en http.*")
	}

	// Las variantes concretas no tienen respuesta propia en el stream, así
	// que se publican desde una entrada marcada como solo documentación. No puede
	// declarar seguridad ni quedar como una ruta invocable.
	const refRuta = "/events/contrato-sse"
	referencia := objeto(t, objeto(t, paths, refRuta), "get")
	if estado := cadena(t, referencia, "x-implementation-status"); estado != "documentation-only" {
		t.Errorf("%s debe marcarse documentation-only, obtuvo %q", refRuta, estado)
	}
	if seguridad, existe := referencia["security"]; existe {
		t.Errorf("%s no es una ruta real: no debe declarar security, no %#v", refRuta, seguridad)
	}
	descripcionReferencia := cadena(t, referencia, "description")
	if !strings.Contains(descripcionReferencia, "No es una ruta del servidor") {
		t.Errorf("%s debe aclarar que no es una ruta del servidor: %q", refRuta, descripcionReferencia)
	}
	verificarRespuesta(t, referencia, "200", "http.SSETiposPayload")

	// El índice arrastra las tres formas concretas para que swag las emita.
	verificarObjetoRequerido(t, definitions, "http.SSETiposPayload", []string{"taskSuccess", "taskFailed", "cierre"})

	// ==========================================================
	// T03 — el contrato documentado tiene que coincidir con el servidor
	// ==========================================================

	// Los enums de Swagger se comparan contra las constantes que realmente
	// valida ports, no contra una copia escrita a mano: si el backend agrega un
	// valor, esta comparación lo delata.
	tipos := []string{
		ports.EventoInstanciaEstado, ports.EventoInstanciaCreada,
		ports.EventoSaturacion, ports.EventoTareaFinalizada,
	}
	verificarEnum(t, definitions, payloadDef, "tipo", tipos...)
	verificarEnum(t, definitions, payloadDef, "severidad",
		ports.SeveridadInfo, ports.SeveridadWarning, ports.SeveridadCritical)
	verificarEnum(t, definitions, payloadDef, "recursoTipo",
		ports.RecursoVM, ports.RecursoLXC, ports.RecursoNodo)
	verificarEnum(t, definitions, detallesDef, "estado",
		ports.TareaRunning, ports.TareaCompleted, ports.TareaFailed)

	// Y el sobre real, serializado como lo hace el handler, tiene que tener
	// exactamente las claves documentadas: ni una de más ni una de menos.
	serializar := func(evento ports.RealtimeEvent) map[string]any {
		t.Helper()
		crudo, err := json.Marshal(evento)
		if err != nil {
			t.Fatalf("serializar el evento: %v", err)
		}
		var sobre map[string]any
		if err := json.Unmarshal(crudo, &sobre); err != nil {
			t.Fatalf("decodificar el evento: %v", err)
		}
		return sobre
	}

	const mensajeOk = "La tarea de encendido finalizó correctamente"
	const mensajeFallo = "La tarea de apagado falló"

	eventoOk, err := ports.NewRealtimeEvent(ports.EventoTareaFinalizada, ports.SeveridadInfo, mensajeOk)
	if err != nil {
		t.Fatalf("construir el evento de éxito: %v", err)
	}
	// Se arma igual que seguimiento_tareas.eventoTareaFinalizada.
	eventoOk = eventoOk.ConRecurso(ports.RecursoVM, "110").ConDetalles(map[string]any{
		"tareaId": "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
		"estado":  ports.TareaCompleted,
		"accion":  "start",
	})

	eventoFallo, err := ports.NewRealtimeEvent(ports.EventoTareaFinalizada, ports.SeveridadWarning, mensajeFallo)
	if err != nil {
		t.Fatalf("construir el evento de fallo: %v", err)
	}
	eventoFallo = eventoFallo.ConRecurso(ports.RecursoLXC, "201").ConDetalles(map[string]any{
		"tareaId": "3f2504e0-4f89-11d3-9a0c-0305e82c3302",
		"estado":  ports.TareaFailed,
		"accion":  "stop",
		"error":   "CT 201 not running",
	})

	compararClaves(t, serializar(eventoOk), payloadDef, definitions, "sobre de éxito")
	compararClaves(t, serializar(eventoFallo), payloadDef, definitions, "sobre de fallo")

	compararClaves(t, serializar(eventoOk)["detalles"], successDef, definitions, "detalles de éxito")
	compararClaves(t, serializar(eventoFallo)["detalles"], failedDef, definitions, "detalles de fallo")

	// El sobre real no puede tener claves que el contrato no declare.
	compararTodasLasClaves(t, serializar(eventoOk), payloadDef, definitions, "sobre de éxito")
	compararTodasLasClaves(t, serializar(eventoFallo), payloadDef, definitions, "sobre de fallo")
	compararTodasLasClaves(t, serializar(eventoOk)["detalles"], successDef, definitions, "detalles de éxito")
	compararTodasLasClaves(t, serializar(eventoFallo)["detalles"], failedDef, definitions, "detalles de fallo")

	contenidoContrato, err := os.ReadFile("contrato-etapa1.md")
	if err != nil {
		t.Fatalf("leer contrato-etapa1.md: %v", err)
	}
	contrato := string(contenidoContrato)
	for _, fragmento := range []string{
		// T01 — lecturas.
		"GET /api/node/status", "node:status:current", "node:status:last_known", "stale",
		"planificada", "404 NOT_FOUND", "GET /api/instances", "operativa",
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
		// T03 — stream SSE, ticket y framing.
		"GET /api/events", "POST /api/events/ticket", "http.SSEEventPayload",
		"http.TaskSuccess", "http.TaskFailed", "http.SSEDetallesEvento",
		"http.SSETicketResponse", "http.SSECierrePayload", "http.SSETiposPayload",
		"EVENTS_TICKET_MISSING", "EVENTS_TICKET_INVALID", "EVENTS_UNAVAILABLE",
		"TOKEN_REVOKED", "LOGOUT", "SESSIONS_REVOKED", "USER_INACTIVE",
		"un solo uso", "30 s", "text/event-stream", "TASK_FINISHED",
	} {
		if !strings.Contains(contrato, fragmento) {
			t.Errorf("contrato-etapa1.md no contiene %q", fragmento)
		}
	}
}

// compararClaves exige que el objeto real tenga, al menos, todas las claves que
// la definición declara obligatorias.
func compararClaves(t *testing.T, real any, definicion string, definitions map[string]any, contexto string) {
	t.Helper()
	objetoReal := objetoReal(t, real, contexto)
	for campo := range requeridosDe(t, objeto(t, definitions, definicion)) {
		if _, existe := objetoReal[campo]; !existe {
			t.Errorf("el %s real no trae la clave obligatoria %s de %s", contexto, campo, definicion)
		}
	}
}

// compararTodasLasClaves exige que el objeto real no tenga ninguna clave fuera
// de la definición. Es la dirección inversa de compararClaves: evita que el
// servidor empiece a mandar campos que el frontend no conoce.
func compararTodasLasClaves(t *testing.T, real any, definicion string, definitions map[string]any, contexto string) {
	t.Helper()
	objetoReal := objetoReal(t, real, contexto)
	declaradas := objeto(t, objeto(t, definitions, definicion), "properties")
	for campo := range objetoReal {
		if _, existe := declaradas[campo]; !existe {
			t.Errorf("el %s real manda la clave %q, que %s no documenta", contexto, campo, definicion)
		}
	}
}

// objetoReal exige que el valor serializado sea un objeto JSON.
func objetoReal(t *testing.T, real any, contexto string) map[string]any {
	t.Helper()
	objetoReal, ok := real.(map[string]any)
	if !ok {
		t.Fatalf("el %s no es un objeto: %#v", contexto, real)
	}
	return objetoReal
}

// verificarProduce comprueba el media type que declara la operación.
func verificarProduce(t *testing.T, operacion map[string]any, mediaType string) {
	t.Helper()
	produce, ok := operacion["produces"].([]any)
	if !ok || len(produce) != 1 {
		t.Fatalf("produces debe declarar %q: %#v", mediaType, operacion["produces"])
	}
	if declarado, ok := produce[0].(string); !ok || declarado != mediaType {
		t.Fatalf("produce declara %q, se esperaba %q", declarado, mediaType)
	}
}

// verificarParametro comprueba que la operación declare un parámetro concreto.
func verificarParametro(t *testing.T, operacion map[string]any, nombre, lugar string, obligatorio bool) {
	t.Helper()
	parametros, ok := operacion["parameters"].([]any)
	if !ok {
		t.Fatalf("la operación no declara parámetros: %#v", operacion["parameters"])
	}
	for _, bruto := range parametros {
		parametro, ok := bruto.(map[string]any)
		if !ok || parametro["name"] != nombre {
			continue
		}
		if in := cadena(t, parametro, "in"); in != lugar {
			t.Errorf("el parámetro %s debe ir en %s, obtuvo %q", nombre, lugar, in)
		}
		if tipo := cadena(t, parametro, "type"); tipo != "string" {
			t.Errorf("el parámetro %s debe ser string, obtuvo %q", nombre, tipo)
		}
		if _, esBool := parametro["required"].(bool); esBool != obligatorio {
			t.Errorf("el parámetro %s required debe ser %v, obtuvo %#v", nombre, obligatorio, parametro["required"])
		}
		return
	}
	t.Errorf("la operación no declara el parámetro %s", nombre)
}

// verificarEnum comprueba que la propiedad declare exactamente los valores dados.
func verificarEnum(t *testing.T, definitions map[string]any, definicion, campo string, esperados ...string) {
	t.Helper()
	enum, ok := propiedad(t, definitions, definicion, campo)["enum"].([]any)
	if !ok {
		t.Fatalf("%s.%s no declara enum: %#v", definicion, campo, propiedad(t, definitions, definicion, campo))
	}
	obtenidos := make([]string, 0, len(enum))
	for _, valor := range enum {
		texto, ok := valor.(string)
		if !ok {
			t.Fatalf("%s.%s declara un enum no textual: %#v", definicion, campo, valor)
		}
		obtenidos = append(obtenidos, texto)
	}
	if strings.Join(obtenidos, ",") != strings.Join(esperados, ",") {
		t.Errorf("%s.%s declara enum %v, se esperaba %v", definicion, campo, obtenidos, esperados)
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
