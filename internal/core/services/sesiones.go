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
func revocarSesionesDeUsuario(ctx context.Context, repo ports.AuthRepository, cache ports.SesionCache, usuarioID uuid.UUID) error {
	ids, err := repo.InvalidarSesionesDeUsuario(ctx, usuarioID)
	if err != nil {
		return err
	}
	if err := cache.EliminarSesiones(ctx, ids...); err != nil {
		return fmt.Errorf("sesiones revocadas en PostgreSQL pero no en el almacén efímero: %w", err)
	}
	return nil
}
