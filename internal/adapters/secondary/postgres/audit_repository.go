package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// auditRepositoryImpl implementa ports.AuditRepository usando GORM + PostgreSQL.
type auditRepositoryImpl struct {
	db *gorm.DB
}

// NewAuditRepository crea un nuevo repositorio de auditoría.
func NewAuditRepository(db *gorm.DB) ports.AuditRepository {
	return &auditRepositoryImpl{db: db}
}

// ==========================================
// Registrar
// ==========================================

// Registrar inserta un nuevo registro de auditoría (solo INSERT, nunca UPDATE/DELETE).
func (r *auditRepositoryImpl) Registrar(ctx context.Context, input ports.RegistrarAuditoriaInput) error {
	if input.UsuarioID == uuid.Nil && (input.Accion == ports.AccionAlertaDisparada || input.Accion == ports.AccionAlertaResuelta) {
		return r.registrarAlertaSistema(ctx, input)
	}
	return r.registrarConAlcance(ctx, input, nil)
}

func (r *auditRepositoryImpl) registrarConAlcance(ctx context.Context, input ports.RegistrarAuditoriaInput, orgID *uuid.UUID) error {
	// Serializar detalles adicionales a JSON
	var detallesPtr *string
	if len(input.Detalles) > 0 {
		b, err := ports.DetallesAuditoriaSeguros(input.Detalles)
		if err != nil {
			return fmt.Errorf("error al serializar detalles de auditoría: %w", err)
		}
		s := string(b)
		detallesPtr = &s
	}

	var usuarioIDPtr *uuid.UUID
	if input.UsuarioID != uuid.Nil {
		usuarioIDPtr = &input.UsuarioID
	}

	registro := domain.Auditoria{
		OrganizacionID:  orgID,
		UsuarioID:       usuarioIDPtr,
		Accion:          input.Accion,
		InstanciaID:     input.InstanciaID,
		InstanciaNombre: input.InstanciaNombre,
		RecursoTipo:     input.RecursoTipo,
		RecursoID:       input.RecursoID,
		Upid:            input.Upid,
		Resultado:       input.Resultado,
		Detalles:        detallesPtr,
		FechaHora:       time.Now(),
	}

	query := r.db.WithContext(ctx)
	// Compatibilidad durante un rolling deploy: los eventos sin UPID pueden
	// seguir insertándose antes de que la instancia nueva ejecute la migración.
	omitir := make([]string, 0, 4)
	if orgID == nil {
		omitir = append(omitir, "OrganizacionID")
	}
	if input.Upid == "" {
		omitir = append(omitir, "Upid")
	}
	if input.RecursoTipo == "" {
		omitir = append(omitir, "RecursoTipo")
	}
	if input.RecursoID == "" {
		omitir = append(omitir, "RecursoID")
	}
	if len(omitir) > 0 {
		query = query.Omit(omitir...)
	}
	if err := query.Create(&registro).Error; err != nil {
		return fmt.Errorf("error al insertar registro de auditoría: %w", err)
	}
	return nil
}

// ==========================================
// Listar
// ==========================================

// Listar devuelve registros de auditoría paginados con filtros dinámicos.
// Solo retorna registros de usuarios que pertenecen a la organización indicada.
func (r *auditRepositoryImpl) Listar(ctx context.Context, orgID uuid.UUID, filtros ports.FiltrosAuditoria, opciones ports.OpcionesAuditoria) (*ports.PaginaAuditoria, error) {
	query := r.db.WithContext(ctx).
		Table("auditoria a").
		Select("a.*, CASE WHEN a.usuario_id IS NULL THEN '[sistema]' ELSE u.nombre_completo END AS nombre_completo").
		Joins("LEFT JOIN usuarios u ON u.id = a.usuario_id").
		// El alcance explícito autoriza los eventos de sistema. Los registros
		// históricos con actor conservan la atribución anterior por usuario.
		Where("(a.organizacion_id = ? OR (a.organizacion_id IS NULL AND u.organizacion_id = ?))", orgID, orgID)

	// Aplicar filtros opcionales
	query = aplicarFiltros(query, filtros)

	// Contar total antes de paginar
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("error al contar registros de auditoría: %w", err)
	}

	// Mapear nombre de campo a columna SQL para ordenamiento seguro
	columnaOrden := map[string]string{
		"fechaHora": "a.fecha_hora",
		"accion":    "a.accion",
		"resultado": "a.resultado",
	}
	col, ok := columnaOrden[opciones.OrdenarPor]
	if !ok {
		col = "a.fecha_hora"
	}
	direccion := "desc"
	if opciones.Direccion == "asc" {
		direccion = "asc"
	}
	orden := col + " " + direccion + ", a.id " + direccion

	offset := (opciones.Pagina - 1) * opciones.TamanoPag

	// Consulta de datos con paginación
	type resultado struct {
		domain.Auditoria
		NombreCompleto string `gorm:"column:nombre_completo"`
	}
	var rows []resultado
	if err := query.Order(orden).Limit(opciones.TamanoPag).Offset(offset).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("error al listar registros de auditoría: %w", err)
	}

	items := make([]ports.AuditoriaDTO, len(rows))
	for i, row := range rows {
		nombreUsuario := row.NombreCompleto
		if nombreUsuario == "" {
			nombreUsuario = "[usuario eliminado]"
		}
		var detalles json.RawMessage
		if row.Auditoria.Detalles != nil {
			detalles = json.RawMessage([]byte(*row.Auditoria.Detalles))
		}
		items[i] = ports.AuditoriaDTO{
			ID:              row.Auditoria.ID,
			UsuarioID:       row.Auditoria.UsuarioID,
			NombreUsuario:   nombreUsuario,
			FechaHora:       row.Auditoria.FechaHora,
			Accion:          row.Auditoria.Accion,
			InstanciaID:     row.Auditoria.InstanciaID,
			InstanciaNombre: row.Auditoria.InstanciaNombre,
			RecursoTipo:     row.Auditoria.RecursoTipo,
			RecursoID:       row.Auditoria.RecursoID,
			Resultado:       row.Auditoria.Resultado,
			Detalles:        detalles,
		}
	}

	return &ports.PaginaAuditoria{
		Total:  total,
		Pagina: opciones.Pagina,
		Items:  items,
	}, nil
}

// ==========================================
// ExportarRegistros
// ==========================================

// ExportarRegistros devuelve hasta 100.000 registros que cumplen los filtros (sin paginación).
func (r *auditRepositoryImpl) ExportarRegistros(ctx context.Context, orgID uuid.UUID, filtros ports.FiltrosAuditoria) ([]ports.AuditoriaDTO, error) {
	query := r.db.WithContext(ctx).
		Table("auditoria a").
		Select("a.*, CASE WHEN a.usuario_id IS NULL THEN '[sistema]' ELSE u.nombre_completo END AS nombre_completo").
		Joins("LEFT JOIN usuarios u ON u.id = a.usuario_id").
		// El alcance explícito autoriza los eventos de sistema. Los registros
		// históricos con actor conservan la atribución anterior por usuario.
		Where("(a.organizacion_id = ? OR (a.organizacion_id IS NULL AND u.organizacion_id = ?))", orgID, orgID)

	query = aplicarFiltros(query, filtros)

	type resultado struct {
		domain.Auditoria
		NombreCompleto string `gorm:"column:nombre_completo"`
	}
	var rows []resultado
	if err := query.Order("a.fecha_hora DESC").Limit(100000).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("error al exportar registros de auditoría: %w", err)
	}

	items := make([]ports.AuditoriaDTO, len(rows))
	for i, row := range rows {
		nombreUsuario := row.NombreCompleto
		if nombreUsuario == "" {
			nombreUsuario = "[usuario eliminado]"
		}
		var detalles json.RawMessage
		if row.Auditoria.Detalles != nil {
			detalles = json.RawMessage([]byte(*row.Auditoria.Detalles))
		}
		items[i] = ports.AuditoriaDTO{
			ID:              row.Auditoria.ID,
			UsuarioID:       row.Auditoria.UsuarioID,
			NombreUsuario:   nombreUsuario,
			FechaHora:       row.Auditoria.FechaHora,
			Accion:          row.Auditoria.Accion,
			InstanciaID:     row.Auditoria.InstanciaID,
			InstanciaNombre: row.Auditoria.InstanciaNombre,
			RecursoTipo:     row.Auditoria.RecursoTipo,
			RecursoID:       row.Auditoria.RecursoID,
			Resultado:       row.Auditoria.Resultado,
			Detalles:        detalles,
		}
	}
	return items, nil
}

// ==========================================
// Helper: aplicarFiltros
// ==========================================

// aplicarFiltros agrega cláusulas WHERE dinámicas a la query según los filtros presentes.
func aplicarFiltros(query *gorm.DB, filtros ports.FiltrosAuditoria) *gorm.DB {
	// Predicado directo sobre la clave de partición: PostgreSQL puede excluir
	// particiones incluso con parámetros preparados. Nunca castear fecha_hora.
	if filtros.Desde == nil {
		ahora := time.Now().UTC()
		desde := time.Date(ahora.Year(), ((ahora.Month()-1)/3)*3+1, 1, 0, 0, 0, 0, time.UTC)
		if filtros.Hasta != nil && filtros.Hasta.Before(desde) {
			desde = filtros.Hasta.AddDate(-1, 0, 0)
		}
		filtros.Desde = &desde
	}
	if filtros.RecursoTipo != "" {
		query = query.Where("a.resource_type = ?", filtros.RecursoTipo)
	}
	if filtros.RecursoID != "" {
		query = query.Where("a.resource_id = ?", filtros.RecursoID)
	}

	if filtros.UsuarioID != nil {
		query = query.Where("a.usuario_id = ?", filtros.UsuarioID)
	}
	if filtros.Accion != "" {
		query = query.Where("a.accion = ?", filtros.Accion)
	}
	if len(filtros.ExcluirAcciones) > 0 {
		query = query.Where("a.accion NOT IN ?", filtros.ExcluirAcciones)
	}
	if filtros.InstanciaID != "" {
		query = query.Where("a.instancia_id = ?", filtros.InstanciaID)
	}
	if filtros.Resultado != "" {
		query = query.Where("a.resultado = ?", filtros.Resultado)
	}
	if filtros.Desde != nil {
		query = query.Where("a.fecha_hora >= ?", filtros.Desde)
	}
	if filtros.Hasta != nil {
		query = query.Where("a.fecha_hora <= ?", filtros.Hasta)
	}
	return query
}

// registrarAlertaSistema congela el alcance con la política vigente del correo:
// ADMIN activos y operadores FULL_ACCESS de la instancia. Cada organización
// recibe una fila y exclusivamente sus propios destinatarios. Todo es atómico.
func (r *auditRepositoryImpl) registrarAlertaSistema(ctx context.Context, input ports.RegistrarAuditoriaInput) error {
	vmid := 0
	switch input.RecursoTipo {
	case ports.RecursoVM, ports.RecursoLXC:
		var err error
		vmid, err = strconv.Atoi(input.RecursoID)
		if err != nil || vmid <= 0 {
			return fmt.Errorf("recurso de alerta inválido")
		}
	case ports.RecursoHost:
	default:
		return fmt.Errorf("tipo de recurso de alerta inválido")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		type destinatario struct {
			OrganizacionID uuid.UUID
			EmailUsuario   string
		}
		var usuarios []destinatario
		permisos := tx.Model(&domain.PermisoInstancia{}).Select("usuario_id").Where("vmid_proxmox = ? AND nivel_acceso = ?", vmid, ports.NivelAccesoFullAccess)
		if err := tx.Model(&domain.Usuario{}).Select("organizacion_id, email_usuario").
			Where("activo AND eliminado_en IS NULL").
			Where("rol = ? OR (rol = ? AND ? > 0 AND id IN (?))", "ADMIN", "OPERATOR", vmid, permisos).
			Order("email_usuario").Find(&usuarios).Error; err != nil {
			return fmt.Errorf("no se pudo resolver alcance de alerta: %w", err)
		}
		audiencias := map[uuid.UUID][]string{}
		for _, u := range usuarios {
			audiencias[u.OrganizacionID] = append(audiencias[u.OrganizacionID], u.EmailUsuario)
		}
		repo := &auditRepositoryImpl{db: tx}
		if len(audiencias) == 0 {
			// Persistir sin hacerlo global cuando no hay una audiencia autorizada.
			return repo.registrarConAlcance(ctx, input, nil)
		}
		for orgID, emails := range audiencias {
			scoped := input
			scoped.Detalles = make(map[string]any, len(input.Detalles))
			for k, v := range input.Detalles {
				scoped.Detalles[k] = v
			}
			if input.Accion == ports.AccionAlertaDisparada {
				scoped.Detalles["destinatarios"] = emails
			}
			if err := repo.registrarConAlcance(ctx, scoped, &orgID); err != nil {
				return err
			}
		}
		return nil
	})
}
