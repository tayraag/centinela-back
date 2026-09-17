package services

import (
	"context"
	"fmt"
	"log"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/infrastructure/crypto"

	"github.com/google/uuid"
)

// userServiceImpl implementa ports.UserService.
type userServiceImpl struct {
	userRepo ports.UserRepository
	authRepo ports.AuthRepository // para invalidar sesiones y resetear TOTP
}

// NewUserService crea una nueva instancia del servicio de usuarios.
func NewUserService(userRepo ports.UserRepository, authRepo ports.AuthRepository) ports.UserService {
	return &userServiceImpl{userRepo: userRepo, authRepo: authRepo}
}

// ==========================================
// Gestión de usuarios (solo ADMIN)
// ==========================================

// ListarUsuarios devuelve la lista de usuarios de la organización con estadísticas.
// solicitanteID se usa para marcar el campo EsUsuarioActual en cada DTO.
func (s *userServiceImpl) ListarUsuarios(ctx context.Context, orgID, solicitanteID uuid.UUID, filtros ports.FiltrosUsuario) (*ports.ListaUsuariosResult, error) {
	resultado, err := s.userRepo.ListarUsuarios(ctx, orgID, filtros)
	if err != nil {
		return nil, err
	}

	// Marcar cuál es el usuario que hace la petición
	for i := range resultado.Users {
		resultado.Users[i].EsUsuarioActual = resultado.Users[i].ID == solicitanteID
	}
	return resultado, nil
}

// ObtenerUsuario devuelve el perfil completo de un usuario con sus permisos de instancia.
func (s *userServiceImpl) ObtenerUsuario(ctx context.Context, id, orgID uuid.UUID) (*ports.UsuarioDetalleDTO, error) {
	usuario, err := s.userRepo.BuscarUsuarioPorIDEnOrg(ctx, id, orgID)
	if err != nil {
		return nil, err
	}
	return s.construirDetalleDTO(ctx, usuario)
}

// CrearUsuario crea un nuevo usuario con contraseña temporal generada automáticamente.
// No envía email (Plan A): devuelve la contraseña en texto plano solo en este momento.
func (s *userServiceImpl) CrearUsuario(ctx context.Context, orgID uuid.UUID, input ports.CrearUsuarioInput) (*ports.CrearUsuarioResult, error) {
	log.Printf("[USERS] crear usuario | email=%s | rol=%s | org=%s", input.EmailUsuario, input.Rol, orgID)

	// 1. Verificar unicidad de email y username en la organización
	if existe, err := s.userRepo.ExisteEmailEnOrg(ctx, input.EmailUsuario, orgID, nil); err != nil {
		return nil, err
	} else if existe {
		return nil, fmt.Errorf("el email ya está registrado en esta organización")
	}

	if existe, err := s.userRepo.ExisteUsernameEnOrg(ctx, input.NombreUsuario, orgID, nil); err != nil {
		return nil, err
	} else if existe {
		return nil, fmt.Errorf("el nombre de usuario ya está registrado en esta organización")
	}

	// 2. Generar contraseña temporal segura
	contrasenaTemp, err := crypto.GenerarContrasenaTemp()
	if err != nil {
		return nil, fmt.Errorf("error al generar contraseña temporal: %w", err)
	}

	// 3. Hashear la contraseña
	hash, err := crypto.HashContrasena(contrasenaTemp)
	if err != nil {
		return nil, fmt.Errorf("error al hashear contraseña: %w", err)
	}

	// 4. Construir y persistir el usuario
	nuevoUsuario := &domain.Usuario{
		ID:               uuid.New(),
		OrganizacionID:   orgID,
		NombreCompleto:   input.NombreCompleto,
		NombreUsuario:    input.NombreUsuario,
		EmailUsuario:     input.EmailUsuario,
		ContrasenaHash:   hash,
		Rol:              input.Rol,
		Activo:           true,
		CambioContrasena: true,  // Obligado a cambiarla en el primer login
		TotpVinculado:    false, // Deberá configurar 2FA en el primer login
	}

	if err := s.userRepo.CrearUsuario(ctx, nuevoUsuario); err != nil {
		return nil, err
	}

	log.Printf("[USERS] usuario creado | id=%s | email=%s | rol=%s", nuevoUsuario.ID, input.EmailUsuario, input.Rol)

	return &ports.CrearUsuarioResult{
		ID:             nuevoUsuario.ID,
		Rol:            nuevoUsuario.Rol,
		Activo:         nuevoUsuario.Activo,
		ContrasenaTemp: contrasenaTemp, // Solo aquí, nunca más
	}, nil
}

// ActualizarUsuario actualiza parcialmente los datos de un usuario.
func (s *userServiceImpl) ActualizarUsuario(ctx context.Context, id, orgID uuid.UUID, input ports.ActualizarUsuarioInput) (*ports.UsuarioResumenDTO, error) {
	// Verificar que el usuario existe en la organización
	usuario, err := s.userRepo.BuscarUsuarioPorIDEnOrg(ctx, id, orgID)
	if err != nil {
		return nil, err
	}

	cambios := map[string]any{}

	if input.NombreCompleto != "" {
		cambios["nombre_completo"] = input.NombreCompleto
	}

	if input.EmailUsuario != "" && input.EmailUsuario != usuario.EmailUsuario {
		if existe, err := s.userRepo.ExisteEmailEnOrg(ctx, input.EmailUsuario, orgID, &id); err != nil {
			return nil, err
		} else if existe {
			return nil, fmt.Errorf("el email ya está registrado en esta organización")
		}
		cambios["email_usuario"] = input.EmailUsuario
	}

	if input.Rol != "" {
		cambios["rol"] = input.Rol
	}

	if input.Activo != nil {
		cambios["activo"] = *input.Activo
		// Si se desactiva el usuario, cerrar todas sus sesiones
		if !*input.Activo {
			if err := s.authRepo.InvalidarSesionesDeUsuario(ctx, id); err != nil {
				log.Printf("[USERS] advertencia: error al invalidar sesiones al desactivar usuario %s: %v", id, err)
			}
		}
	}

	if len(cambios) == 0 {
		// Nada que cambiar, devolver el usuario actual proyectado
		dto := usuarioAResumenDTO(*usuario)
		return &dto, nil
	}

	if err := s.userRepo.ActualizarUsuario(ctx, id, cambios); err != nil {
		return nil, err
	}

	// Releer el usuario actualizado para devolver el estado real
	usuarioActualizado, err := s.userRepo.BuscarUsuarioPorIDEnOrg(ctx, id, orgID)
	if err != nil {
		return nil, err
	}
	dto := usuarioAResumenDTO(*usuarioActualizado)
	log.Printf("[USERS] usuario actualizado | id=%s | cambios=%v", id, cambios)
	return &dto, nil
}

// EliminarUsuario realiza un soft-delete del usuario y cierra todas sus sesiones.
func (s *userServiceImpl) EliminarUsuario(ctx context.Context, id, orgID uuid.UUID) error {
	// Verificar que existe en la organización
	if _, err := s.userRepo.BuscarUsuarioPorIDEnOrg(ctx, id, orgID); err != nil {
		return err
	}

	// Soft-delete: marcar como inactivo
	if err := s.userRepo.ActualizarUsuario(ctx, id, map[string]any{"activo": false}); err != nil {
		return err
	}

	// Cerrar todas sus sesiones activas
	if err := s.authRepo.InvalidarSesionesDeUsuario(ctx, id); err != nil {
		log.Printf("[USERS] advertencia: error al invalidar sesiones al eliminar usuario %s: %v", id, err)
	}

	log.Printf("[USERS] usuario eliminado (soft-delete) | id=%s", id)
	return nil
}

// AsignarPermisos reemplaza todos los permisos de instancia de un usuario operador.
func (s *userServiceImpl) AsignarPermisos(ctx context.Context, usuarioID, orgID uuid.UUID, vmids []int) error {
	// Verificar que el usuario existe en la organización
	if _, err := s.userRepo.BuscarUsuarioPorIDEnOrg(ctx, usuarioID, orgID); err != nil {
		return err
	}

	if err := s.userRepo.ReemplazarPermisos(ctx, usuarioID, vmids); err != nil {
		return err
	}

	log.Printf("[USERS] permisos asignados | user=%s | vmids=%v", usuarioID, vmids)
	return nil
}

// ResetearContrasena genera una nueva contraseña temporal y la aplica al usuario.
func (s *userServiceImpl) ResetearContrasena(ctx context.Context, usuarioID, orgID uuid.UUID) (string, error) {
	if _, err := s.userRepo.BuscarUsuarioPorIDEnOrg(ctx, usuarioID, orgID); err != nil {
		return "", err
	}

	contrasenaTemp, err := crypto.GenerarContrasenaTemp()
	if err != nil {
		return "", fmt.Errorf("error al generar contraseña temporal: %w", err)
	}

	hash, err := crypto.HashContrasena(contrasenaTemp)
	if err != nil {
		return "", fmt.Errorf("error al hashear contraseña: %w", err)
	}

	if err := s.userRepo.ActualizarUsuario(ctx, usuarioID, map[string]any{
		"contrasena_hash":   hash,
		"cambio_contrasena": true,
	}); err != nil {
		return "", err
	}

	// Invalidar sesiones para forzar re-login con la nueva clave
	if err := s.authRepo.InvalidarSesionesDeUsuario(ctx, usuarioID); err != nil {
		log.Printf("[USERS] advertencia: error al invalidar sesiones al resetear contraseña %s: %v", usuarioID, err)
	}

	log.Printf("[USERS] contraseña reseteada | user=%s", usuarioID)
	return contrasenaTemp, nil
}

// ResetearTotp invalida el 2FA del usuario, forzando revinculación en el próximo login.
func (s *userServiceImpl) ResetearTotp(ctx context.Context, usuarioID, orgID uuid.UUID) error {
	if _, err := s.userRepo.BuscarUsuarioPorIDEnOrg(ctx, usuarioID, orgID); err != nil {
		return err
	}

	if err := s.authRepo.ResetearTotp(ctx, usuarioID); err != nil {
		return err
	}

	// Invalidar sesiones para forzar re-login
	if err := s.authRepo.InvalidarSesionesDeUsuario(ctx, usuarioID); err != nil {
		log.Printf("[USERS] advertencia: error al invalidar sesiones al resetear TOTP %s: %v", usuarioID, err)
	}

	log.Printf("[USERS] TOTP reseteado | user=%s", usuarioID)
	return nil
}

// ListarActividad devuelve el historial de acciones auditadas de un usuario.
func (s *userServiceImpl) ListarActividad(ctx context.Context, usuarioID, orgID uuid.UUID, filtros ports.FiltrosActividad) ([]ports.ActividadDTO, error) {
	if _, err := s.userRepo.BuscarUsuarioPorIDEnOrg(ctx, usuarioID, orgID); err != nil {
		return nil, err
	}

	registros, err := s.userRepo.ListarActividadDeUsuario(ctx, usuarioID, filtros)
	if err != nil {
		return nil, err
	}

	dtos := make([]ports.ActividadDTO, len(registros))
	for i, r := range registros {
		dtos[i] = ports.ActividadDTO{
			ID:              r.ID,
			FechaHora:       r.FechaHora,
			Accion:          r.Accion,
			InstanciaID:     r.InstanciaID,
			InstanciaNombre: r.InstanciaNombre,
			Resultado:       r.Resultado,
			Detalles:        r.Detalles,
		}
	}
	return dtos, nil
}

// ==========================================
// Perfil propio (cualquier usuario autenticado)
// ==========================================

// ObtenerPerfil devuelve el perfil del usuario autenticado.
func (s *userServiceImpl) ObtenerPerfil(ctx context.Context, usuarioID uuid.UUID) (*ports.UsuarioDetalleDTO, error) {
	usuario, err := s.authRepo.BuscarUsuarioPorID(ctx, usuarioID)
	if err != nil {
		return nil, err
	}
	return s.construirDetalleDTO(ctx, usuario)
}

// ActualizarPerfil permite al usuario modificar su propio nombre y email.
// No permite cambiar rol ni organización (eso es solo para admins).
func (s *userServiceImpl) ActualizarPerfil(ctx context.Context, usuarioID uuid.UUID, input ports.ActualizarPerfilInput) (*ports.UsuarioResumenDTO, error) {
	usuario, err := s.authRepo.BuscarUsuarioPorID(ctx, usuarioID)
	if err != nil {
		return nil, err
	}

	cambios := map[string]any{}

	if input.NombreCompleto != "" {
		cambios["nombre_completo"] = input.NombreCompleto
	}

	if input.EmailUsuario != "" && input.EmailUsuario != usuario.EmailUsuario {
		// Verificar unicidad en la misma organización
		if existe, err := s.userRepo.ExisteEmailEnOrg(ctx, input.EmailUsuario, usuario.OrganizacionID, &usuarioID); err != nil {
			return nil, err
		} else if existe {
			return nil, fmt.Errorf("el email ya está en uso")
		}
		cambios["email_usuario"] = input.EmailUsuario
	}

	if len(cambios) > 0 {
		if err := s.userRepo.ActualizarUsuario(ctx, usuarioID, cambios); err != nil {
			return nil, err
		}
	}

	// Releer el usuario actualizado
	usuarioActualizado, err := s.authRepo.BuscarUsuarioPorID(ctx, usuarioID)
	if err != nil {
		return nil, err
	}
	dto := usuarioAResumenDTO(*usuarioActualizado)
	return &dto, nil
}

// CambiarContrasena valida la contraseña actual y aplica la nueva.
// Si la contraseña es temporal (cambioContrasena=true), limpia ese flag.
func (s *userServiceImpl) CambiarContrasena(ctx context.Context, usuarioID uuid.UUID, input ports.CambiarContrasenaInput) error {
	usuario, err := s.authRepo.BuscarUsuarioPorID(ctx, usuarioID)
	if err != nil {
		return err
	}

	// Verificar que la contraseña actual es correcta
	if !crypto.VerificarContrasena(usuario.ContrasenaHash, input.ContrasenaActual) {
		return fmt.Errorf("la contraseña actual es incorrecta")
	}

	// No permitir reusar la misma contraseña
	if crypto.VerificarContrasena(usuario.ContrasenaHash, input.ContrasenaNueva) {
		return fmt.Errorf("la nueva contraseña no puede ser igual a la actual")
	}

	hash, err := crypto.HashContrasena(input.ContrasenaNueva)
	if err != nil {
		return fmt.Errorf("error al procesar la nueva contraseña: %w", err)
	}

	if err := s.userRepo.ActualizarUsuario(ctx, usuarioID, map[string]any{
		"contrasena_hash":   hash,
		"cambio_contrasena": false, // Quitar el flag de contraseña temporal
	}); err != nil {
		return err
	}

	log.Printf("[USERS] contraseña cambiada | user=%s", usuarioID)
	return nil
}

// ==========================================
// Helpers internos
// ==========================================

// construirDetalleDTO arma el UsuarioDetalleDTO incluyendo los permisos de instancia.
func (s *userServiceImpl) construirDetalleDTO(ctx context.Context, usuario *domain.Usuario) (*ports.UsuarioDetalleDTO, error) {
	vmids, err := s.userRepo.ListarPermisosDeUsuario(ctx, usuario.ID)
	if err != nil {
		return nil, err
	}

	return &ports.UsuarioDetalleDTO{
		ID:                        usuario.ID,
		NombreCompleto:            usuario.NombreCompleto,
		NombreUsuario:             usuario.NombreUsuario,
		EmailUsuario:              usuario.EmailUsuario,
		OrganizacionID:            usuario.OrganizacionID,
		Rol:                       usuario.Rol,
		Activo:                    usuario.Activo,
		TotpVinculado:             usuario.TotpVinculado,
		CambioContrasenaRequerido: usuario.CambioContrasena,
		FechaCreacion:             usuario.FechaCreacion,
		FechaUltimoAcceso:         usuario.FechaUltimoAcceso,
		InstanciasPermitidas:      vmids,
	}, nil
}

// usuarioAResumenDTO convierte un domain.Usuario en un UsuarioResumenDTO (sin campos sensibles).
func usuarioAResumenDTO(u domain.Usuario) ports.UsuarioResumenDTO {
	return ports.UsuarioResumenDTO{
		ID:                u.ID,
		NombreCompleto:    u.NombreCompleto,
		NombreUsuario:     u.NombreUsuario,
		EmailUsuario:      u.EmailUsuario,
		Rol:               u.Rol,
		Activo:            u.Activo,
		TotpVinculado:     u.TotpVinculado,
		FechaUltimoAcceso: u.FechaUltimoAcceso,
		FechaCreacion:     u.FechaCreacion,
	}
}
