package services

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
)

// formatoNombreSnapshot es el que acepta Proxmox (pve-configid): empieza con
// letra; letras, números, _ y -; de 2 a 40 caracteres. "current" está
// reservado para el estado actual de la instancia.
var formatoNombreSnapshot = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{1,39}$`)

// snapshotService implementa ports.SnapshotService.
type snapshotService struct {
	proxmox     ports.ProxmoxPort
	seguimiento ports.SeguimientoTareas
	audit       ports.AuditService
}

// NewSnapshotService crea el servicio de snapshots.
func NewSnapshotService(proxmox ports.ProxmoxPort, seguimiento ports.SeguimientoTareas, audit ports.AuditService) ports.SnapshotService {
	return &snapshotService{proxmox: proxmox, seguimiento: seguimiento, audit: audit}
}

// NombreSnapshotValido indica si Proxmox acepta el nombre.
func NombreSnapshotValido(nombre string) bool {
	return formatoNombreSnapshot.MatchString(nombre) && !strings.EqualFold(nombre, "current")
}

func (s *snapshotService) Listar(ctx context.Context, vmid int) ([]ports.SnapshotDTO, error) {
	inst, err := s.proxmox.ObtenerInstancia(ctx, vmid)
	if err != nil {
		return nil, err
	}
	snapshots, err := s.proxmox.ListarSnapshots(ctx, inst.Nodo, inst.Tipo, vmid)
	if err != nil {
		return nil, err
	}
	// Del más nuevo al más viejo; a igual snaptime, por nombre para un orden estable.
	sort.SliceStable(snapshots, func(i, j int) bool {
		if snapshots[i].SnapTime != snapshots[j].SnapTime {
			return snapshots[i].SnapTime > snapshots[j].SnapTime
		}
		return snapshots[i].Nombre < snapshots[j].Nombre
	})
	return snapshots, nil
}

func (s *snapshotService) Crear(ctx context.Context, usuarioID uuid.UUID, vmid int, nombre, descripcion string) (*ports.SnapshotCreado, error) {
	if !NombreSnapshotValido(nombre) {
		return nil, ports.ErrSnapshotNombreInvalido
	}
	descripcion = strings.TrimSpace(descripcion)
	if utf8.RuneCountInString(descripcion) > ports.LargoMaximoDescripcionSnapshot {
		return nil, ports.ErrSnapshotDescripcionInvalida
	}
	inst, err := s.proxmox.ObtenerInstancia(ctx, vmid)
	if err != nil {
		return nil, err
	}
	// Proxmox rechaza un nombre repetido con un error genérico: se avisa antes.
	existentes, err := s.proxmox.ListarSnapshots(ctx, inst.Nodo, inst.Tipo, vmid)
	if err != nil {
		return nil, err
	}
	for _, e := range existentes {
		if e.Nombre == nombre {
			return nil, ports.ErrSnapshotExistente
		}
	}

	upid, err := s.proxmox.CrearSnapshot(ctx, inst.Nodo, inst.Tipo, vmid, nombre, descripcion)
	if err != nil {
		s.audit.Registrar(ctx, ports.RegistrarAuditoriaInput{
			UsuarioID: usuarioID, Accion: ports.AccionCrearSnapshot, InstanciaID: strconv.Itoa(vmid),
			InstanciaNombre: inst.Nombre, Resultado: ports.ResultadoFalla,
			Detalles: map[string]any{"snapname": nombre, "resource_type": inst.Tipo, "error": err.Error()},
		})
		return nil, err
	}
	tareaID, err := s.seguimiento.Seguir(ctx, usuarioID, vmid, ports.AccionTareaSnapshot, upid, map[string]any{
		"instanciaNombre": inst.Nombre, "resource_type": inst.Tipo, "snapname": nombre,
	})
	if err != nil {
		log.Printf("[SNAPSHOTS] Proxmox aceptó el snapshot %s de %d pero no se pudo registrar su seguimiento: %v", nombre, vmid, err)
		return nil, fmt.Errorf("%w: %v", ports.ErrSeguimientoNoRegistrado, err)
	}
	return &ports.SnapshotCreado{TareaID: tareaID, Nombre: nombre}, nil
}
