package services_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/memoria"
	"el-centinela/internal/adapters/secondary/proxmox"
	redisstore "el-centinela/internal/adapters/secondary/redis"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"

	"github.com/alicebob/miniredis/v2"
)

type creadorConError struct{ err error }

func (c creadorConError) AprovisionarInstancia(context.Context, ports.CrearInstanciaProxmoxInput) (*ports.ResultadoAprovisionamiento, error) {
	return nil, c.err
}

func TestAprovisionamiento_ConservaCapacidadAnteResultadoDesconocido(t *testing.T) {
	for _, fallo := range []error{
		fmt.Errorf("%w: %w", ports.ErrProxmoxNoDisponible, ports.ErrProxmoxTimeout),
		context.Canceled,
		ports.ErrProxmoxNoDisponible,
	} {
		t.Run(fallo.Error(), func(t *testing.T) {
			ahora := time.Now()
			store := memoria.NuevoConReloj(func() time.Time { return ahora })
			px := &proxmoxAprovisionamiento{}
			svc := services.NewCuotasService(px, creadorConError{fallo}, store)
			if _, err := svc.AprovisionarInstancia(context.Background(), inputCreacion(4096, 1, 1)); !errors.Is(err, fallo) {
				t.Fatalf("creation error = %v, want %v", err, fallo)
			}
			// A lost response does not prove that Proxmox stopped, even after the old TTL.
			ahora = ahora.Add(16 * time.Minute)
			if _, err := store.Get(context.Background(), "aprovisionamiento:reservas"); err != nil {
				t.Fatalf("uncertain reservation disappeared: %v", err)
			}
			segundo := services.NewCuotasService(px, px, store)
			if _, err := segundo.AprovisionarInstancia(context.Background(), inputCreacion(3072, 1, 1)); !errors.Is(err, ports.ErrRecursosInsuficientes) {
				t.Fatalf("second request = %v, want insufficient capacity", err)
			}
			if px.creaciones != 0 {
				t.Fatalf("second request reached Proxmox %d times", px.creaciones)
			}
		})
	}
}

func TestAprovisionamiento_LiberaReservaSiLaCreacionNoFueDespachada(t *testing.T) {
	store := memoria.Nuevo()
	px := &proxmoxAprovisionamiento{}
	svc := services.NewCuotasService(px, creadorConError{ports.ErrCreacionNoDespachada}, store)

	if _, err := svc.AprovisionarInstancia(context.Background(), inputCreacion(4096, 1, 1)); !errors.Is(err, ports.ErrCreacionNoDespachada) {
		t.Fatalf("creation error = %v, want ErrCreacionNoDespachada", err)
	}
	if _, err := store.Get(context.Background(), "aprovisionamiento:reservas"); !errors.Is(err, ports.ErrClaveNoEncontrada) {
		t.Fatalf("la reserva de una orden no despachada debía eliminarse: %v", err)
	}

	segundo := services.NewCuotasService(px, px, store)
	if _, err := segundo.AprovisionarInstancia(context.Background(), inputCreacion(4096, 1, 1)); err != nil {
		t.Fatalf("la capacidad debía quedar disponible: %v", err)
	}
}

type proxmoxConInventario struct {
	proxmoxAprovisionamiento
	mu      sync.Mutex
	visible bool
}

func (p *proxmoxConInventario) ListarInstancias(ctx context.Context) ([]ports.InstanciaProxmoxDTO, error) {
	base, err := p.proxmoxAprovisionamiento.ListarInstancias(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.visible {
		base = append(base, ports.InstanciaProxmoxDTO{Vmid: 101, Nodo: "pve1", MaxMem: 3 * 1024 * 1024 * 1024, MaxCpu: 3})
	}
	return base, err
}

func TestAprovisionamiento_NoDuplicaReservaReflejadaEnInventario(t *testing.T) {
	px := &proxmoxConInventario{}
	svc := services.NewCuotasService(px, px, memoria.Nuevo())
	if _, err := svc.AprovisionarInstancia(context.Background(), inputCreacion(3072, 1, 3)); err != nil {
		t.Fatal(err)
	}
	px.mu.Lock()
	px.visible = true
	px.mu.Unlock()
	// La VM visible ya representa la reserva: con el headroom del 15% todavía
	// cabe 1 GiB; si se contara dos veces, la segunda creación sería rechazada.
	if _, err := svc.AprovisionarInstancia(context.Background(), inputCreacion(1024, 1, 1)); err != nil {
		t.Fatalf("valid second creation rejected: %v", err)
	}
	if px.creaciones != 2 {
		t.Fatalf("creations = %d, want 2", px.creaciones)
	}
}

func TestAprovisionamiento_ResultadoFinalNoConfundeTimeoutConLiberacion(t *testing.T) {
	for _, caso := range []struct {
		nombre    string
		resultado services.ResultadoTarea
		libera    bool
	}{
		{"timeout", services.ResultadoTarea{Estado: ports.TareaFailed, Motivo: ports.MotivoTimeout}, false},
		{"remote_failure_may_leave_resources", services.ResultadoTarea{Estado: ports.TareaFailed, Motivo: ports.MotivoProxmoxError}, false},
		{"completed", services.ResultadoTarea{Estado: ports.TareaCompleted, ExitStatus: "OK"}, true},
		{"warnings", services.ResultadoTarea{Estado: ports.TareaCompleted, ExitStatus: "WARNINGS: 1"}, true},
	} {
		t.Run(caso.nombre, func(t *testing.T) {
			store := memoria.Nuevo()
			px := &proxmoxAprovisionamiento{}
			svc := services.NewCuotasService(px, px, store)
			primera, err := svc.AprovisionarInstancia(context.Background(), inputCreacion(4096, 1, 1))
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.FinalizarReserva(context.Background(), primera.ReservaID, caso.resultado); err != nil {
				t.Fatal(err)
			}
			_, err = svc.AprovisionarInstancia(context.Background(), inputCreacion(3072, 1, 1))
			if caso.libera && err != nil {
				t.Fatalf("confirmed completion should release: %v", err)
			}
			if !caso.libera && !errors.Is(err, ports.ErrRecursosInsuficientes) {
				t.Fatalf("uncertain result freed capacity: %v", err)
			}
		})
	}
}

func TestAprovisionamiento_DosClientesRedisVeinticuatroSolicitudes(t *testing.T) {
	srv := miniredis.RunT(t)
	px := &proxmoxAprovisionamiento{}
	var servicios []*services.CuotasService
	for i := 0; i < 2; i++ {
		store := redisstore.Nuevo(redisstore.Config{Addr: srv.Addr()})
		t.Cleanup(func() { _ = store.Close() })
		servicios = append(servicios, services.NewCuotasService(px, px, store))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan error, 24)
	for i := 0; i < cap(results); i++ {
		go func(i int) {
			<-start
			_, err := servicios[i%2].AprovisionarInstancia(ctx, inputCreacion(512, 1, 1))
			results <- err
		}(i)
	}
	close(start)
	aceptadas, rechazadas := 0, 0
	for i := 0; i < cap(results); i++ {
		err := <-results
		switch {
		case err == nil:
			aceptadas++
		case errors.Is(err, ports.ErrRecursosInsuficientes):
			rechazadas++
		default:
			t.Errorf("unexpected result: %v", err)
		}
	}
	if aceptadas != 6 || rechazadas != 18 || px.creaciones != 6 {
		t.Fatalf("accepted=%d rejected=%d Proxmox=%d, want 6/18/6", aceptadas, rechazadas, px.creaciones)
	}
}

func TestAprovisionamiento_HTTPAceptadoConRespuestaPerdida(t *testing.T) {
	var aceptadas atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api2/json/cluster/nextid":
			_, _ = fmt.Fprint(w, `{"data":"101"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api2/json/nodes/pve1/qemu":
			if err := r.ParseForm(); err != nil || r.Form.Get("vmid") != "101" {
				t.Errorf("invalid creation request: %v %v", r.Form, err)
			}
			aceptadas.Add(1) // Remote side accepted the operation before losing the reply.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	px := &proxmoxAprovisionamiento{}
	remote := proxmox.NewClient(server.URL, "test@pve!token", "not-a-secret", nil)
	store := memoria.Nuevo()
	svc := services.NewCuotasService(px, remote, store)
	if result, err := svc.AprovisionarInstancia(context.Background(), inputCreacion(4096, 1, 1)); result != nil || !errors.Is(err, ports.ErrProxmoxNoDisponible) {
		t.Fatalf("lost reply result=%v error=%v", result, err)
	}
	if aceptadas.Load() != 1 {
		t.Fatalf("remote accepted %d requests, want 1", aceptadas.Load())
	}
	second := services.NewCuotasService(px, px, store)
	if _, err := second.AprovisionarInstancia(context.Background(), inputCreacion(3072, 1, 1)); !errors.Is(err, ports.ErrRecursosInsuficientes) {
		t.Fatalf("lost reply freed reserved resources: %v", err)
	}
	if px.creaciones != 0 {
		t.Fatal("second request reached Proxmox despite outstanding reservation")
	}
}
