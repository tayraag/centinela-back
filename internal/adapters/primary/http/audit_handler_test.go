package http_test

import (
	"el-centinela/internal/adapters/primary/http/middleware"
	"time"

	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	handlers "el-centinela/internal/adapters/primary/http"
	"el-centinela/internal/core/ports"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type auditFiltros struct {
	ports.AuditService
	filtros ports.FiltrosAuditoria
	pagina  *ports.PaginaAuditoria
}

func (a *auditFiltros) ListarAuditoria(_ context.Context, _ uuid.UUID, filtros ports.FiltrosAuditoria, _ ports.OpcionesAuditoria) (*ports.PaginaAuditoria, error) {
	a.filtros = filtros
	if a.pagina != nil {
		return a.pagina, nil
	}
	return &ports.PaginaAuditoria{Items: []ports.AuditoriaDTO{}}, nil
}

func TestAuditHandlerExcluirAccion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	casos := []struct {
		nombre   string
		query    string
		esperado []string
	}{
		{"un solo valor", "excluirAccion=EVENTOS_CONEXION", []string{"EVENTOS_CONEXION"}},
		{"separado por comas", "excluirAccion=EVENTOS_CONEXION,EVENTOS_CIERRE", []string{"EVENTOS_CONEXION", "EVENTOS_CIERRE"}},
		{"repetido en la query", "excluirAccion=EVENTOS_CONEXION&excluirAccion=EVENTOS_CIERRE", []string{"EVENTOS_CONEXION", "EVENTOS_CIERRE"}},
		{"con espacios y vacíos", "excluirAccion=%20EVENTOS_CONEXION%20,,EVENTOS_CIERRE", []string{"EVENTOS_CONEXION", "EVENTOS_CIERRE"}},
		{"sin exclusión no filtra nada", "accion=LOGIN", nil},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			service := &auditFiltros{}
			router := gin.New()
			router.GET("/api/admin/audit", handlers.NewAuditHandler(service).ListarAuditoria)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/audit?"+caso.query, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("status inesperado: %d", w.Code)
			}
			obtenido := service.filtros.ExcluirAcciones
			if len(obtenido) != len(caso.esperado) {
				t.Fatalf("ExcluirAcciones = %v, se esperaba %v", obtenido, caso.esperado)
			}
			for i := range caso.esperado {
				if obtenido[i] != caso.esperado[i] {
					t.Fatalf("ExcluirAcciones = %v, se esperaba %v", obtenido, caso.esperado)
				}
			}
		})
	}
}

func TestAuditHandlerFiltrosRF08(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, caso := range []struct{ accion, resultado string }{
		{ports.AccionCrearInstancia, "PENDIENTE"},
		{ports.AccionCrearInstancia, "SUCCESS"},
		{ports.AccionCrearInstancia, "FAILED"},
		{ports.AccionConfigurarInstancia, "SUCCESS"},
		{ports.AccionConfigurarInstancia, "FAILED"},
	} {
		t.Run(caso.accion+"/"+caso.resultado, func(t *testing.T) {
			service := &auditFiltros{}
			router := gin.New()
			router.GET("/api/admin/audit", handlers.NewAuditHandler(service).ListarAuditoria)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/audit?accion="+caso.accion+"&resultado="+caso.resultado, nil))
			if w.Code != http.StatusOK || service.filtros.Accion != caso.accion || service.filtros.Resultado != caso.resultado {
				t.Fatalf("filtros perdidos: status=%d filtros=%+v", w.Code, service.filtros)
			}
		})
	}
}

func TestAuditHandlerDevuelveDetallesComoObjetoJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &auditFiltros{pagina: &ports.PaginaAuditoria{
		Total: 1, Pagina: 1,
		Items: []ports.AuditoriaDTO{{
			ID: uuid.New(), Accion: ports.AccionCrearInstancia, Resultado: ports.ResultadoPendiente,
			Detalles: json.RawMessage(`{"tareaId":"tarea-123","cores":4}`),
		}},
	}}
	router := gin.New()
	router.GET("/api/admin/audit", handlers.NewAuditHandler(service).ListarAuditoria)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/audit", nil))

	var respuesta struct {
		Items []struct {
			Detalles map[string]any `json:"detalles"`
		} `json:"items"`
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status inesperado: %d", w.Code)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &respuesta); err != nil {
		t.Fatalf("la respuesta no contiene detalles como objeto JSON: %v; body=%s", err, w.Body.String())
	}
	if len(respuesta.Items) != 1 || respuesta.Items[0].Detalles["tareaId"] != "tarea-123" || respuesta.Items[0].Detalles["cores"] != float64(4) {
		t.Fatalf("detalles incompletos: %+v", respuesta.Items)
	}
}

func TestRF08ConsultaValidada(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, query := range []string{"usuarioId=incorrecto", "desde=ayer", "desde=2026-10-21&hasta=2026-10-20", "recursoTipo=SQL", "resultado=OK", "page=0", "limit=-1"} {
		t.Run(query, func(t *testing.T) {
			r := gin.New()
			r.GET("/audit", handlers.NewAuditHandler(&auditFiltros{}).ListarAuditoria)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/audit?"+query, nil))
			if w.Code != 400 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
	svc := &auditFiltros{}
	r := gin.New()
	r.GET("/audit", handlers.NewAuditHandler(svc).ListarAuditoria)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/audit?desde=2026-10-01T10:30:00Z&hasta=2026-10-20&recursoTipo=VM&recursoId=105&limit=200&page=2", nil))
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	if svc.filtros.RecursoTipo != "VM" || svc.filtros.RecursoID != "105" || svc.filtros.Desde.Hour() != 10 || svc.filtros.Hasta.Nanosecond() != 999999999 {
		t.Fatalf("filtros=%+v", svc.filtros)
	}
	if !svc.filtros.Hasta.Before(time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("hasta fuera de rango")
	}
	var body map[string]any
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["page"] != float64(2) || body["limit"] != float64(100) {
		t.Fatalf("respuesta=%v", body)
	}
}
func TestRF08OperatorForbidden(t *testing.T) {
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.ContextKeyRol, "OPERATOR") })
	r.GET("/api/admin/audit", middleware.RequireRole("ADMIN"), handlers.NewAuditHandler(&auditFiltros{}).ListarAuditoria)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/audit", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestRF08ContratoPaginado(t *testing.T) {
	svc := &auditFiltros{pagina: &ports.PaginaAuditoria{Total: 1420, Items: []ports.AuditoriaDTO{{NombreUsuario: "admin", RecursoTipo: "VM", RecursoID: "105", Detalles: json.RawMessage(`{"snapname":"pre-deploy"}`)}}}}
	r := gin.New()
	r.GET("/audit", handlers.NewAuditHandler(svc).ListarAuditoria)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/audit", nil))
	var body struct {
		Page, Limit, TotalPages int
		Items                   []struct {
			UsuarioNombre, RecursoTipo, RecursoId string
			Detalle                               map[string]any
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Page != 1 || body.Limit != 25 || body.TotalPages != 57 || len(body.Items) != 1 {
		t.Fatalf("body=%s", w.Body)
	}
	item := body.Items[0]
	if item.UsuarioNombre != "admin" || item.RecursoTipo != "VM" || item.RecursoId != "105" || item.Detalle["snapname"] != "pre-deploy" {
		t.Fatalf("item=%+v", item)
	}
}

type cursorExportTest struct {
	ctx              context.Context
	cantidad, leidas int
	cerrado          bool
	errorLectura     error
	pausa            time.Duration
}

func (c *cursorExportTest) Next() bool {
	if c.ctx.Err() != nil {
		return false
	}
	if c.leidas >= c.cantidad {
		return false
	}
	c.leidas++
	return true
}
func (c *cursorExportTest) Actual() (ports.AuditoriaDTO, error) {
	if c.pausa > 0 {
		select {
		case <-time.After(c.pausa):
		case <-c.ctx.Done():
			return ports.AuditoriaDTO{}, c.ctx.Err()
		}
	}
	return ports.AuditoriaDTO{ID: uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1"), FechaHora: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), Accion: "SNAPSHOT_CREATE", RecursoTipo: "VM", RecursoID: "inst_publica", Resultado: "SUCCESS", Detalles: json.RawMessage(`{"snapname":"área,prueba"}`)}, nil
}
func (c *cursorExportTest) Err() error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	return c.errorLectura
}
func (c *cursorExportTest) Close() error { c.cerrado = true; return nil }

type servicioExportTest struct {
	ports.AuditService
	cursor  *cursorExportTest
	err     error
	abierto bool
	filtros ports.FiltrosAuditoria
}

func (s *servicioExportTest) AbrirExportacion(ctx context.Context, _ uuid.UUID, f ports.FiltrosAuditoria) (ports.CursorAuditoria, error) {
	s.abierto = true
	s.filtros = f
	if s.err != nil {
		return nil, s.err
	}
	s.cursor.ctx = ctx
	return s.cursor, nil
}
func routerExportTest(s *servicioExportTest, rol string) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.ContextKeyRol, rol) })
	r.GET("/api/admin/audit/export", middleware.RequireRole("ADMIN"), handlers.NewAuditHandler(s).ExportarAuditoria)
	return r
}
func TestAuditExportStreamingFormatosYVacio(t *testing.T) {
	for _, formato := range []string{"csv", "json"} {
		for _, cantidad := range []int{0, 3, 501} {
			t.Run(formato+"/"+fmt.Sprint(cantidad), func(t *testing.T) {
				s := &servicioExportTest{cursor: &cursorExportTest{cantidad: cantidad}}
				w := httptest.NewRecorder()
				routerExportTest(s, "ADMIN").ServeHTTP(w, httptest.NewRequest("GET", "/api/admin/audit/export?format="+formato, nil))
				if w.Code != 200 || !s.cursor.cerrado || s.cursor.leidas != cantidad {
					t.Fatalf("status=%d cursor=%+v", w.Code, s.cursor)
				}
				patron := `attachment; filename="auditoria_centinela_[0-9]{8}_[0-9]{4}\.` + formato + `"`
				if !regexp.MustCompile(patron).MatchString(w.Header().Get("Content-Disposition")) {
					t.Fatal(w.Header())
				}
				if formato == "csv" {
					if w.Header().Get("Content-Type") != "text/csv; charset=utf-8" || !strings.HasPrefix(w.Body.String(), "\xef\xbb\xbf") {
						t.Fatal("CSV sin UTF8/BOM")
					}
					rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(w.Body.String(), "\xef\xbb\xbf"))).ReadAll()
					if err != nil || len(rows) != cantidad+1 {
						t.Fatalf("csv: filas=%d err=%v", len(rows), err)
					}
					if strings.Join(rows[0], ",") != "id,fecha_hora,usuario_id,accion,recurso_tipo,recurso_id,resultado,detalle" {
						t.Fatal(rows[0])
					}
					if cantidad > 0 && !strings.Contains(rows[1][7], "área,prueba") {
						t.Fatal(rows[1])
					}
				} else {
					var rows []map[string]any
					if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || len(rows) != cantidad {
						t.Fatalf("JSON filas=%d err=%v", len(rows), err)
					}
					if w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
						t.Fatal(w.Header())
					}
				}
			})
		}
	}
}
func TestAuditExportStreamingPermisosYErrores(t *testing.T) {
	for _, caso := range []struct {
		rol, query string
		status     int
	}{
		{"OPERATOR", "", 403}, {"ADMIN", "?format=xml", 400}, {"ADMIN", "?usuarioId=invalido", 400},
	} {
		s := &servicioExportTest{cursor: &cursorExportTest{}}
		w := httptest.NewRecorder()
		routerExportTest(s, caso.rol).ServeHTTP(w, httptest.NewRequest("GET", "/api/admin/audit/export"+caso.query, nil))
		if w.Code != caso.status || s.abierto {
			t.Fatalf("%+v: %d abierto=%v", caso, w.Code, s.abierto)
		}
	}
	s := &servicioExportTest{err: errors.New("base caída")}
	w := httptest.NewRecorder()
	routerExportTest(s, "ADMIN").ServeHTTP(w, httptest.NewRequest("GET", "/api/admin/audit/export", nil))
	if w.Code != 500 || w.Header().Get("Content-Disposition") != "" {
		t.Fatalf("error pre-stream: %d %v", w.Code, w.Header())
	}
	s = &servicioExportTest{cursor: &cursorExportTest{}}
	w = httptest.NewRecorder()
	routerExportTest(s, "ADMIN").ServeHTTP(w, httptest.NewRequest("GET", "/api/admin/audit/export?formato=json", nil))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal("alias formato roto")
	}
}

type writerExportCancel struct {
	*httptest.ResponseRecorder
	cancel  context.CancelFunc
	flushes int
	cursor  *cursorExportTest
}

func (w *writerExportCancel) Flush() {
	w.flushes++
	if w.flushes == 1 && w.cursor.leidas != 0 {
		panic("se leyó el dataset antes del primer flush")
	}
	if w.flushes == 2 {
		w.cancel()
	}
	w.ResponseRecorder.Flush()
}
func TestAuditExportStreamingCancelaYCierraCursor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &servicioExportTest{cursor: &cursorExportTest{cantidad: 60000}}
	w := &writerExportCancel{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, cursor: s.cursor}
	req := httptest.NewRequest("GET", "/api/admin/audit/export?format=json", nil).WithContext(ctx)
	routerExportTest(s, "ADMIN").ServeHTTP(w, req)
	if !s.cursor.cerrado || s.cursor.leidas != 500 {
		t.Fatalf("se leyó de más o quedó abierto: %+v", s.cursor)
	}
	if strings.HasSuffix(strings.TrimSpace(w.Body.String()), "]") {
		t.Fatal("una descarga cancelada no debe completarse")
	}
}
func TestAuditExportStreamingFalloParcialAborta(t *testing.T) {
	s := &servicioExportTest{cursor: &cursorExportTest{cantidad: 1, errorLectura: errors.New("fallo de cursor")}}
	w := httptest.NewRecorder()
	defer func() {
		if r := recover(); r != http.ErrAbortHandler {
			t.Fatalf("se esperaba abortar transporte, vino %v", r)
		}
		if !s.cursor.cerrado {
			t.Fatal("cursor sin cerrar")
		}
		if strings.Contains(w.Body.String(), "EXPORT_ERROR") {
			t.Fatal("se anexó error JSON al archivo")
		}
	}()
	routerExportTest(s, "ADMIN").ServeHTTP(w, httptest.NewRequest("GET", "/api/admin/audit/export?format=json", nil))
}

func TestAuditExportStreamingSuperaTimeoutGlobal(t *testing.T) {
	s := &servicioExportTest{cursor: &cursorExportTest{cantidad: 3, pausa: 40 * time.Millisecond}}
	server := httptest.NewUnstartedServer(routerExportTest(s, "ADMIN"))
	server.Config.WriteTimeout = 20 * time.Millisecond
	server.Start()
	defer server.Close()
	cliente := &http.Client{Timeout: 3 * time.Second}
	response, err := cliente.Get(server.URL + "/api/admin/audit/export?format=json")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var rows []map[string]any
	if err := json.NewDecoder(response.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatal("el timeout global cortó la descarga")
	}
}
