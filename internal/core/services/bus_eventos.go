package services

import (
	"context"
	"log"

	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
)

// busEventos publica mensajes en el canal centinela:events (Redis Pub/Sub).
type busEventos struct {
	kv ports.KeyValueStore
}

func (b busEventos) publicar(ctx context.Context, m ports.MensajeBus) error {
	return b.kv.Publish(ctx, ports.CanalEventos, m)
}

// avisarLogout pide cortar el stream de eventos de una sesión que se cerró.
// Si el aviso no sale, se loguea: la sesión ya está cerrada igual.
func (b busEventos) avisarLogout(ctx context.Context, usuarioID, sesionID uuid.UUID) {
	if err := b.publicar(ctx, ports.MensajeBus{Tipo: ports.MensajeLogout, UsuarioID: &usuarioID, SesionID: &sesionID}); err != nil {
		log.Printf("[EVENTOS] no se pudo avisar el logout de la sesión %s: %v", sesionID, err)
	}
}

// avisarSesionesRevocadas pide cortar todos los streams de un usuario.
func (b busEventos) avisarSesionesRevocadas(ctx context.Context, usuarioID uuid.UUID) {
	if err := b.publicar(ctx, ports.MensajeBus{Tipo: ports.MensajeSesionesRevocadas, UsuarioID: &usuarioID}); err != nil {
		log.Printf("[EVENTOS] no se pudo avisar la revocación de sesiones del usuario %s: %v", usuarioID, err)
	}
}
