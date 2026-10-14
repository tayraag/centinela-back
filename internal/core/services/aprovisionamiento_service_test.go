package services_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/memoria"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"
)

type proxmoxAprovisionamiento struct {
	proxmoxTareas
	muCrear           sync.Mutex
	creaciones        int
	lecturasNodo      int
	lecturasInstancia int
	lecturasStorage   int
	bloquear          chan struct{}
	inicio            chan struct{}
	inicioOnce        sync.Once
}

func (p *proxmoxAprovisionamiento) ObtenerEstadoNodo(context.Context, string) (*ports.NodeStatusDTO, error) {
	p.muCrear.Lock()
	p.lecturasNodo++
	p.muCrear.Unlock()
	return &ports.NodeStatusDTO{CPUs: 8, MemTotal: 8 * 1024 * 1024 * 1024, MemUsada: 1024 * 1024 * 1024}, nil
}

func (p *proxmoxAprovisionamiento) ListarInstancias(context.Context) ([]ports.InstanciaProxmoxDTO, error) {
	p.muCrear.Lock()
	p.lecturasInstancia++
	p.muCrear.Unlock()
	return []ports.InstanciaProxmoxDTO{{Nodo: "pve1", MaxCpu: 2, MaxMem: 2 * 1024 * 1024 * 1024}}, nil
}

func (p *proxmoxAprovisionamiento) ObtenerStorages(context.Context, string) ([]ports.StorageDTO, error) {
	p.muCrear.Lock()
	p.lecturasStorage++
	p.muCrear.Unlock()
	return []ports.StorageDTO{{Storage: "local-lvm", Avail: 20 * 1024 * 1024 * 1024, Active: 1, Enabled: 1, Content: "rootdir,images"}}, nil
}

func (p *proxmoxAprovisionamiento) AprovisionarInstancia(context.Context, ports.CrearInstanciaProxmoxInput) (*ports.ResultadoAprovisionamiento, error) {
	p.muCrear.Lock()
	p.creaciones++
	n := p.creaciones
	p.muCrear.Unlock()
	if p.inicio != nil {
		p.inicioOnce.Do(func() { close(p.inicio) })
	}
	if p.bloquear != nil {
		<-p.bloquear
	}
	return &ports.ResultadoAprovisionamiento{VMID: 100 + n, UPID: "UPID", Accion: ports.AccionProxmoxCrearVM}, nil
}

func TestAprovisionamiento_SerializaSolicitudesConcurrentes(t *testing.T) {
	px := &proxmoxAprovisionamiento{bloquear: make(chan struct{}), inicio: make(chan struct{})}
	svc := services.NewAprovisionamientoService(px, px, memoria.Nuevo())
	ctx := context.Background()

	primeraResultado := make(chan error, 1)
	go func() {
		_, err := svc.AprovisionarInstancia(ctx, inputCreacion(4*1024, 12, 4))
		primeraResultado <- err
	}()
	<-px.inicio // la primera ya validó, reservó y mantiene el lock

	segundaResultado := make(chan error, 1)
	go func() {
		_, err := svc.AprovisionarInstancia(ctx, inputCreacion(3*1024, 9, 3))
		segundaResultado <- err
	}()
	select {
	case err := <-segundaResultado:
		t.Fatalf("la segunda solicitud no debía atravesar el lock antes que la primera: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(px.bloquear)
	if err := <-primeraResultado; err != nil {
		t.Fatalf("la primera solicitud debía entrar a Proxmox: %v", err)
	}
	if err := <-segundaResultado; !errors.Is(err, ports.ErrRecursosInsuficientes) {
		t.Fatalf("la segunda debía revalidar y recibir cuota insuficiente: %v", err)
	}
	if px.creaciones != 1 {
		t.Fatalf("solo la solicitud ganadora debe llegar al POST de Proxmox, llegaron %d", px.creaciones)
	}
}

func inputCreacion(memoriaMB, discoGB, cores int) ports.CrearInstanciaProxmoxInput {
	return ports.CrearInstanciaProxmoxInput{
		Tipo: ports.TipoInstanciaQemu, Nodo: "pve1", StorageID: "local-lvm",
		MemoriaMB: memoriaMB, DiscoGB: discoGB, Cores: cores,
	}
}

func TestAprovisionamiento_RechazaCuotasInsuficientesAntesDeCrear(t *testing.T) {
	casos := []struct {
		nombre string
		input  ports.CrearInstanciaProxmoxInput
	}{
		{"RAM", inputCreacion(7*1024, 1, 1)},
		{"CPU", inputCreacion(512, 1, 7)},
		{"disco", inputCreacion(512, 21, 1)},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			px := &proxmoxAprovisionamiento{}
			svc := services.NewAprovisionamientoService(px, px, memoria.Nuevo())
			if _, err := svc.AprovisionarInstancia(context.Background(), caso.input); !errors.Is(err, ports.ErrRecursosInsuficientes) {
				t.Fatalf("se esperaba ErrRecursosInsuficientes, vino %v", err)
			}
			if px.creaciones != 0 {
				t.Fatal("no debe llamar al POST de Proxmox cuando la cuota es insuficiente")
			}
		})
	}
}

func TestAprovisionamiento_ReutilizaFotoDeCapacidadDeRedis(t *testing.T) {
	px := &proxmoxAprovisionamiento{}
	svc := services.NewAprovisionamientoService(px, px, memoria.Nuevo())
	ctx := context.Background()

	for intento := 0; intento < 2; intento++ {
		if _, err := svc.AprovisionarInstancia(ctx, inputCreacion(7*1024, 1, 1)); !errors.Is(err, ports.ErrRecursosInsuficientes) {
			t.Fatalf("intento %d: se esperaba ErrRecursosInsuficientes, vino %v", intento+1, err)
		}
	}
	if px.lecturasNodo != 1 || px.lecturasInstancia != 1 || px.lecturasStorage != 1 {
		t.Fatalf("la segunda validación debía usar Redis; lecturas nodo=%d instancias=%d storage=%d", px.lecturasNodo, px.lecturasInstancia, px.lecturasStorage)
	}
}

func TestAprovisionamiento_ReservaEvitaCarreraYSeLibera(t *testing.T) {
	px := &proxmoxAprovisionamiento{}
	store := memoria.Nuevo()
	svc := services.NewAprovisionamientoService(px, px, store)
	ctx := context.Background()
	_ = store.Set(ctx, "node:status:current", "foto-anterior", time.Minute)

	primera, err := svc.AprovisionarInstancia(ctx, inputCreacion(4*1024, 12, 4))
	if err != nil {
		t.Fatal(err)
	}
	if primera.ReservaID == "" {
		t.Fatal("la creación aceptada debe conservar el identificador de reserva")
	}
	if _, err := store.Get(ctx, "node:status:current"); !errors.Is(err, ports.ErrClaveNoEncontrada) {
		t.Fatalf("una creación aceptada debe invalidar la foto cacheada del nodo: %v", err)
	}
	if _, err := store.Get(ctx, "aprovisionamiento:capacidad:pve1"); !errors.Is(err, ports.ErrClaveNoEncontrada) {
		t.Fatalf("una creación aceptada debe invalidar la capacidad de aprovisionamiento: %v", err)
	}

	// Sin la reserva, ambas solicitudes entrarían porque Proxmox todavía
	// devuelve la misma foto de capacidad. Con ella, la segunda llega tarde.
	if _, err := svc.AprovisionarInstancia(ctx, inputCreacion(3*1024, 9, 3)); !errors.Is(err, ports.ErrRecursosInsuficientes) {
		t.Fatalf("la segunda solicitud debía ser rechazada por la reserva: %v", err)
	}
	if px.creaciones != 1 {
		t.Fatalf("solo debía enviarse una creación a Proxmox, se enviaron %d", px.creaciones)
	}

	_ = store.Set(ctx, "node:status:current", "foto-durante-la-tarea", time.Minute)
	_ = store.Set(ctx, "aprovisionamiento:capacidad:pve1", "foto-durante-la-tarea", time.Minute)
	if err := svc.LiberarReserva(ctx, primera.ReservaID); err != nil {
		t.Fatal(err)
	}
	for _, clave := range []string{"node:status:current", "aprovisionamiento:capacidad:pve1"} {
		if _, err := store.Get(ctx, clave); !errors.Is(err, ports.ErrClaveNoEncontrada) {
			t.Fatalf("al cerrar la tarea debía invalidarse %s: %v", clave, err)
		}
	}
	if _, err := svc.AprovisionarInstancia(ctx, inputCreacion(3*1024, 9, 3)); err != nil {
		t.Fatalf("después de liberar la reserva la capacidad debe volver a estar disponible: %v", err)
	}
}
