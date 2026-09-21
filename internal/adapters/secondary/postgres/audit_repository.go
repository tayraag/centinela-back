package postgres

import (
	"context"
	"encoding/json"
	"fmt"
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
	// Serializar detalles adicionales a JSON
	var detallesPtr *string
	if len(input.Detalles) > 0 {
		b, err := json.Marshal(input.Detalles)
		if err == nil {
			s := string(b)
			detallesPtr = &s
		}
	}

	var usuarioIDPtr *uuid.UUID
	if input.UsuarioID != uuid.Nil {
		usuarioIDPtr = &input.UsuarioID
	}

	registro := domain.Auditoria{
		UsuarioID:       usuarioIDPtr,
		Accion:          input.Accion,
		InstanciaID:     input.InstanciaID,
		InstanciaNombre: input.InstanciaNombre,
		Resultado:       input.Resultado,
		Detalles:        detallesPtr,
		FechaHora:       time.Now(),
	}

	if err := r.db.WithContext(ctx).Create(&registro).Error; err != nil {
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
		Select("a.*, u.nombre_completo").
		Joins("LEFT JOIN usuarios u ON u.id = a.usuario_id").
		Where("u.organizacion_id = ? OR a.usuario_id IS NULL", orgID)

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
	orden := col + " " + opciones.Direccion

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
		detalles := ""
		if row.Auditoria.Detalles != nil {
			detalles = *row.Auditoria.Detalles
		}
		items[i] = ports.AuditoriaDTO{
			ID:              row.Auditoria.ID,
			UsuarioID:       row.Auditoria.UsuarioID,
			NombreUsuario:   nombreUsuario,
			FechaHora:       row.Auditoria.FechaHora,
			Accion:          row.Auditoria.Accion,
			InstanciaID:     row.Auditoria.InstanciaID,
			InstanciaNombre: row.Auditoria.InstanciaNombre,
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
		Select("a.*, u.nombre_completo").
		Joins("LEFT JOIN usuarios u ON u.id = a.usuario_id").
		Where("u.organizacion_id = ? OR a.usuario_id IS NULL", orgID)

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
		detalles := ""
		if row.Auditoria.Detalles != nil {
			detalles = *row.Auditoria.Detalles
		}
		items[i] = ports.AuditoriaDTO{
			ID:              row.Auditoria.ID,
			UsuarioID:       row.Auditoria.UsuarioID,
			NombreUsuario:   nombreUsuario,
			FechaHora:       row.Auditoria.FechaHora,
			Accion:          row.Auditoria.Accion,
			InstanciaID:     row.Auditoria.InstanciaID,
			InstanciaNombre: row.Auditoria.InstanciaNombre,
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
	if filtros.UsuarioID != nil {
		query = query.Where("a.usuario_id = ?", filtros.UsuarioID)
	}
	if filtros.Accion != "" {
		query = query.Where("a.accion = ?", filtros.Accion)
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
		// Incluir todo el día "hasta"
		hasta := filtros.Hasta.Add(24*time.Hour - time.Second)
		query = query.Where("a.fecha_hora <= ?", hasta)
	}
	return query
}
