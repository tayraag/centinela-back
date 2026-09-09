package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
	"github.com/google/uuid"
)

const (
	ChallengePurposeSetup  = "2FA_SETUP"
	ChallengePurposeVerify = "2FA_VERIFY"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrAccountInactive    = errors.New("account inactive")
	ErrPasswordChange     = errors.New("password change required")
	ErrChallengeInvalid   = errors.New("invalid or expired challenge")
	ErrTwoFactorInvalid   = errors.New("invalid two-factor code")
	ErrSessionInvalid     = errors.New("invalid session")
)

type AuthConfig struct {
	ChallengeTTL       time.Duration
	AccessTTL          time.Duration
	RefreshTTL         time.Duration
	RememberRefreshTTL time.Duration
}

type AuthService struct {
	users       ports.UserRepository
	permissions ports.PermissionRepository
	sessions    ports.SessionRepository
	passwords   ports.PasswordHasher
	twoFactor   ports.TwoFactorProvider
	secrets     ports.SecretProtector
	tokens      ports.TokenProvider
	audits      ports.AuditRepository
	config      AuthConfig
	now         func() time.Time
}

func NewAuthService(users ports.UserRepository, permissions ports.PermissionRepository, sessions ports.SessionRepository, passwords ports.PasswordHasher, twoFactor ports.TwoFactorProvider, secrets ports.SecretProtector, tokens ports.TokenProvider, audits ports.AuditRepository, config AuthConfig) *AuthService {
	return &AuthService{users: users, permissions: permissions, sessions: sessions, passwords: passwords, twoFactor: twoFactor, secrets: secrets, tokens: tokens, audits: audits, config: config, now: time.Now}
}

func (service *AuthService) Login(ctx context.Context, request ports.LoginRequest) (ports.LoginResponse, error) {
	email := strings.ToLower(strings.TrimSpace(request.Email))
	user, err := service.users.FindByEmail(ctx, email)
	if err != nil || user == nil || service.passwords.Compare(request.Password, user.ContrasenaHash) != nil {
		service.audit(ctx, userID(user), "LOGIN", "FALLA", "invalid credentials")
		return ports.LoginResponse{}, ErrInvalidCredentials
	}
	if !user.Activo {
		service.audit(ctx, user.ID, "LOGIN", "FALLA", "inactive account")
		return ports.LoginResponse{}, ErrAccountInactive
	}
	if user.DebeCambiarContrasena {
		service.audit(ctx, user.ID, "LOGIN", "FALLA", "password change required")
		return ports.LoginResponse{}, ErrPasswordChange
	}

	session, err := service.createChallenge(ctx, user, request.RememberSession, challengePurpose(user))
	if err != nil {
		return ports.LoginResponse{}, err
	}
	return ports.LoginResponse{
		Success:                true,
		User:                   service.sessionUser(ctx, user),
		RequiresTwoFactor:      true,
		RequiresTwoFactorSetup: !user.TotpVinculado,
		ChallengeToken:         session.ChallengeHash,
		ChallengeExpiresAt:     session.ExpiresAt,
		RememberSession:        request.RememberSession,
	}, nil
}

func (service *AuthService) SetupTwoFactor(ctx context.Context, request ports.TwoFactorSetupRequest) (ports.TwoFactorSetupResponse, error) {
	session, err := service.sessions.ConsumeChallenge(ctx, hashToken(request.ChallengeToken), service.now())
	if err != nil || session == nil || session.Purpose != ChallengePurposeSetup {
		return ports.TwoFactorSetupResponse{}, ErrChallengeInvalid
	}
	user, err := service.users.FindByID(ctx, session.UserID)
	if err != nil || user == nil || user.ID != session.UserID {
		return ports.TwoFactorSetupResponse{}, ErrChallengeInvalid
	}
	secret, otpURL, err := service.twoFactor.NewSecret(user.EmailUsuario)
	if err != nil {
		return ports.TwoFactorSetupResponse{}, fmt.Errorf("create two-factor secret: %w", err)
	}
	protectedSecret, err := service.secrets.Encrypt(secret)
	if err != nil {
		return ports.TwoFactorSetupResponse{}, fmt.Errorf("protect two-factor secret: %w", err)
	}
	if err := service.users.SetTwoFactorSecret(ctx, user.ID, protectedSecret); err != nil {
		return ports.TwoFactorSetupResponse{}, fmt.Errorf("save two-factor secret: %w", err)
	}
	verification, err := service.createChallenge(ctx, user, session.Remember, ChallengePurposeVerify)
	if err != nil {
		return ports.TwoFactorSetupResponse{}, err
	}
	return ports.TwoFactorSetupResponse{Secret: secret, OTPAuthURL: otpURL, ChallengeToken: verification.ChallengeHash, ExpiresAt: verification.ExpiresAt}, nil
}

func (service *AuthService) VerifyTwoFactor(ctx context.Context, request ports.TwoFactorVerifyRequest) (ports.LoginResponse, error) {
	session, err := service.sessions.ConsumeChallenge(ctx, hashToken(request.ChallengeToken), service.now())
	if err != nil || session == nil || (session.Purpose != ChallengePurposeSetup && session.Purpose != ChallengePurposeVerify) {
		return ports.LoginResponse{}, ErrChallengeInvalid
	}
	user, err := service.users.FindByID(ctx, session.UserID)
	if err != nil || user == nil {
		return ports.LoginResponse{}, ErrTwoFactorInvalid
	}
	secret, err := service.secrets.Decrypt(user.SecretoTotpCifrado)
	if err != nil || !service.twoFactor.Verify(secret, request.Code) {
		return ports.LoginResponse{}, ErrTwoFactorInvalid
	}
	if !user.TotpVinculado {
		if err := service.users.EnableTwoFactor(ctx, user.ID); err != nil {
			return ports.LoginResponse{}, fmt.Errorf("enable two-factor: %w", err)
		}
		user.TotpVinculado = true
	}
	return service.issueSession(ctx, user, session.Remember)
}

func (service *AuthService) Refresh(ctx context.Context, request ports.RefreshRequest) (ports.LoginResponse, error) {
	if strings.TrimSpace(request.RefreshToken) == "" {
		return ports.LoginResponse{}, ErrSessionInvalid
	}
	now := service.now()
	current, err := service.sessions.FindActiveRefresh(ctx, hashToken(request.RefreshToken), now)
	if err != nil || current == nil {
		return ports.LoginResponse{}, ErrSessionInvalid
	}
	user, err := service.users.FindByID(ctx, current.UserID)
	if err != nil || user == nil || !user.Activo {
		return ports.LoginResponse{}, ErrSessionInvalid
	}
	permissions, err := service.permissions.ListInstanceIDs(ctx, user.ID)
	if err != nil {
		return ports.LoginResponse{}, fmt.Errorf("load permissions: %w", err)
	}
	profile := service.sessionUserWithPermissions(user, permissions)
	refreshTTL := service.config.RefreshTTL
	if current.Remember {
		refreshTTL = service.config.RememberRefreshTTL
	}
	pair, err := service.tokens.Issue(profile, service.config.AccessTTL, refreshTTL)
	if err != nil {
		return ports.LoginResponse{}, fmt.Errorf("issue tokens: %w", err)
	}
	next := ports.Session{ID: uuid.New(), UserID: user.ID, RefreshHash: hashToken(pair.RefreshToken), ExpiresAt: pair.RefreshExpiresAt, Remember: current.Remember, Purpose: "REFRESH"}
	if err := service.sessions.RotateRefreshSession(ctx, hashToken(request.RefreshToken), now, next); err != nil {
		return ports.LoginResponse{}, ErrSessionInvalid
	}
	return ports.LoginResponse{Success: true, User: profile, AccessToken: pair.AccessToken, RefreshToken: pair.RefreshToken, AccessExpiresAt: pair.AccessExpiresAt, RefreshExpiresAt: pair.RefreshExpiresAt, RememberSession: current.Remember}, nil
}

func (service *AuthService) Logout(ctx context.Context, request ports.LogoutRequest) error {
	if strings.TrimSpace(request.RefreshToken) == "" {
		return ErrSessionInvalid
	}
	return service.sessions.RevokeRefreshSession(ctx, hashToken(request.RefreshToken), service.now())
}

func (service *AuthService) createChallenge(ctx context.Context, user *domain.Usuario, remember bool, purpose string) (ports.Session, error) {
	token := uuid.NewString()
	session := ports.Session{ID: uuid.New(), UserID: user.ID, ChallengeHash: hashToken(token), ExpiresAt: service.now().Add(service.config.ChallengeTTL), Remember: remember, Purpose: purpose}
	if err := service.sessions.CreateChallenge(ctx, session); err != nil {
		return ports.Session{}, fmt.Errorf("create authentication challenge: %w", err)
	}
	session.ChallengeHash = token
	return session, nil
}

func (service *AuthService) issueSession(ctx context.Context, user *domain.Usuario, remember bool) (ports.LoginResponse, error) {
	permissions, err := service.permissions.ListInstanceIDs(ctx, user.ID)
	if err != nil {
		return ports.LoginResponse{}, fmt.Errorf("load permissions: %w", err)
	}
	profile := service.sessionUserWithPermissions(user, permissions)
	refreshTTL := service.config.RefreshTTL
	if remember {
		refreshTTL = service.config.RememberRefreshTTL
	}
	pair, err := service.tokens.Issue(profile, service.config.AccessTTL, refreshTTL)
	if err != nil {
		return ports.LoginResponse{}, fmt.Errorf("issue tokens: %w", err)
	}
	if err := service.sessions.CreateRefreshSession(ctx, ports.Session{ID: uuid.New(), UserID: user.ID, RefreshHash: hashToken(pair.RefreshToken), ExpiresAt: pair.RefreshExpiresAt, Remember: remember, Purpose: "REFRESH"}); err != nil {
		return ports.LoginResponse{}, fmt.Errorf("save refresh session: %w", err)
	}
	accessedAt := service.now()
	if err := service.users.UpdateLastAccess(ctx, user.ID, accessedAt); err != nil {
		return ports.LoginResponse{}, fmt.Errorf("update last access: %w", err)
	}
	service.audit(ctx, user.ID, "LOGIN", "EXITO", "two-factor verified")
	return ports.LoginResponse{Success: true, User: profile, AccessToken: pair.AccessToken, RefreshToken: pair.RefreshToken, AccessExpiresAt: pair.AccessExpiresAt, RefreshExpiresAt: pair.RefreshExpiresAt, RememberSession: remember}, nil
}

func (service *AuthService) sessionUser(ctx context.Context, user *domain.Usuario) ports.UserSession {
	permissions, _ := service.permissions.ListInstanceIDs(ctx, user.ID)
	return service.sessionUserWithPermissions(user, permissions)
}

func (service *AuthService) sessionUserWithPermissions(user *domain.Usuario, permissions []int) ports.UserSession {
	return ports.UserSession{ID: user.ID, OrganizationID: user.OrganizacionID, FullName: user.NombreCompleto, Email: user.EmailUsuario, Role: user.Rol, InstanceIDs: permissions, HasTwoFactor: user.TotpVinculado}
}

func (service *AuthService) audit(ctx context.Context, userID uuid.UUID, action, result, details string) {
	if service.audits == nil {
		return
	}
	_ = service.audits.Record(ctx, &domain.Auditoria{UsuarioID: userID, Accion: action, Resultado: result, Detalles: details, FechaHora: service.now()})
}

func challengePurpose(user *domain.Usuario) string {
	if user.TotpVinculado {
		return ChallengePurposeVerify
	}
	return ChallengePurposeSetup
}

func userID(user *domain.Usuario) uuid.UUID {
	if user == nil {
		return uuid.Nil
	}
	return user.ID
}

func hashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}