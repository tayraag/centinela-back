package memory

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
	"github.com/google/uuid"
)

var errNotFound = errors.New("record not found")

type UserRepository struct {
	mu    sync.RWMutex
	users map[uuid.UUID]*domain.Usuario
}

func NewUserRepository(user domain.Usuario) *UserRepository {
	return &UserRepository{users: map[uuid.UUID]*domain.Usuario{user.ID: &user}}
}

func (repository *UserRepository) FindByEmail(_ context.Context, email string) (*domain.Usuario, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	for _, user := range repository.users {
		if strings.EqualFold(user.EmailUsuario, email) {
			copy := *user
			return &copy, nil
		}
	}
	return nil, errNotFound
}

func (repository *UserRepository) FindByID(_ context.Context, id uuid.UUID) (*domain.Usuario, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	user, ok := repository.users[id]
	if !ok {
		return nil, errNotFound
	}
	copy := *user
	return &copy, nil
}

func (repository *UserRepository) UpdateLastAccess(_ context.Context, id uuid.UUID, accessedAt time.Time) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	user, ok := repository.users[id]
	if !ok {
		return errNotFound
	}
	user.FechaUltimoAcceso = &accessedAt
	log.Printf("[memory] último acceso actualizado user=%s at=%s", id, accessedAt.Format(time.RFC3339))
	return nil
}

func (repository *UserRepository) SetTwoFactorSecret(_ context.Context, id uuid.UUID, secret string) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	user, ok := repository.users[id]
	if !ok {
		return errNotFound
	}
	user.SecretoTotpCifrado = secret
	log.Printf("[memory] secreto TOTP cifrado guardado user=%s", id)
	return nil
}

func (repository *UserRepository) EnableTwoFactor(_ context.Context, id uuid.UUID) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	user, ok := repository.users[id]
	if !ok {
		return errNotFound
	}
	user.TotpVinculado = true
	log.Printf("[memory] 2FA habilitado user=%s", id)
	return nil
}

type PermissionRepository struct {
	instanceIDs map[uuid.UUID][]int
}

func NewPermissionRepository(userID uuid.UUID, instanceIDs []int) *PermissionRepository {
	return &PermissionRepository{instanceIDs: map[uuid.UUID][]int{userID: instanceIDs}}
}

func (repository *PermissionRepository) ListInstanceIDs(_ context.Context, userID uuid.UUID) ([]int, error) {
	return append([]int(nil), repository.instanceIDs[userID]...), nil
}

type AuditRepository struct{}

func (AuditRepository) Record(_ context.Context, audit *domain.Auditoria) error {
	log.Printf("[audit] user=%s action=%s result=%s details=%s", audit.UsuarioID, audit.Accion, audit.Resultado, audit.Detalles)
	return nil
}

type SessionRepository struct {
	mu       sync.Mutex
	sessions map[string]ports.Session
}

func NewSessionRepository() *SessionRepository {
	return &SessionRepository{sessions: make(map[string]ports.Session)}
}

func (repository *SessionRepository) CreateChallenge(_ context.Context, session ports.Session) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.sessions[session.ChallengeHash] = session
	log.Printf("[memory] desafío creado user=%s purpose=%s expires=%s", session.UserID, session.Purpose, session.ExpiresAt.Format(time.RFC3339))
	return nil
}

func (repository *SessionRepository) ConsumeChallenge(_ context.Context, tokenHash string, now time.Time) (*ports.Session, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	session, ok := repository.sessions[tokenHash]
	if !ok || session.ExpiresAt.Before(now) || session.Purpose == "REFRESH" {
		return nil, errNotFound
	}
	delete(repository.sessions, tokenHash)
	log.Printf("[memory] desafío consumido user=%s purpose=%s", session.UserID, session.Purpose)
	return &session, nil
}

func (repository *SessionRepository) FindActiveRefresh(_ context.Context, tokenHash string, now time.Time) (*ports.Session, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	session, ok := repository.sessions[tokenHash]
	if !ok || session.ExpiresAt.Before(now) || session.Purpose != "REFRESH" {
		return nil, errNotFound
	}
	return &session, nil
}

func (repository *SessionRepository) CreateRefreshSession(_ context.Context, session ports.Session) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.sessions[session.RefreshHash] = session
	log.Printf("[memory] refresh creado user=%s expires=%s remember=%t", session.UserID, session.ExpiresAt.Format(time.RFC3339), session.Remember)
	return nil
}

func (repository *SessionRepository) RotateRefreshSession(_ context.Context, oldHash string, now time.Time, next ports.Session) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	current, ok := repository.sessions[oldHash]
	if !ok || current.ExpiresAt.Before(now) || current.Purpose != "REFRESH" {
		return errNotFound
	}
	delete(repository.sessions, oldHash)
	repository.sessions[next.RefreshHash] = next
	log.Printf("[memory] refresh rotado user=%s expires=%s", next.UserID, next.ExpiresAt.Format(time.RFC3339))
	return nil
}

func (repository *SessionRepository) RevokeRefreshSession(_ context.Context, tokenHash string, now time.Time) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	session, ok := repository.sessions[tokenHash]
	if !ok || session.ExpiresAt.Before(now) || session.Purpose != "REFRESH" {
		return errNotFound
	}
	delete(repository.sessions, tokenHash)
	log.Printf("[memory] refresh revocado user=%s", session.UserID)
	return nil
}

var _ ports.UserRepository = (*UserRepository)(nil)
var _ ports.PermissionRepository = (*PermissionRepository)(nil)
var _ ports.AuditRepository = (*AuditRepository)(nil)
var _ ports.SessionRepository = (*SessionRepository)(nil)