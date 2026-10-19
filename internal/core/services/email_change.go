package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"strings"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/infrastructure/crypto"
	"github.com/google/uuid"
)

type cambioEmail struct {
	UsuarioID     uuid.UUID `json:"usuarioId"`
	NuevoEmail    string    `json:"nuevoEmail"`
	EmailOriginal string    `json:"emailOriginal"`
	SolicitudID   uuid.UUID `json:"solicitudId"`
	IP            string    `json:"ipSolicitud"`
}

func (s *userServiceImpl) solicitarCambioEmail(ctx context.Context, u *domain.Usuario, in ports.ActualizarPerfilInput) error {
	nuevo := strings.ToLower(strings.TrimSpace(*in.EmailUsuario))
	if nuevo == strings.ToLower(u.EmailUsuario) {
		return nil
	}
	if addr, err := mail.ParseAddress(nuevo); err != nil || addr.Address != nuevo {
		return ports.ErrPerfilInvalido
	}
	if in.ContrasenaActual == nil || *in.ContrasenaActual == "" || in.Codigo2FA == nil || *in.Codigo2FA == "" {
		return ports.ErrPerfilInvalido
	}
	if err := cuentaHabilitada(u); err != nil {
		return err
	}
	if !crypto.VerificarContrasena(u.ContrasenaHash, *in.ContrasenaActual) {
		return ports.ErrPerfilAuth
	}
	if !u.TotpVinculado || len(*in.Codigo2FA) != 6 {
		return ports.ErrPerfilOTP
	}
	for _, c := range *in.Codigo2FA {
		if c < '0' || c > '9' {
			return ports.ErrPerfilOTP
		}
	}
	valido, err := crypto.ValidarCodigo(u.SecretoTotpCifrado, os.Getenv("TOTP_ENCRYPTION_KEY"), *in.Codigo2FA)
	if err != nil {
		return fmt.Errorf("validar TOTP: %w", err)
	}
	if !valido {
		return ports.ErrPerfilOTP
	}
	if existe, err := s.userRepo.ExisteEmailEnOrg(ctx, nuevo, u.OrganizacionID, &u.ID); err != nil {
		return err
	} else if existe {
		return ports.ErrEmailYaRegistrado
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return err
	}
	token := hex.EncodeToString(tokenBytes)
	cambio := cambioEmail{u.ID, nuevo, u.EmailUsuario, uuid.New(), in.IP}
	// La auditoría es obligatoria: una solicitud no se habilita sin evidencia persistida.
	if err := s.auditSvc.RegistrarEstricto(ctx, cambio.auditoria("UPDATE_PROFILE_EMAIL_REQUESTED", "PENDIENTE", in.IP)); err != nil {
		return err
	}
	// Enviar primero la alerta. Si falla, nunca se habilita el token.
	if err := s.emailService.SendMail(u.EmailUsuario, "Alerta de seguridad: cambio de correo solicitado", fmt.Sprintf("Se solicitó cambiar su correo a %s desde la IP %s. Si no fue usted, proteja su cuenta inmediatamente.", nuevo, in.IP)); err != nil {
		return fmt.Errorf("EMAIL_DELIVERY_FAILED: %w", err)
	}
	key := "auth:email_change:" + token
	if err := s.sesiones.kv.Set(ctx, key, cambio, 15*time.Minute); err != nil {
		return err
	}
	if err := s.emailService.SendMail(nuevo, "Confirme su nuevo correo", "Confirme el cambio con este token (vence en 15 minutos): "+token); err != nil {
		_ = s.sesiones.kv.Del(context.WithoutCancel(ctx), key)
		return fmt.Errorf("EMAIL_DELIVERY_FAILED: %w", err)
	}
	return nil
}

func (c cambioEmail) auditoria(accion, resultado, ip string) ports.RegistrarAuditoriaInput {
	return ports.RegistrarAuditoriaInput{UsuarioID: c.UsuarioID, Accion: accion, Resultado: resultado, Detalles: map[string]any{"solicitudId": c.SolicitudID.String(), "emailOriginal": c.EmailOriginal, "nuevoEmail": c.NuevoEmail, "ip": ip, "ipSolicitud": c.IP}}
}

func (s *userServiceImpl) ConfirmarEmail(ctx context.Context, usuarioID uuid.UUID, token, ip string) error {
	if b, err := hex.DecodeString(token); err != nil || len(b) != 32 {
		return ports.ErrEmailToken
	}
	key := "auth:email_change:" + token
	raw, err := s.sesiones.kv.Get(ctx, key)
	if errors.Is(err, ports.ErrClaveNoEncontrada) {
		return ports.ErrEmailToken
	}
	if err != nil {
		return err
	}
	var c cambioEmail
	if json.Unmarshal([]byte(raw), &c) != nil || c.UsuarioID != usuarioID {
		return ports.ErrEmailToken
	}
	// GETDEL evita dos confirmaciones concurrentes, incluso entre réplicas.
	// Redis y PostgreSQL no comparten transacción: si la base falla después
	// del consumo, se solicita otro token. Se prioriza impedir su reutilización.
	consumed, err := s.sesiones.kv.GetDel(ctx, key)
	if errors.Is(err, ports.ErrClaveNoEncontrada) {
		return ports.ErrEmailToken
	}
	if err != nil {
		return err
	}
	if consumed != raw {
		return ports.ErrEmailToken
	}
	return s.userRepo.ConfirmarCambioEmail(ctx, usuarioID, c.EmailOriginal, c.NuevoEmail, c.auditoria("UPDATE_PROFILE_EMAIL_CONFIRMED", "SUCCESS", ip))
}
