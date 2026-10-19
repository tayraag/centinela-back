package services_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/memoria"
	redisstore "el-centinela/internal/adapters/secondary/redis"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

type inventarioParcial struct {
	proxmoxAprovisionamiento
	vmid    int
	maxMem  int64
	maxCpu  int
	visible bool
}

func (p *inventarioParcial) ListarInstancias(ctx context.Context) ([]ports.InstanciaProxmoxDTO, error) {
	base, err := p.proxmoxAprovisionamiento.ListarInstancias(ctx)
	if p.visible {
		base = append(base, ports.InstanciaProxmoxDTO{Vmid: p.vmid, Nodo: "pve1", MaxMem: p.maxMem, MaxCpu: p.maxCpu})
	}
	return base, err
}

func TestVerifyP1a_DescuentaSoloRemanenteParcial(t *testing.T) {
	px := &inventarioParcial{vmid: 101, maxMem: 1024 * 1024 * 1024, maxCpu: 1}
	svc := services.NewCuotasService(px, px, memoria.Nuevo())
	if _, err := svc.AprovisionarInstancia(context.Background(), inputCreacion(4096, 1, 4)); err != nil {
		t.Fatal(err)
	}
	px.visible = true
	if _, err := svc.AprovisionarInstancia(context.Background(), inputCreacion(512, 1, 1)); err != nil {
		t.Fatalf("segunda creación de 512 MiB debía caber tras descontar sólo el remanente y el headroom: %v", err)
	}
	if px.creaciones != 2 {
		t.Fatalf("creaciones=%d, want 2", px.creaciones)
	}
}

func TestVerifyP1b_RemanenteNoSobreDescuenta(t *testing.T) {
	px := &inventarioParcial{vmid: 101, maxMem: 1024 * 1024 * 1024, maxCpu: 1}
	svc := services.NewCuotasService(px, px, memoria.Nuevo())
	if _, err := svc.AprovisionarInstancia(context.Background(), inputCreacion(4096, 1, 4)); err != nil {
		t.Fatal(err)
	}
	px.visible = true
	if _, err := svc.AprovisionarInstancia(context.Background(), inputCreacion(820, 1, 1)); !errors.Is(err, ports.ErrRecursosInsuficientes) {
		t.Fatalf("820 MiB debía ser rechazado al preservar también el headroom, vino %v", err)
	}
	if px.creaciones != 1 {
		t.Fatalf("creaciones=%d, want 1", px.creaciones)
	}
}

func TestVerifyP1c_ClampRemanenteNoNegativo(t *testing.T) {
	px := &inventarioParcial{vmid: 101, maxMem: 4096 * 1024 * 1024, maxCpu: 4}
	svc := services.NewCuotasService(px, px, memoria.Nuevo())
	if _, err := svc.AprovisionarInstancia(context.Background(), inputCreacion(1024, 1, 1)); err != nil {
		t.Fatal(err)
	}
	px.visible = true
	if _, err := svc.AprovisionarInstancia(context.Background(), inputCreacion(1024, 1, 1)); !errors.Is(err, ports.ErrRecursosInsuficientes) {
		t.Fatalf("con clamp y headroom, 1 GiB no cabe; vino %v", err)
	}
	if _, err := svc.AprovisionarInstancia(context.Background(), inputCreacion(512, 1, 1)); err != nil {
		t.Fatalf("512 MiB debía caber: %v", err)
	}
}

func TestVerifyP2_RetencionEntreInstanciasSinVencimiento(t *testing.T) {
	ctx := context.Background()
	srv := miniredis.RunT(t)
	store1 := redisstore.Nuevo(redisstore.Config{Addr: srv.Addr()})
	t.Cleanup(func() { _ = store1.Close() })
	px := &proxmoxAprovisionamiento{}
	svc1 := services.NewCuotasService(px, creadorConError{ports.ErrProxmoxNoDisponible}, store1)
	if _, err := svc1.AprovisionarInstancia(ctx, inputCreacion(4096, 1, 1)); !errors.Is(err, ports.ErrProxmoxNoDisponible) {
		t.Fatalf("se esperaba error de transporte, vino %v", err)
	}
	rdb := goredis.NewClient(&goredis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	if ttl, err := rdb.TTL(ctx, "aprovisionamiento:reservas").Result(); err != nil || ttl > 0 {
		t.Fatalf("la reserva no debe tener vencimiento: ttl=%v err=%v", ttl, err)
	}
	srv.FastForward(16 * time.Minute)
	store2 := redisstore.Nuevo(redisstore.Config{Addr: srv.Addr()})
	t.Cleanup(func() { _ = store2.Close() })
	px2 := &proxmoxAprovisionamiento{}
	svc2 := services.NewCuotasService(px2, px2, store2)
	if _, err := svc2.AprovisionarInstancia(ctx, inputCreacion(4096, 1, 1)); !errors.Is(err, ports.ErrRecursosInsuficientes) {
		t.Fatalf("la reserva retenida debía bloquear 4 GiB tras el TTL antiguo; vino %v", err)
	}
	if px2.creaciones != 0 {
		t.Fatalf("no debía llegar a Proxmox, creaciones=%d", px2.creaciones)
	}
}

func TestVerifyP3_RechazoNoDejaReserva(t *testing.T) {
	ctx := context.Background()
	store := memoria.Nuevo()
	px := &proxmoxAprovisionamiento{}
	svc := services.NewCuotasService(px, px, store)
	if _, err := svc.AprovisionarInstancia(ctx, inputCreacion(4096, 1, 4)); err != nil {
		t.Fatal(err)
	}
	antes, err := store.Get(ctx, "aprovisionamiento:reservas")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AprovisionarInstancia(ctx, inputCreacion(4096, 1, 4)); !errors.Is(err, ports.ErrRecursosInsuficientes) {
		t.Fatalf("se esperaba rechazo por cuota, vino %v", err)
	}
	malo := inputCreacion(512, 1, 1)
	malo.StorageID = "storage-inexistente"
	if _, err := svc.AprovisionarInstancia(ctx, malo); !errors.Is(err, ports.ErrRecursosInsuficientes) {
		t.Fatalf("se esperaba rechazo por storage, vino %v", err)
	}
	despues, err := store.Get(ctx, "aprovisionamiento:reservas")
	if err != nil {
		t.Fatal(err)
	}
	if despues != antes {
		t.Fatalf("el estado persistido cambió tras rechazos:\nantes=%s\ndespues=%s", antes, despues)
	}
	if px.creaciones != 1 {
		t.Fatalf("sólo una orden debía enviarse a Proxmox, creaciones=%d", px.creaciones)
	}
}

func TestVerifyP5_ReservaHeredadaVencidaSigueContando(t *testing.T) {
	ctx := context.Background()
	store := memoria.Nuevo()
	legacy := `[{"id":"legacy-1","nodo":"pve1","storageId":"local-lvm","compartido":false,` +
		`"memoriaBytes":4294967296,"discoBytes":1073741824,"cores":4,"vence":"2020-01-01T00:00:00Z"}]`
	if err := store.Set(ctx, "aprovisionamiento:reservas", legacy, 0); err != nil {
		t.Fatal(err)
	}
	px := &proxmoxAprovisionamiento{}
	svc := services.NewCuotasService(px, px, store)
	if _, err := svc.AprovisionarInstancia(ctx, inputCreacion(4096, 1, 1)); !errors.Is(err, ports.ErrRecursosInsuficientes) {
		t.Fatalf("la reserva heredada vencida debía seguir contando; vino %v", err)
	}
	if px.creaciones != 0 {
		t.Fatalf("creaciones=%d, want 0", px.creaciones)
	}
	if crudo, err := store.Get(ctx, "aprovisionamiento:reservas"); err != nil || crudo != legacy {
		t.Fatalf("el registro heredado debe conservarse intacto; err=%v crudo=%s", err, crudo)
	}
}
