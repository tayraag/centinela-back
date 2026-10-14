package services

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
)

// auditServiceImpl implementa ports.AuditService.
type auditServiceImpl struct {
	repo ports.AuditRepository
}

// NewAuditService crea una nueva instancia del servicio de auditoría.
func NewAuditService(repo ports.AuditRepository) ports.AuditService {
	return &auditServiceImpl{repo: repo}
}

// ==========================================
// Registrar
// ==========================================

// Registrar inserta un registro de auditoría de forma inmutable.
// En caso de error, solo lo loguea — no interrumpe la operación principal (fallo silencioso).
func (s *auditServiceImpl) Registrar(ctx context.Context, input ports.RegistrarAuditoriaInput) {
	if err := s.repo.Registrar(ctx, input); err != nil {
		log.Printf("[AUDIT] error al registrar auditoría | usuario=%s | accion=%s | error=%v",
			input.UsuarioID, input.Accion, err)
	}
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

	return s.repo.Listar(ctx, orgID, filtros, opciones)
}

// ==========================================
// ExportarCSV
// ==========================================

// ExportarCSV construye y devuelve un archivo CSV con todos los registros que cumplen los filtros.
func (s *auditServiceImpl) ExportarCSV(ctx context.Context, orgID uuid.UUID, filtros ports.FiltrosAuditoria) ([]byte, error) {
	registros, err := s.repo.ExportarRegistros(ctx, orgID, filtros)
	if err != nil {
		return nil, fmt.Errorf("error al obtener registros para exportar: %w", err)
	}

	var buf bytes.Buffer
	// UTF-8 BOM para compatibilidad con Excel
	buf.Write([]byte{0xEF, 0xBB, 0xBF})

	w := csv.NewWriter(&buf)

	// Encabezados
	if err := w.Write([]string{
		"ID", "UsuarioID", "NombreUsuario", "FechaHora",
		"Accion", "InstanciaID", "InstanciaNombre", "Resultado", "Detalles",
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
			r.Resultado,
			r.Detalles,
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
	registros, err := s.repo.ExportarRegistros(ctx, orgID, filtros)
	if err != nil {
		return nil, fmt.Errorf("error al obtener registros para exportar: %w", err)
	}

	data, err := json.MarshalIndent(registros, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("error al serializar JSON: %w", err)
	}

	return data, nil
}

// PrepararAuditoriaTarea construye ambos registros RF-08. UPID permanece en el JSONB
// existente: no requiere agregar columnas a la tabla particionada.
// Un cierre sin exitstatus OK se audita FAILED, incluso si Proxmox informa
// advertencias que el contrato de tareas/SSE considera COMPLETED.
func (s *auditServiceImpl) PrepararAuditoriaTarea(t *domain.TareaAsincrona, cierre map[string]any, ahora time.Time) ports.RegistrarAuditoriaInput {
	acciones := map[string]string{
		"start": ports.AccionIniciarVM, "stop": ports.AccionDetenerVM,
		"shutdown": ports.AccionApagarVM, "reboot": ports.AccionReiniciarVM, "delete": ports.AccionEliminarVM,
		ports.AccionProxmoxCrearVM: ports.AccionCrearVM, ports.AccionProxmoxCrearLXC: ports.AccionCrearLXC,
	}
	accion := acciones[strings.ToLower(t.Accion)]
	if accion == "" {
		accion = t.Accion
	}
	meta := map[string]any{}
	if t.Metadatos != nil {
		_ = json.Unmarshal([]byte(*t.Metadatos), &meta)
	}
	nombre, _ := meta["instanciaNombre"].(string)
	detalles := map[string]any{
		"upid": t.UpidProxmox, "tareaId": t.ID.String(), "instanciaNombre": nombre,
	}
	if tipo, ok := meta["resource_type"]; ok {
		detalles["resource_type"] = tipo
	}
	resultado := ports.ResultadoPendiente
	if cierre != nil {
		resultado = ports.ResultadoFailed
		if cierre["exitstatus"] == "OK" && cierre["motivo"] == nil {
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
		InstanciaNombre: nombre, Resultado: resultado, Detalles: detalles,
	}
}
