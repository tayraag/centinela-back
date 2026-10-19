package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/memoria"
	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/infrastructure/crypto"
	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
)

type emailRepoTest struct {
	ports.UserRepository
	confirmed int
	audit     ports.RegistrarAuditoriaInput
}

func (r *emailRepoTest) ExisteEmailEnOrg(context.Context, string, uuid.UUID, *uuid.UUID) (bool, error) {
	return false, nil
}
func (r *emailRepoTest) ConfirmarCambioEmail(_ context.Context, _ uuid.UUID, _, _ string, a ports.RegistrarAuditoriaInput) error {
	r.confirmed++
	r.audit = a
	return nil
}

type emailAuditTest struct {
	ports.AuditService
	events []ports.RegistrarAuditoriaInput
	err    error
}

func (a *emailAuditTest) RegistrarEstricto(_ context.Context, in ports.RegistrarAuditoriaInput) error {
	a.events = append(a.events, in)
	return a.err
}

type emailSenderTest struct {
	ports.EmailService
	destinations []string
	bodies       []string
	err          error
}

func (e *emailSenderTest) SendMail(to, subject, body string) error {
	e.destinations = append(e.destinations, to)
	e.bodies = append(e.bodies, body)
	return e.err
}

func TestCambioEmailReautenticacionYConfirmacion(t *testing.T) {
	ctx := context.Background()
	key := "0123456789abcdef0123456789abcdef"
	t.Setenv("TOTP_ENCRYPTION_KEY", key)
	hash, err := crypto.HashContrasena("Clave123!")
	if err != nil {
		t.Fatal(err)
	}
	secret, err := crypto.CifrarSecreto("JBSWY3DPEHPK3PXP", key)
	if err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCode("JBSWY3DPEHPK3PXP", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	u := &domain.Usuario{ID: uuid.New(), EmailUsuario: "original@example.com", Activo: true, TotpVinculado: true, ContrasenaHash: hash, SecretoTotpCifrado: secret}
	nuevo := "nuevo@example.com"
	pass := "Clave123!"
	wrong := "incorrecta"
	badOTP := "abcdef"
	for _, tc := range []struct {
		name      string
		pass, otp *string
		want      error
	}{
		{"sin credenciales", nil, nil, ports.ErrPerfilInvalido},
		{"sin OTP", &pass, nil, ports.ErrPerfilInvalido},
		{"sin contraseña", nil, &code, ports.ErrPerfilInvalido},
		{"contraseña incorrecta", &wrong, &code, ports.ErrPerfilAuth},
		{"OTP incorrecto", &pass, &badOTP, ports.ErrPerfilOTP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &userServiceImpl{}
			err := s.solicitarCambioEmail(ctx, u, ports.ActualizarPerfilInput{EmailUsuario: &nuevo, ContrasenaActual: tc.pass, Codigo2FA: tc.otp})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
	now := time.Now()
	kv := memoria.NuevoConReloj(func() time.Time { return now })
	repo := &emailRepoTest{}
	audit := &emailAuditTest{}
	sender := &emailSenderTest{}
	s := &userServiceImpl{userRepo: repo, auditSvc: audit, emailService: sender, sesiones: almacenSesiones{kv: kv}}
	input := ports.ActualizarPerfilInput{EmailUsuario: &nuevo, ContrasenaActual: &pass, Codigo2FA: &code, IP: "192.0.2.1"}
	if err := s.solicitarCambioEmail(ctx, u, input); err != nil {
		t.Fatal(err)
	}
	if u.EmailUsuario != "original@example.com" || repo.confirmed != 0 {
		t.Fatal("correo modificado antes de confirmar")
	}
	if len(sender.destinations) != 2 || sender.destinations[0] != u.EmailUsuario || sender.destinations[1] != nuevo {
		t.Fatal("faltan correos de doble canal")
	}
	token := sender.bodies[1][len(sender.bodies[1])-64:]
	if len(token) != 64 || !strings.Contains(sender.bodies[0], input.IP) {
		t.Fatal("token o IP ausente")
	}
	if err := s.ConfirmarEmail(ctx, uuid.New(), token, "192.0.2.2"); !errors.Is(err, ports.ErrEmailToken) {
		t.Fatal(err)
	}
	if err := s.ConfirmarEmail(ctx, u.ID, token, "192.0.2.2"); err != nil {
		t.Fatal(err)
	}
	if repo.confirmed != 1 || repo.audit.Detalles["solicitudId"] != audit.events[0].Detalles["solicitudId"] {
		t.Fatal("auditoría sin correlación")
	}
	if err := s.ConfirmarEmail(ctx, u.ID, token, ""); !errors.Is(err, ports.ErrEmailToken) {
		t.Fatal("token reutilizado", err)
	}
	if err := s.solicitarCambioEmail(ctx, u, input); err != nil {
		t.Fatal(err)
	}
	token = sender.bodies[3][len(sender.bodies[3])-64:]
	now = now.Add(16 * time.Minute)
	if err := s.ConfirmarEmail(ctx, u.ID, token, ""); !errors.Is(err, ports.ErrEmailToken) {
		t.Fatal("token vencido aceptado", err)
	}
	audit.err = errors.New("auditoría caída")
	before := len(sender.destinations)
	if err := s.solicitarCambioEmail(ctx, u, input); err == nil || len(sender.destinations) != before {
		t.Fatal("no se cerró el flujo al fallar auditoría")
	}
}
