package services

import (
	"context"
	"errors"
	"fmt"
	"log"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/infrastructure/crypto"

	"github.com/google/uuid"
)

// userServiceImpl implementa ports.UserService.
type userServiceImpl struct {
	userRepo     ports.UserRepository
	authRepo     ports.AuthRepository // para invalidar sesiones y resetear TOTP
	sesiones     almacenSesiones      // para que las sesiones revocadas se borren también de Redis
	bus          busEventos           // para cortar los streams de eventos de un usuario revocado
	emailService ports.EmailService
	auditSvc     ports.AuditService
}

// NewUserService crea una nueva instancia del servicio de usuarios.
func NewUserService(userRepo ports.UserRepository, authRepo ports.AuthRepository, kv ports.KeyValueStore, auditSvc ports.AuditService, emailService ports.EmailService) ports.UserService {
	return &userServiceImpl{
		userRepo:     userRepo,
		authRepo:     authRepo,
		sesiones:     almacenSesiones{kv: kv},
		bus:          busEventos{kv: kv},
		emailService: emailService,
		auditSvc:     auditSvc,
	}
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
//
// La cuenta nueva es independiente de cualquier cuenta eliminada que haya usado
// el mismo correo: UUID nuevo, nombre de usuario distinto (es único para
// siempre), clave temporal y 2FA propios, y sin permisos ni sesiones heredados.
//
// Riesgo conocido: el correo con la clave temporal se envía ANTES del INSERT
// (para no crear cuentas sin credenciales entregadas). Si dos altas con el mismo
// correo compiten, las dos pueden pasar la verificación previa y enviar su
// correo, pero el índice único deja entrar solo a una: la otra responde 409
// USER_EMAIL_ALREADY_EXISTS y su correo ya enviado lleva una clave que no sirve.
// No hay transacción distribuida entre SMTP y PostgreSQL; el caso es raro (dos
// admins dando de alta el mismo correo a la vez) y no deja datos inconsistentes.
func (s *userServiceImpl) CrearUsuario(ctx context.Context, orgID uuid.UUID, actorID uuid.UUID, input ports.CrearUsuarioInput) (*ports.CrearUsuarioResult, error) {
	log.Printf("[USERS] crear usuario | email=%s | rol=%s | org=%s", input.EmailUsuario, input.Rol, orgID)

	// 1. Verificar unicidad de email y username en la organización
	if existe, err := s.userRepo.ExisteEmailEnOrg(ctx, input.EmailUsuario, orgID, nil); err != nil {
		return nil, err
	} else if existe {
		return nil, ports.ErrEmailYaRegistrado
	}

	if existe, err := s.userRepo.ExisteUsernameEnOrg(ctx, input.NombreUsuario, orgID, nil); err != nil {
		return nil, err
	} else if existe {
		return nil, ports.ErrUsernameYaRegistrado
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

	// 4. Enviar contraseña temporal por correo ANTES de persistir
	if err := s.emailService.EnviarCredencialesTemporales(input.EmailUsuario, input.NombreCompleto, contrasenaTemp); err != nil {
		log.Printf("[USERS] error al enviar email de bienvenida a %s: %v", input.EmailUsuario, err)
		return nil, fmt.Errorf("EMAIL_DELIVERY_FAILED: %w", err)
	}

	// 5. Construir y persistir el usuario
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

	log.Printf("[USERS] usuario creado y credenciales enviadas | id=%s | email=%s | rol=%s", nuevoUsuario.ID, input.EmailUsuario, input.Rol)

	s.auditSvc.Registrar(ctx, ports.RegistrarAuditoriaInput{
		UsuarioID: actorID,
		Accion:    ports.AccionCrearUsuario,
		Resultado: ports.ResultadoExito,
		Detalles:  map[string]any{"usuarioCreado": nuevoUsuario.ID.String(), "email": input.EmailUsuario, "rol": input.Rol},
	})

	return &ports.CrearUsuarioResult{
		ID:     nuevoUsuario.ID,
		Rol:    nuevoUsuario.Rol,
		Activo: nuevoUsuario.Activo,
	}, nil
}

// ActualizarUsuario actualiza parcialmente los datos de un usuario.
func (s *userServiceImpl) ActualizarUsuario(ctx context.Context, id, orgID uuid.UUID, actorID uuid.UUID, input ports.ActualizarUsuarioInput) (*ports.UsuarioResumenDTO, error) {
	// Verificar que el usuario existe en la organización y no fue eliminado
	usuario, err := s.buscarModificable(ctx, id, orgID)
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
			return nil, ports.ErrEmailYaRegistrado
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
			if err := revocarSesionesDeUsuario(ctx, s.authRepo, s.sesiones, s.bus, id); err != nil {
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
	s.auditSvc.Registrar(ctx, ports.RegistrarAuditoriaInput{
		UsuarioID: actorID,
		Accion:    ports.AccionActualizarUsuario,
		Resultado: ports.ResultadoExito,
		Detalles:  map[string]any{"usuarioAfectado": id.String(), "cambios": cambios},
	})
	return &dto, nil
}

// EliminarUsuario realiza la eliminación lógica del usuario. A diferencia de la
// suspensión (PUT con activo = false), es irreversible y libera el correo: la
// fila queda solo por la auditoría.
//
// Es fail-closed: en una sola transacción marca eliminado_en, borra el código de
// recuperación e invalida sus sesiones en PostgreSQL, y antes del COMMIT las
// borra de Redis y corta sus streams SSE. Si algo de eso falla se deshace todo y
// se devuelve ports.ErrRevocacionFallida: nunca se informa una baja con el
// acceso todavía vivo. Los tokens temporales pre-2FA y los tickets SSE que
// queden sueltos ya no sirven: VerificarTotp exige una cuenta activa y abrir un
// stream exige una sesión activa.
func (s *userServiceImpl) EliminarUsuario(ctx context.Context, id, orgID uuid.UUID, actorID uuid.UUID) error {
	// Verificar que existe en la organización y que no fue eliminado antes
	if _, err := s.buscarModificable(ctx, id, orgID); err != nil {
		return err
	}

	err := s.userRepo.EliminarLogicamente(ctx, id, func(sesionIDs []uuid.UUID) error {
		if err := s.sesiones.eliminarSesiones(ctx, sesionIDs...); err != nil {
			return fmt.Errorf("sesiones en el almacén efímero: %w", err)
		}
		if err := s.bus.publicar(ctx, ports.MensajeBus{Tipo: ports.MensajeSesionesRevocadas, UsuarioID: &id}); err != nil {
			return fmt.Errorf("corte de streams SSE: %w", err)
		}
		return nil
	})
	if err != nil {
		log.Printf("[USERS] no se eliminó el usuario %s: %v", id, err)
		if errors.Is(err, ports.ErrRevocacionFallida) {
			s.auditSvc.Registrar(ctx, ports.RegistrarAuditoriaInput{
				UsuarioID: actorID,
				Accion:    ports.AccionEliminarUsuario,
				Resultado: ports.ResultadoFalla,
				Detalles:  map[string]any{"usuarioEliminado": id.String(), "razon": "no se pudo revocar el acceso"},
			})
		}
		return err
	}

	log.Printf("[USERS] usuario eliminado (eliminación lógica) | id=%s", id)
	s.auditSvc.Registrar(ctx, ports.RegistrarAuditoriaInput{
		UsuarioID: actorID,
		Accion:    ports.AccionEliminarUsuario,
		Resultado: ports.ResultadoExito,
		Detalles:  map[string]any{"usuarioEliminado": id.String()},
	})
	return nil
}

// ObtenerPermisos devuelve los permisos (vmid + nivel de acceso) asignados a un usuario.
func (s *userServiceImpl) ObtenerPermisos(ctx context.Context, usuarioID, orgID uuid.UUID) ([]ports.PermisoInstanciaInput, error) {
	if _, err := s.userRepo.BuscarUsuarioPorIDEnOrg(ctx, usuarioID, orgID); err != nil {
		return nil, err
	}
	return s.userRepo.ListarPermisosConNivel(ctx, usuarioID)
}

// AsignarPermisos reemplaza todos los permisos de instancia de un usuario operador.
func (s *userServiceImpl) AsignarPermisos(ctx context.Context, usuarioID, orgID uuid.UUID, actorID uuid.UUID, permisos []ports.PermisoInstanciaInput) error {
	// Verificar que el usuario existe en la organización y no fue eliminado
	if _, err := s.buscarModificable(ctx, usuarioID, orgID); err != nil {
		return err
	}

	if err := s.userRepo.ReemplazarPermisos(ctx, usuarioID, permisos); err != nil {
		return err
	}

	log.Printf("[USERS] permisos asignados | user=%s | permisos=%v", usuarioID, permisos)
	s.auditSvc.Registrar(ctx, ports.RegistrarAuditoriaInput{
		UsuarioID: actorID,
		Accion:    ports.AccionAsignarPermisos,
		Resultado: ports.ResultadoExito,
		Detalles:  map[string]any{"usuarioAfectado": usuarioID.String(), "permisos": permisos},
	})
	return nil
}

// ResetearContrasena genera una nueva contraseña temporal y la aplica al usuario.
func (s *userServiceImpl) ResetearContrasena(ctx context.Context, usuarioID, orgID uuid.UUID, actorID uuid.UUID) (string, error) {
	usuario, err := s.buscarModificable(ctx, usuarioID, orgID)
	if err != nil {
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

	// Enviar correo antes de modificar la DB
	if err := s.emailService.EnviarCredencialesTemporales(usuario.EmailUsuario, usuario.NombreCompleto, contrasenaTemp); err != nil {
		log.Printf("[USERS] error al enviar email de reset a %s: %v", usuario.EmailUsuario, err)
		return "", fmt.Errorf("EMAIL_DELIVERY_FAILED: %w", err)
	}

	if err := s.userRepo.ActualizarUsuario(ctx, usuarioID, map[string]any{
		"contrasena_hash":   hash,
		"cambio_contrasena": true,
	}); err != nil {
		return "", err
	}

	// Invalidar sesiones para forzar re-login con la nueva clave
	if err := revocarSesionesDeUsuario(ctx, s.authRepo, s.sesiones, s.bus, usuarioID); err != nil {
		log.Printf("[USERS] advertencia: error al invalidar sesiones al resetear contraseña %s: %v", usuarioID, err)
	}

	log.Printf("[USERS] contraseña reseteada y enviada | user=%s", usuarioID)

	s.auditSvc.Registrar(ctx, ports.RegistrarAuditoriaInput{
		UsuarioID: actorID,
		Accion:    ports.AccionResetearContrasena,
		Resultado: ports.ResultadoExito,
		Detalles:  map[string]any{"usuarioAfectado": usuarioID.String()},
	})
	return contrasenaTemp, nil
}

// ResetearTotp invalida el 2FA del usuario, forzando revinculación en el próximo login.
func (s *userServiceImpl) ResetearTotp(ctx context.Context, usuarioID, orgID uuid.UUID, actorID uuid.UUID) error {
	if _, err := s.buscarModificable(ctx, usuarioID, orgID); err != nil {
		return err
	}

	if err := s.authRepo.ResetearTotp(ctx, usuarioID); err != nil {
		return err
	}

	// Invalidar sesiones para forzar re-login
	if err := revocarSesionesDeUsuario(ctx, s.authRepo, s.sesiones, s.bus, usuarioID); err != nil {
		log.Printf("[USERS] advertencia: error al invalidar sesiones al resetear TOTP %s: %v", usuarioID, err)
	}

	log.Printf("[USERS] TOTP reseteado | user=%s", usuarioID)
	s.auditSvc.Registrar(ctx, ports.RegistrarAuditoriaInput{
		UsuarioID: actorID,
		Accion:    ports.AccionResetearTotp,
		Resultado: ports.ResultadoExito,
		Detalles:  map[string]any{"usuarioAfectado": usuarioID.String()},
	})
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
		detalles := ""
		if r.Detalles != nil {
			detalles = *r.Detalles
		}
		dtos[i] = ports.ActividadDTO{
			ID:              r.ID,
			FechaHora:       r.FechaHora,
			Accion:          r.Accion,
			InstanciaID:     r.InstanciaID,
			InstanciaNombre: r.InstanciaNombre,
			Resultado:       r.Resultado,
			Detalles:        detalles,
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
			return nil, ports.ErrEmailYaRegistrado
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

	// Validar complejidad de la nueva contraseña
	if err := crypto.ValidarComplejidadContrasena(input.ContrasenaNueva); err != nil {
		return err
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

	log.Printf("[USERS] contraseña cambiada | user=%s | cambio_obligatorio_resuelto=%v", usuarioID, usuario.CambioContrasena)
	s.auditSvc.Registrar(ctx, ports.RegistrarAuditoriaInput{
		UsuarioID: usuarioID,
		Accion:    ports.AccionCambiarContrasena,
		Resultado: ports.ResultadoExito,
	})
	return nil
}

// ==========================================
// Helpers internos
// ==========================================

// construirDetalleDTO arma el UsuarioDetalleDTO incluyendo los permisos de instancia.
// buscarModificable busca el usuario para una operación que lo modifica. Los
// eliminados se siguen pudiendo consultar (detalle y actividad), pero no modificar.
func (s *userServiceImpl) buscarModificable(ctx context.Context, id, orgID uuid.UUID) (*domain.Usuario, error) {
	usuario, err := s.userRepo.BuscarUsuarioPorIDEnOrg(ctx, id, orgID)
	if err != nil {
		return nil, err
	}
	if usuario.EliminadoEn != nil {
		return nil, ports.ErrUsuarioEliminado
	}
	return usuario, nil
}

func (s *userServiceImpl) construirDetalleDTO(ctx context.Context, usuario *domain.Usuario) (*ports.UsuarioDetalleDTO, error) {
	permisosInput, err := s.userRepo.ListarPermisosConNivel(ctx, usuario.ID)
	if err != nil {
		return nil, err
	}

	vmids := make([]int, len(permisosInput))
	permisosDTO := make([]ports.PermisoInstanciaDTO, len(permisosInput))
	for i, p := range permisosInput {
		vmids[i] = p.Vmid
		permisosDTO[i] = ports.PermisoInstanciaDTO{
			VMID:        p.Vmid,
			NivelAcceso: p.NivelAcceso,
		}
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
		Permisos:                  permisosDTO,
		EliminadoEn:               usuario.EliminadoEn,
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
