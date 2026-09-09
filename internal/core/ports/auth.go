package ports

import (
	"context"
	"time"

	"el-centinela/internal/core/domain"
	"github.com/google/uuid"
)

type LoginRequest struct {
	Email          string
	Password       string
	RememberSession bool
}

type LoginResponse struct {
	Success                bool        `json:"success"`
	User                   UserSession `json:"user"`
	RequiresTwoFactor      bool        `json:"requiresTwoFactor"`
	RequiresTwoFactorSetup bool        `json:"requiresTwoFactorSetup"`
	ChallengeToken         string      `json:"challengeToken,omitempty"`
	ChallengeExpiresAt     time.Time   `json:"challengeExpiresAt,omitempty"`
	AccessToken            string      `json:"accessToken,omitempty"`
	RefreshToken           string      `json:"refreshToken,omitempty"`
	AccessExpiresAt        time.Time   `json:"accessExpiresAt,omitempty"`
	RefreshExpiresAt       time.Time   `json:"refreshExpiresAt,omitempty"`
	RememberSession        bool        `json:"recordarSesion"`
}

type UserSession struct {
	ID             uuid.UUID `json:"id"`
	OrganizationID uuid.UUID `json:"organizacionId"`
	FullName       string    `json:"nombreCompleto"`
	Email          string    `json:"email"`
	Role           string    `json:"rol"`
	InstanceIDs    []int     `json:"instanciasPermitidas"`
	HasTwoFactor   bool      `json:"tiene2FA"`
}

type TwoFactorSetupRequest struct {
	ChallengeToken string
	Code           string
}

type TwoFactorSetupResponse struct {
	Secret         string    `json:"secret"`
	OTPAuthURL     string    `json:"otpAuthUrl"`
	ChallengeToken string    `json:"challengeToken"`
	ExpiresAt      time.Time `json:"expiresAt"`
}

type TwoFactorVerifyRequest struct {
	ChallengeToken string
	Code           string
}

type RefreshRequest struct {
	RefreshToken string
}

type LogoutRequest struct {
	RefreshToken string
}

type AuthService interface {
	Login(context.Context, LoginRequest) (LoginResponse, error)
	SetupTwoFactor(context.Context, TwoFactorSetupRequest) (TwoFactorSetupResponse, error)
	VerifyTwoFactor(context.Context, TwoFactorVerifyRequest) (LoginResponse, error)
	Refresh(context.Context, RefreshRequest) (LoginResponse, error)
	Logout(context.Context, LogoutRequest) error
}

type UserRepository interface {
	FindByEmail(context.Context, string) (*domain.Usuario, error)
	FindByID(context.Context, uuid.UUID) (*domain.Usuario, error)
	UpdateLastAccess(context.Context, uuid.UUID, time.Time) error
	SetTwoFactorSecret(context.Context, uuid.UUID, string) error
	EnableTwoFactor(context.Context, uuid.UUID) error
}

type PermissionRepository interface {
	ListInstanceIDs(context.Context, uuid.UUID) ([]int, error)
}

type Session struct {
	ID             uuid.UUID
	UserID         uuid.UUID
	ChallengeHash  string
	RefreshHash    string
	ExpiresAt      time.Time
	ConsumedAt     *time.Time
	Remember       bool
	Purpose        string
}

type SessionRepository interface {
	CreateChallenge(context.Context, Session) error
	ConsumeChallenge(context.Context, string, time.Time) (*Session, error)
	FindActiveRefresh(context.Context, string, time.Time) (*Session, error)
	CreateRefreshSession(context.Context, Session) error
	RotateRefreshSession(context.Context, string, time.Time, Session) error
	RevokeRefreshSession(context.Context, string, time.Time) error
}

type PasswordHasher interface {
	Compare(password, hash string) error
}

type TwoFactorProvider interface {
	NewSecret(email string) (secret string, otpAuthURL string, err error)
	Verify(secret, code string) bool
}

type TokenPair struct {
	AccessToken     string
	RefreshToken    string
	AccessExpiresAt time.Time
	RefreshExpiresAt time.Time
}

type TokenProvider interface {
	Issue(UserSession, time.Duration, time.Duration) (TokenPair, error)
}

type AuditRepository interface {
	Record(context.Context, *domain.Auditoria) error
}

type SecretProtector interface {
	Encrypt(string) (string, error)
	Decrypt(string) (string, error)
}