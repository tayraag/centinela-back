package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"el-centinela/internal/core/ports"
)

const (
	// formatoLockInstancia: lock de exclusión mutua de una instancia.
	formatoLockInstancia = "lock:instance:%d:operation"
	// ttlLockInstancia: el lock se libera al registrar la tarea, pero si el
	// proceso muere con el lock tomado, Redis lo suelta solo. Cubre con margen
	// las dos consultas a Proxmox del handler (10 s cada una como máximo).
	ttlLockInstancia = 60 * time.Second
	// esperaLockInstancia: no se hace cola. Si el lock está tomado se rechaza
	// casi de inmediato (lo que tarda un intento de SET NX).
	esperaLockInstancia = 100 * time.Millisecond
)

type exclusionInstancias struct {
	kv     ports.KeyValueStore
	tareas ports.TareaRepository
}

// NewExclusionInstancias crea el guardián de exclusión mutua por instancia.
func NewExclusionInstancias(kv ports.KeyValueStore, tareas ports.TareaRepository) ports.ExclusionInstancias {
	return &exclusionInstancias{kv: kv, tareas: tareas}
}

// Reservar primero toma el lock (atómico): así, de una ráfaga de pedidos
// simultáneos sobre la misma instancia, uno solo pasa. Con el lock tomado
// verifica que no haya una tarea RUNNING de un pedido anterior. El lock se
// mantiene hasta que el handler registra la tarea en tareas_asincronas; desde
// ahí, la tarea RUNNING es la que bloquea a los pedidos siguientes.
func (s *exclusionInstancias) Reservar(ctx context.Context, vmid int) (func(), error) {
	clave := fmt.Sprintf(formatoLockInstancia, vmid)
	ctxLock, cancelar := context.WithTimeout(ctx, esperaLockInstancia)
	soltarLock, err := s.kv.AcquireLock(ctxLock, clave, ttlLockInstancia)
	cancelar()
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case errors.Is(err, context.DeadlineExceeded):
		return nil, ports.ErrInstanciaOcupada // otra operación tiene el lock
	default:
		// Redis no responde: se sigue solo con la verificación en la base. El
		// lock de archivo de Proxmox (lock-<vmid>.conf) queda como última barrera.
		log.Printf("⚠️  [EXCLUSION] sin lock Redis para %d (se verifica solo en la base): %v", vmid, err)
		soltarLock = nil
	}

	liberar := func() {
		if soltarLock == nil {
			return
		}
		ctxSoltar, cancelar := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelar()
		if err := soltarLock(ctxSoltar); err != nil {
			log.Printf("⚠️  [EXCLUSION] no se pudo soltar %s (vence solo en %s): %v", clave, ttlLockInstancia, err)
		}
	}

	activas, err := s.tareas.BuscarTareasActivasPorVmids(ctx, []int{vmid})
	if err != nil {
		liberar()
		return nil, fmt.Errorf("%w: %v", ports.ErrExclusionNoDisponible, err)
	}
	if _, enCurso := activas[vmid]; enCurso {
		liberar()
		return nil, ports.ErrInstanciaOcupada
	}
	return liberar, nil
}
