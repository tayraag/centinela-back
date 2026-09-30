package services

import (
	"context"
	"fmt"

	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
)

// revocarSesionesDeUsuario invalida todas las sesiones de un usuario en
// PostgreSQL y las borra del almacén efímero, para que dejen de funcionar en
// el acto (desactivar o eliminar usuario, reset de contraseña o de 2FA).
// También avisa por el bus para cortar los streams de eventos del usuario.
func revocarSesionesDeUsuario(ctx context.Context, repo ports.AuthRepository, sesiones almacenSesiones, bus busEventos, usuarioID uuid.UUID) error {
	ids, err := repo.InvalidarSesionesDeUsuario(ctx, usuarioID)
	if err != nil {
		return err
	}
	bus.avisarSesionesRevocadas(ctx, usuarioID)
	if err := sesiones.eliminarSesiones(ctx, ids...); err != nil {
		return fmt.Errorf("sesiones revocadas en PostgreSQL pero no en el almacén efímero: %w", err)
	}
	return nil
}
