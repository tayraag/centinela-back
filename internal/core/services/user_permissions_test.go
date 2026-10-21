package services_test

import (
	"context"
	"errors"
	"testing"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"

	"github.com/google/uuid"
)

type repoPermisosUsuario struct {
	ports.UserRepository
	usuario       domain.Usuario
	reemplazos    int
	ultimoPermiso []ports.PermisoInstanciaInput
}

func (r *repoPermisosUsuario) BuscarUsuarioPorIDEnOrg(context.Context, uuid.UUID, uuid.UUID) (*domain.Usuario, error) {
	return &r.usuario, nil
}

func (r *repoPermisosUsuario) ReemplazarPermisos(_ context.Context, _ uuid.UUID, permisos []ports.PermisoInstanciaInput) error {
	r.reemplazos++
	r.ultimoPermiso = append([]ports.PermisoInstanciaInput(nil), permisos...)
	return nil
}

type proxmoxCatalogo struct {
	ports.ProxmoxPort
	instancias []ports.InstanciaProxmoxDTO
	err        error
	consultas  int
}

func (p *proxmoxCatalogo) ListarInstancias(context.Context) ([]ports.InstanciaProxmoxDTO, error) {
	p.consultas++
	return p.instancias, p.err
}

type auditoriaPermisosNula struct{ ports.AuditService }

func (auditoriaPermisosNula) Registrar(context.Context, ports.RegistrarAuditoriaInput) {}

func nuevoServicioPermisos(repo *repoPermisosUsuario, proxmox ports.ProxmoxPort, protegidos map[int]bool) ports.UserService {
	return services.NewUserService(repo, nil, nil, auditoriaPermisosNula{}, nil, proxmox, protegidos)
}

func TestAsignarPermisos_RechazaVMIDInexistenteSinPersistir(t *testing.T) {
	repo := &repoPermisosUsuario{usuario: domain.Usuario{ID: uuid.New()}}
	proxmox := &proxmoxCatalogo{instancias: []ports.InstanciaProxmoxDTO{{Vmid: 110}}}
	svc := nuevoServicioPermisos(repo, proxmox, nil)

	err := svc.AsignarPermisos(context.Background(), repo.usuario.ID, uuid.New(), uuid.New(), []ports.PermisoInstanciaInput{
		{Vmid: 110, NivelAcceso: ports.NivelAccesoReadOnly},
		{Vmid: 999, NivelAcceso: ports.NivelAccesoFullAccess},
	})
	if !errors.Is(err, ports.ErrVMIDNoExiste) || err.Error() != "El VMID 999 no existe en el hipervisor." {
		t.Fatalf("error inesperado: %v", err)
	}
	if repo.reemplazos != 0 {
		t.Fatal("no debe reemplazar permisos cuando algún VMID no existe")
	}
}

func TestAsignarPermisos_RechazaProtegidoAntesDeConsultarYPersistir(t *testing.T) {
	repo := &repoPermisosUsuario{usuario: domain.Usuario{ID: uuid.New()}}
	proxmox := &proxmoxCatalogo{instancias: []ports.InstanciaProxmoxDTO{{Vmid: 100}}}
	svc := nuevoServicioPermisos(repo, proxmox, map[int]bool{100: true})

	err := svc.AsignarPermisos(context.Background(), repo.usuario.ID, uuid.New(), uuid.New(), []ports.PermisoInstanciaInput{{Vmid: 100}})
	if !errors.Is(err, ports.ErrVMIDProtegido) || err.Error() != "La instancia 100 es de infraestructura protegida y no puede asignarse a operadores." {
		t.Fatalf("error inesperado: %v", err)
	}
	if proxmox.consultas != 0 || repo.reemplazos != 0 {
		t.Fatalf("el rechazo debe ser inmediato: consultas=%d reemplazos=%d", proxmox.consultas, repo.reemplazos)
	}
}

func TestAsignarPermisos_ValidaTodoAntesDelReemplazoAtomico(t *testing.T) {
	repo := &repoPermisosUsuario{usuario: domain.Usuario{ID: uuid.New()}}
	proxmox := &proxmoxCatalogo{instancias: []ports.InstanciaProxmoxDTO{{Vmid: 110}, {Vmid: 201}}}
	svc := nuevoServicioPermisos(repo, proxmox, map[int]bool{100: true})
	permisos := []ports.PermisoInstanciaInput{{Vmid: 110}, {Vmid: 201}}

	if err := svc.AsignarPermisos(context.Background(), repo.usuario.ID, uuid.New(), uuid.New(), permisos); err != nil {
		t.Fatal(err)
	}
	if proxmox.consultas != 1 || repo.reemplazos != 1 || len(repo.ultimoPermiso) != 2 {
		t.Fatalf("validación/reemplazo inesperados: consultas=%d reemplazos=%d permisos=%v", proxmox.consultas, repo.reemplazos, repo.ultimoPermiso)
	}
}

func TestAsignarPermisos_NoPersisteSiFallaCatalogo(t *testing.T) {
	repo := &repoPermisosUsuario{usuario: domain.Usuario{ID: uuid.New()}}
	proxmox := &proxmoxCatalogo{err: ports.ErrProxmoxNoDisponible}
	svc := nuevoServicioPermisos(repo, proxmox, nil)

	err := svc.AsignarPermisos(context.Background(), repo.usuario.ID, uuid.New(), uuid.New(), []ports.PermisoInstanciaInput{{Vmid: 110}})
	if !errors.Is(err, ports.ErrProxmoxNoDisponible) || repo.reemplazos != 0 {
		t.Fatalf("debe fallar cerrado sin persistir: err=%v reemplazos=%d", err, repo.reemplazos)
	}
}
