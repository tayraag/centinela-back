package services_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"
	"github.com/google/uuid"
)

type cierreConFalla struct {
	tareasEnMemoria
	intentos atomic.Int32
}

func (r *cierreConFalla) ActualizarEstado(ctx context.Context, id uuid.UUID, estado string, meta map[string]any, auditoria ports.RegistrarAuditoriaInput) error {
	if r.intentos.Add(1) == 1 {
		return errors.New("inserción de auditoría fallida")
	}
	return r.tareasEnMemoria.ActualizarEstado(ctx, id, estado, meta, auditoria)
}

func TestSeguimientoReintentaCierreAntesDePublicar(t *testing.T) {
	repo := &cierreConFalla{tareasEnMemoria: tareasEnMemoria{tareas: map[uuid.UUID]*domain.TareaAsincrona{}}}
	px := &proxmoxTareas{exitStatus: "OK"}
	publicado := make(chan string, 2)
	pool := services.NewSeguimientoTareas(px, repo, services.NewAuditService(nil), services.ConfigSeguimiento{
		IntervaloReconciliar: 20 * time.Millisecond,
		OnTaskFinished: func(_ context.Context, tarea *domain.TareaAsincrona, _ string, _ services.ResultadoTarea) {
			publicado <- repo.estado(tarea.ID)
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	pool.Iniciar(ctx)
	t.Cleanup(func() {
		cancel()
		fin, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		if err := pool.Esperar(fin); err != nil {
			t.Error(err)
		}
	})
	if _, err := pool.Seguir(context.Background(), uuid.New(), 110, "start", "UPID:test"); err != nil {
		t.Fatal(err)
	}
	select {
	case estado := <-publicado:
		if estado != ports.TareaCompleted || repo.intentos.Load() < 2 {
			t.Fatalf("publicó antes de confirmar el cierre: %s, intentos=%d", estado, repo.intentos.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("el reconciliador no reintentó el cierre")
	}
}
