package http

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/core/ports"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// El VMID de Proxmox nunca sale de la API: toda respuesta, evento o mensaje que
// identifica una instancia lleva su referencia pública ("inst_..."), y toda
// ruta o cuerpo que la recibe la traduce al VMID antes de llegar al core.

// refPublica devuelve la referencia pública del VMID. Un VMID fuera de rango
// (Proxmox no los genera) se registra y devuelve false: el llamador lo omite en
// vez de exponer el número.
func refPublica(refs ports.ReferenciasInstancia, vmid int) (string, bool) {
	ref, err := refs.OfuscarVMID(vmid)
	if err != nil {
		log.Printf("⚠️  [REFS] no se pudo ofuscar el VMID %d: %v", vmid, err)
		return "", false
	}
	return ref, true
}

// extraerVmid lee el VMID que ResolveInstanceRef tradujo desde :instanceRef.
func extraerVmid(c *gin.Context) (int, bool) {
	vmid, ok := middleware.VmidResuelto(c)
	if !ok {
		middleware.AbortRecursoNoEncontrado(c)
	}
	return vmid, ok
}

// ==========================================
// Permisos de instancia (admin y perfil)
// ==========================================

// PermisoInstanciaRequest es un permiso del cuerpo de PUT /admin/users/:id/permissions.
type PermisoInstanciaRequest struct {
	Vmid        string `json:"vmid" binding:"required" example:"inst_3Hk9aQ2mXv7LpR4sT1bN8c"` // referencia pública
	NivelAcceso string `json:"nivelAcceso,omitempty" enums:"FULL_ACCESS,READ_ONLY" example:"READ_ONLY"`
}

// PermisoInstanciaResponse es un permiso de instancia con su referencia pública.
type PermisoInstanciaResponse struct {
	Vmid        string `json:"vmid" example:"inst_3Hk9aQ2mXv7LpR4sT1bN8c"` // referencia pública
	NivelAcceso string `json:"nivelAcceso" enums:"FULL_ACCESS,READ_ONLY" example:"FULL_ACCESS"`
}

func permisosPublicos(refs ports.ReferenciasInstancia, permisos []ports.PermisoInstanciaInput) []PermisoInstanciaResponse {
	resultado := make([]PermisoInstanciaResponse, 0, len(permisos))
	for _, p := range permisos {
		if ref, ok := refPublica(refs, p.Vmid); ok {
			resultado = append(resultado, PermisoInstanciaResponse{Vmid: ref, NivelAcceso: p.NivelAcceso})
		}
	}
	return resultado
}

// UsuarioDetalleResponse es ports.UsuarioDetalleDTO con las instancias como
// referencias públicas.
type UsuarioDetalleResponse struct {
	ID                        uuid.UUID                  `json:"id"`
	NombreCompleto            string                     `json:"nombreCompleto"`
	NombreUsuario             string                     `json:"nombreUsuario"`
	EmailUsuario              string                     `json:"emailUsuario"`
	OrganizacionID            uuid.UUID                  `json:"organizacionId"`
	Rol                       string                     `json:"rol"`
	Activo                    bool                       `json:"activo"`
	TotpVinculado             bool                       `json:"totpVinculado"`
	CambioContrasenaRequerido bool                       `json:"cambioContrasenaRequerido"`
	FechaCreacion             time.Time                  `json:"fechaCreacion"`
	FechaUltimoAcceso         *time.Time                 `json:"fechaUltimoAcceso"`
	InstanciasPermitidas      []string                   `json:"instanciasPermitidas" example:"inst_3Hk9aQ2mXv7LpR4sT1bN8c"`
	Permisos                  []PermisoInstanciaResponse `json:"permisos"`
	EliminadoEn               *time.Time                 `json:"eliminadoEn,omitempty"`
}

func detalleUsuarioPublico(refs ports.ReferenciasInstancia, d *ports.UsuarioDetalleDTO) UsuarioDetalleResponse {
	r := UsuarioDetalleResponse{
		ID: d.ID, NombreCompleto: d.NombreCompleto, NombreUsuario: d.NombreUsuario, EmailUsuario: d.EmailUsuario,
		OrganizacionID: d.OrganizacionID, Rol: d.Rol, Activo: d.Activo, TotpVinculado: d.TotpVinculado,
		CambioContrasenaRequerido: d.CambioContrasenaRequerido, FechaCreacion: d.FechaCreacion,
		FechaUltimoAcceso: d.FechaUltimoAcceso, EliminadoEn: d.EliminadoEn,
		InstanciasPermitidas: make([]string, 0, len(d.InstanciasPermitidas)),
		Permisos:             make([]PermisoInstanciaResponse, 0, len(d.Permisos)),
	}
	for _, vmid := range d.InstanciasPermitidas {
		if ref, ok := refPublica(refs, vmid); ok {
			r.InstanciasPermitidas = append(r.InstanciasPermitidas, ref)
		}
	}
	for _, p := range d.Permisos {
		if ref, ok := refPublica(refs, p.VMID); ok {
			r.Permisos = append(r.Permisos, PermisoInstanciaResponse{Vmid: ref, NivelAcceso: p.NivelAcceso})
		}
	}
	return r
}

// actividadPublica reemplaza el VMID de cada registro por su referencia.
func actividadPublica(refs ports.ReferenciasInstancia, actividad []ports.ActividadDTO) []ports.ActividadDTO {
	resultado := make([]ports.ActividadDTO, len(actividad))
	for i, a := range actividad {
		a.Detalles = ports.DetallesAuditoriaPublicos(refs, a.Detalles, a.InstanciaID)
		a.InstanciaID = ports.IDInstanciaPublico(refs, a.InstanciaID)
		resultado[i] = a
	}
	return resultado
}

// responderErrorRefs: no se pudo ofuscar un VMID que debía responderse.
func responderErrorRefs(c *gin.Context) {
	SendError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo armar la referencia de la instancia.")
}

// eventoPublico devuelve el evento listo para el front: en VM y LXC, recursoId
// pasa a la referencia pública y el VMID se oculta en los textos de detalles
// (el error de Proxmox lo nombra). Devuelve una copia: el evento original lo
// comparten todos los streams.
func eventoPublico(refs ports.ReferenciasInstancia, ev ports.RealtimeEvent) ports.RealtimeEvent {
	if ev.RecursoTipo != ports.RecursoVM && ev.RecursoTipo != ports.RecursoLXC || ev.RecursoID == "" {
		return ev
	}
	vmid, _ := strconv.Atoi(ev.RecursoID)
	ev.RecursoID = ports.IDInstanciaPublico(refs, ev.RecursoID)
	ev.Detalles = ports.OcultarVmidEnDetalles(ev.Detalles, vmid, ev.RecursoID)
	return ev
}
