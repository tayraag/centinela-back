package services

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
)

// auditServiceImpl implementa ports.AuditService.
type auditServiceImpl struct {
	repo ports.AuditRepository
	refs ports.ReferenciasInstancia // VMID guardado -> referencia pública en listado y exportaciones
}

// NewAuditService crea una nueva instancia del servicio de auditoría. refs
// traduce el VMID de cada registro a la referencia pública en el listado y las
// exportaciones (y el filtro instanciaId de vuelta al VMID). Sin refs esos
// campos salen vacíos: el VMID nunca se expone.
func NewAuditService(repo ports.AuditRepository, refs ...ports.ReferenciasInstancia) ports.AuditService {
	s := &auditServiceImpl{repo: repo, refs: refsNulas{}}
	if len(refs) > 0 && refs[0] != nil {
		s.refs = refs[0]
	}
	return s
}

// refsNulas no traduce nada: sin traductor configurado, ningún VMID sale.
type refsNulas struct{}

func (refsNulas) OfuscarVMID(int) (string, error)    { return "", errSinReferencias }
func (refsNulas) DesofuscarVMID(string) (int, error) { return 0, errSinReferencias }

var errSinReferencias = errors.New("sin traductor de referencias de instancia")

// filtroInterno traduce el filtro instanciaId (referencia pública) al VMID que
// guarda la tabla. false si la referencia no es válida: no hay registros que
// mostrar (un VMID numérico tampoco se acepta como filtro).
func (s *auditServiceImpl) filtroInterno(filtros ports.FiltrosAuditoria) (ports.FiltrosAuditoria, bool) {
	if filtros.InstanciaID == "" {
		return filtros, true
	}
	vmid, err := s.refs.DesofuscarVMID(filtros.InstanciaID)
	if err != nil {
		return filtros, false
	}
	filtros.InstanciaID = strconv.Itoa(vmid)
	return filtros, true
}

// publicos devuelve una copia de los registros con el VMID reemplazado por la
// referencia pública: instanciaId, resourceId (VM y LXC) y los VMID dentro de
// detalles.
func (s *auditServiceImpl) publicos(originales []ports.AuditoriaDTO) []ports.AuditoriaDTO {
	registros := make([]ports.AuditoriaDTO, len(originales))
	copy(registros, originales)
	for i := range registros {
		r := &registros[i]
		if len(r.Detalles) > 0 {
			r.Detalles = json.RawMessage(ports.DetallesAuditoriaPublicos(s.refs, string(r.Detalles), r.InstanciaID))
		}
		if r.RecursoTipo == ports.RecursoVM || r.RecursoTipo == ports.RecursoLXC {
			r.RecursoID = ports.IDInstanciaPublico(s.refs, r.RecursoID)
		}
		r.InstanciaID = ports.IDInstanciaPublico(s.refs, r.InstanciaID)
	}
	return registros
}

// registrosExportables devuelve los registros de las exportaciones ya traducidos.
func (s *auditServiceImpl) registrosExportables(ctx context.Context, orgID uuid.UUID, filtros ports.FiltrosAuditoria) ([]ports.AuditoriaDTO, error) {
	filtros, valido := s.filtroInterno(filtros)
	if !valido {
		return []ports.AuditoriaDTO{}, nil
	}
	registros, err := s.repo.ExportarRegistros(ctx, orgID, filtros)
	if err != nil {
		return nil, fmt.Errorf("error al obtener registros para exportar: %w", err)
	}
	if registros == nil {
		registros = []ports.AuditoriaDTO{}
	}
	return s.publicos(registros), nil
}

// ==========================================
// Registrar
// ==========================================

// Registrar inserta un registro de auditoría de forma inmutable.
// En caso de error, solo lo loguea — no interrumpe la operación principal (fallo silencioso).
func (s *auditServiceImpl) Registrar(ctx context.Context, input ports.RegistrarAuditoriaInput) {
	if err := s.RegistrarEstricto(ctx, input); err != nil {
		log.Printf("[AUDIT] error al registrar auditoría | usuario=%s | accion=%s | error=%v",
			input.UsuarioID, input.Accion, err)
	}
}

// RegistrarEstricto confirma la escritura de auditoría al llamador. A diferencia
// de Registrar, no oculta los errores de persistencia.
func (s *auditServiceImpl) RegistrarEstricto(ctx context.Context, input ports.RegistrarAuditoriaInput) error {
	if err := s.repo.Registrar(ctx, input); err != nil {
		return fmt.Errorf("no se pudo persistir la auditoría %s: %w", input.Accion, err)
	}
	return nil
}

// ==========================================
// ListarAuditoria
// ==========================================

// ListarAuditoria devuelve el log de auditoría paginado con filtros y ordenamiento.
func (s *auditServiceImpl) ListarAuditoria(ctx context.Context, orgID uuid.UUID, filtros ports.FiltrosAuditoria, opciones ports.OpcionesAuditoria) (*ports.PaginaAuditoria, error) {
	// Sanitizar paginación
	if opciones.Pagina < 1 {
		opciones.Pagina = 1
	}
	if opciones.TamanoPag < 1 || opciones.TamanoPag > 200 {
		opciones.TamanoPag = 50
	}

	// Sanitizar ordenamiento
	camposPermitidos := map[string]bool{
		"fechaHora": true,
		"accion":    true,
		"resultado": true,
	}
	if !camposPermitidos[opciones.OrdenarPor] {
		opciones.OrdenarPor = "fechaHora"
	}
	if opciones.Direccion != "asc" && opciones.Direccion != "desc" {
		opciones.Direccion = "desc"
	}

	filtros, valido := s.filtroInterno(filtros)
	if !valido {
		return &ports.PaginaAuditoria{Pagina: opciones.Pagina, Items: []ports.AuditoriaDTO{}}, nil
	}
	pagina, err := s.repo.Listar(ctx, orgID, filtros, opciones)
	if err != nil || pagina == nil {
		return pagina, err
	}
	pagina.Items = s.publicos(pagina.Items)
	return pagina, nil
}

// ==========================================
// ExportarCSV
// ==========================================

// ExportarCSV construye y devuelve un archivo CSV con todos los registros que cumplen los filtros.
func (s *auditServiceImpl) ExportarCSV(ctx context.Context, orgID uuid.UUID, filtros ports.FiltrosAuditoria) ([]byte, error) {
	registros, err := s.registrosExportables(ctx, orgID, filtros)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	// UTF-8 BOM para compatibilidad con Excel
	buf.Write([]byte{0xEF, 0xBB, 0xBF})

	w := csv.NewWriter(&buf)

	// Encabezados
	if err := w.Write([]string{
		"ID", "UsuarioID", "NombreUsuario", "FechaHora",
		"Accion", "InstanciaID", "InstanciaNombre", "ResourceType", "ResourceID", "Resultado", "Detalles",
	}); err != nil {
		return nil, fmt.Errorf("error al escribir encabezados CSV: %w", err)
	}

	// Filas
	for _, r := range registros {
		usuarioIDStr := ""
		if r.UsuarioID != nil {
			usuarioIDStr = r.UsuarioID.String()
		}
		row := []string{
			r.ID.String(),
			usuarioIDStr,
			r.NombreUsuario,
			r.FechaHora.Format(time.RFC3339),
			r.Accion,
			r.InstanciaID,
			r.InstanciaNombre,
			r.RecursoTipo,
			r.RecursoID,
			r.Resultado,
			string(r.Detalles),
		}
		if err := w.Write(row); err != nil {
			return nil, fmt.Errorf("error al escribir fila CSV: %w", err)
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("error al finalizar CSV: %w", err)
	}

	return buf.Bytes(), nil
}

// ==========================================
// ExportarJSON
// ==========================================

// ExportarJSON construye y devuelve un archivo JSON con todos los registros que cumplen los filtros.
func (s *auditServiceImpl) ExportarJSON(ctx context.Context, orgID uuid.UUID, filtros ports.FiltrosAuditoria) ([]byte, error) {
	registros, err := s.registrosExportables(ctx, orgID, filtros)
	if err != nil {
		return nil, err
	}

	data, err := json.MarshalIndent(registros, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("error al serializar JSON: %w", err)
	}

	return data, nil
}

// PrepararAuditoriaTarea construye los registros append-only de despacho y
// desenlace. Las creaciones se unifican como CREATE_INSTANCE y correlacionan
// ambos registros mediante la columna UPID.
func (s *auditServiceImpl) PrepararAuditoriaTarea(t *domain.TareaAsincrona, cierre map[string]any, ahora time.Time) ports.RegistrarAuditoriaInput {
	acciones := map[string]string{
		"start": ports.AccionIniciarVM, "stop": ports.AccionDetenerVM,
		"shutdown": ports.AccionApagarVM, "reboot": ports.AccionReiniciarVM, "delete": ports.AccionEliminarVM,
	}
	accion := acciones[strings.ToLower(t.Accion)]
	recursoTipo, esCreacion := ports.TipoRecursoCreacion(t.Accion)
	if esCreacion {
		accion = ports.AccionCrearInstancia
	}
	if accion == "" {
		accion = t.Accion
	}
	meta := map[string]any{}
	if t.Metadatos != nil {
		_ = json.Unmarshal([]byte(*t.Metadatos), &meta)
	}
	nombre, _ := meta["instanciaNombre"].(string)
	if esCreacion {
		detalles := map[string]any{"tareaId": t.ID.String()}
		resultado := ports.ResultadoPendiente
		if cierre == nil {
			detalles["nombre"] = nombre
			detalles["tipo"] = meta["resource_type"]
			detalles["cores"] = meta["cores"]
			detalles["ramMb"] = meta["ramMb"]
			detalles["discoGb"] = meta["discoGb"]
		} else {
			resultado = ports.ResultadoFailed
			if cierre["exitstatus"] == "OK" && cierre["motivo"] == nil {
				resultado = ports.ResultadoSuccess
			}
			detalles["exitstatus"] = cierre["exitstatus"]
			detalles["duracionSegundos"] = max(0, ahora.Sub(t.FechaCreacion).Seconds())
			if motivo := cierre["motivo"]; motivo != nil {
				detalles["motivo"] = motivo
			}
			if err := cierre["error"]; err != nil {
				detalles["error"] = err
			}
		}
		return ports.RegistrarAuditoriaInput{
			UsuarioID: t.UsuarioID, Accion: accion, InstanciaID: t.InstanciaID,
			InstanciaNombre: nombre, RecursoTipo: recursoTipo, RecursoID: t.InstanciaID,
			Upid: t.UpidProxmox, Resultado: resultado, Detalles: detalles,
		}
	}
	detalles := map[string]any{"tareaId": t.ID.String(), "instanciaNombre": nombre}
	if tipo, ok := meta["resource_type"]; ok {
		detalles["resource_type"] = tipo
	}
	resultado := ports.ResultadoPendiente
	if cierre != nil {
		resultado = ports.ResultadoFailed
		// Una acción operativa completada con advertencias de Proxmox sigue
		// siendo exitosa: el motivo sólo se completa cuando la tarea falló.
		// Las creaciones conservan la regla estricta de exitstatus "OK".
		if cierre["motivo"] == nil {
			resultado = ports.ResultadoSuccess
		}
		detalles["exitstatus"] = cierre["exitstatus"]
		detalles["motivo"] = cierre["motivo"]
		if resultado == ports.ResultadoFailed && detalles["motivo"] == nil {
			detalles["motivo"] = ports.MotivoProxmoxError
		}
		detalles["duracionSegundos"] = max(0, ahora.Sub(t.FechaCreacion).Seconds())
		if err, ok := cierre["error"]; ok {
			detalles["error"] = err
		}
	}
	return ports.RegistrarAuditoriaInput{
		UsuarioID: t.UsuarioID, Accion: accion, InstanciaID: t.InstanciaID,
		InstanciaNombre: nombre, Upid: t.UpidProxmox, Resultado: resultado, Detalles: detalles,
	}
}
