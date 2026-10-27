package services_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/redis"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"

	"github.com/alicebob/miniredis/v2"
)

// Integración: el evaluador publica en Redis Pub/Sub (miniredis) a través del
// servicio de eventos real. Varias réplicas evalúan a la vez el ciclo en que se
// confirma la saturación: al canal centinela:events llega un solo
// ALERT_TRIGGERED, en menos de un segundo, y se manda un solo correo.
func TestAlertas_IntegracionPubSubYCorreo(t *testing.T) {
	srv := miniredis.RunT(t)
	kv := redis.Nuevo(redis.Config{Addr: srv.Addr()})
	t.Cleanup(func() { _ = kv.Close() })
	eventos := services.NewEventosService(kv, nil, nil, nil, topologiaPrueba())

	mensajes, cancelar, err := kv.Subscribe(context.Background(), ports.CanalEventos)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelar()

	nodo, correo, audit := &nodoAlertas{}, &correoAlertas{}, &auditoriaAlertas{}
	nodo.fijar(92, 10, 10)
	replicas := make([]*services.EvaluadorAlertas, 4)
	for i := range replicas {
		replicas[i] = services.NewEvaluadorAlertas(nodo, &inventarioAlertas{}, &tareasAlertas{conTarea: map[int]bool{}}, kv, eventos, audit,
			&destinatariosAlertas{}, correo, "pve", 30*time.Second)
	}
	replicas[0].Evaluar(context.Background()) // primer ciclo: pico

	inicio := time.Now()
	var wg sync.WaitGroup
	for _, r := range replicas {
		wg.Add(1)
		go func() { defer wg.Done(); r.Evaluar(context.Background()) }()
	}

	select {
	case m := <-mensajes:
		if demora := time.Since(inicio); demora > time.Second {
			t.Errorf("El evento tardó %s en llegar al canal", demora)
		}
		var bus ports.MensajeBus
		if err := json.Unmarshal([]byte(m.Contenido), &bus); err != nil || bus.Evento == nil || bus.Evento.Tipo != ports.EventoAlerta || bus.Evento.Detalles["valorActual"] != 92.0 {
			t.Fatalf("Mensaje inesperado en el canal: %s", m.Contenido)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("No llegó el ALERT_TRIGGERED al canal centinela:events")
	}
	wg.Wait()
	for _, r := range replicas {
		r.EsperarCorreos()
	}
	select {
	case m := <-mensajes:
		t.Errorf("Llegó un segundo evento (debió deduplicarse): %s", m.Contenido)
	case <-time.After(300 * time.Millisecond):
	}
	if n := len(correo.lista()); n != 1 {
		t.Errorf("Se esperaba un solo correo, hubo %d", n)
	}
	if n := len(audit.registros); n != 1 {
		t.Errorf("Se esperaba una sola fila de auditoría, hubo %d", n)
	}
}
