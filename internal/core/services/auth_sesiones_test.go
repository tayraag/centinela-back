package services_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"el-centinela/internal/adapters/secondary/memoria"
	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"
	"el-centinela/internal/infrastructure/crypto"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
)

// ==========================================
// Dobles de prueba
// ==========================================

// repoSesiones es un AuthRepository en memoria que cuenta las escrituras sobre
// sesiones_activas, para verificar la regla "1 sesión = 1 fila".
type repoSesiones struct {
	mu        sync.Mutex
	usuario   *domain.Usuario
	sesiones  map[uuid.UUID]*domain.SesionActiva
	inserts   int
	updates   int
	ultimoAcc *time.Time
}

func (r *repoSesiones) BuscarUsuarioPorEmail(_ context.Context, email string) (*domain.Usuario, error) {
	if email != r.usuario.EmailUsuario {
		return nil, errors.New("no encontrado")
	}
	u := *r.usuario
	return &u, nil
}
func (r *repoSesiones) BuscarUsuarioPorID(_ context.Context, id uuid.UUID) (*domain.Usuario, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id != r.usuario.ID {
		return nil, errors.New("no encontrado")
	}
	u := *r.usuario
	return &u, nil
}
func (r *repoSesiones) CrearSesionYRegistrarAcceso(_ context.Context, s *domain.SesionActiva, fecha time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copia := *s
	r.sesiones[s.ID] = &copia
	r.inserts++
	r.ultimoAcc = &fecha
	return nil
}
func (r *repoSesiones) BuscarSesionPorID(_ context.Context, id uuid.UUID) (*domain.SesionActiva, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sesiones[id]
	if !ok {
		return nil, ports.ErrSesionRevocada
	}
	copia := *s
	return &copia, nil
}
func (r *repoSesiones) RotarJtiAccess(_ context.Context, id uuid.UUID, jtiRefresh, nuevo string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sesiones[id]
	if !ok || !s.Activa || s.JtiRefresh != jtiRefresh {
		return ports.ErrSesionRevocada
	}
	s.JtiAccess = nuevo
	r.updates++
	return nil
}
func (r *repoSesiones) DesactivarSesion(_ context.Context, id uuid.UUID, jtiRefresh string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sesiones[id]
	if !ok || s.JtiRefresh != jtiRefresh {
		return ports.ErrSesionRevocada
	}
	s.Activa = false
	r.updates++
	return nil
}
func (r *repoSesiones) InvalidarSesionesDeUsuario(_ context.Context, usuarioID uuid.UUID) ([]uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []uuid.UUID
	for id, s := range r.sesiones {
		if s.UsuarioID == usuarioID && s.Activa {
			s.Activa = false
			ids = append(ids, id)
		}
	}
	return ids, nil
}
func (r *repoSesiones) ActualizarTotp(_ context.Context, _ uuid.UUID, secreto string, vinculado bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.usuario.SecretoTotpCifrado, r.usuario.TotpVinculado = secreto, vinculado
	return nil
}
func (r *repoSesiones) ActualizarUltimoTotpPeriodo(_ context.Context, _ uuid.UUID, periodo int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.usuario.UltimoTotpPeriodo = &periodo
	return nil
}
func (r *repoSesiones) ResetearTotp(context.Context, uuid.UUID) error { return nil }
func (r *repoSesiones) ActualizarCodigoRecuperacion(context.Context, uuid.UUID, *string, *time.Time) error {
	return nil
}
func (r *repoSesiones) ActualizarIntentosRecuperacion(context.Context, uuid.UUID, int) error {
	return nil
}
func (r *repoSesiones) ActualizarContrasenaYLimpiarCodigo(context.Context, uuid.UUID, string) error {
	return nil
}

type auditoriaNula struct{}

func (auditoriaNula) Registrar(context.Context, ports.RegistrarAuditoriaInput) {}
func (auditoriaNula) ListarAuditoria(context.Context, uuid.UUID, ports.FiltrosAuditoria, ports.OpcionesAuditoria) (*ports.PaginaAuditoria, error) {
	return nil, nil
}
func (auditoriaNula) ExportarCSV(context.Context, uuid.UUID, ports.FiltrosAuditoria) ([]byte, error) {
	return nil, nil
}
func (auditoriaNula) ExportarJSON(context.Context, uuid.UUID, ports.FiltrosAuditoria) ([]byte, error) {
	return nil, nil
}

// ==========================================
// Entorno
// ==========================================

const (
	emailPrueba      = "operador@elcentinela.local"
	contrasenaPrueba = "Clave123!"
)

// bcrypt es lento a propósito: el hash se calcula una sola vez para todos los tests.
var (
	hashOnce   sync.Once
	hashPrueba string
)

type entornoAuth struct {
	svc          ports.AuthService
	repo         *repoSesiones
	cache        *memoria.SesionCache
	secretoTOTP  string
	periodoUsado int64
}

func nuevoEntornoAuth(t *testing.T) *entornoAuth {
	t.Helper()
	t.Setenv("JWT_SECRET", "secreto-jwt-de-prueba-con-mas-de-32-caracteres")
	t.Setenv("TOTP_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")

	clave, _ := totp.Generate(totp.GenerateOpts{Issuer: "El Centinela", AccountName: emailPrueba})
	cifrado, err := crypto.CifrarSecreto(clave.Secret(), "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	hashOnce.Do(func() { hashPrueba, _ = crypto.HashContrasena(contrasenaPrueba) })
	hash := hashPrueba
	repo := &repoSesiones{
		usuario: &domain.Usuario{
			ID: uuid.New(), OrganizacionID: uuid.New(), EmailUsuario: emailPrueba, ContrasenaHash: hash,
			Rol: "OPERATOR", Activo: true, TotpVinculado: true, SecretoTotpCifrado: cifrado,
		},
		sesiones: map[uuid.UUID]*domain.SesionActiva{},
	}
	cache := memoria.NewSesionCache()
	return &entornoAuth{
		svc: services.NewAuthService(repo, cache, nil, auditoriaNula{}), repo: repo, cache: cache, secretoTOTP: clave.Secret(),
	}
}

// login hace login + 2FA y devuelve los tokens. Simula el anti-replay del
// TOTP limpiando el último período usado (en la vida real se espera 30 s).
func (e *entornoAuth) login(t *testing.T) (*ports.TokenResult, *crypto.JWTClaims) {
	t.Helper()
	res, err := e.svc.Login(context.Background(), emailPrueba, contrasenaPrueba)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	pre, _ := crypto.VerificarToken(res.JWTTemporal, "secreto-jwt-de-prueba-con-mas-de-32-caracteres")
	e.repo.usuario.UltimoTotpPeriodo = nil
	codigo, _ := totp.GenerateCode(e.secretoTOTP, time.Now())
	tokens, err := e.svc.VerificarTotp(context.Background(), pre.ID, codigo)
	if err != nil {
		t.Fatalf("VerificarTotp: %v", err)
	}
	return tokens, pre
}

func claimsDe(t *testing.T, token string) *crypto.JWTClaims {
	t.Helper()
	c, err := crypto.VerificarToken(token, "secreto-jwt-de-prueba-con-mas-de-32-caracteres")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// ==========================================
// Criterios de aceptación
// ==========================================

// DoD: login + 2FA + 10 renovaciones = exactamente 1 fila en sesiones_activas.
func TestUnaSesionUnaFila_LoginMas10Renovaciones(t *testing.T) {
	e := nuevoEntornoAuth(t)
	tokens, _ := e.login(t)

	for i := 0; i < 10; i++ {
		nuevo, err := e.svc.RefrescarToken(context.Background(), tokens.RefreshToken)
		if err != nil {
			t.Fatalf("Renovación %d: %v", i+1, err)
		}
		tokens.AccessToken = nuevo.AccessToken
	}

	if e.repo.inserts != 1 || len(e.repo.sesiones) != 1 {
		t.Fatalf("Se esperaba exactamente 1 INSERT y 1 fila, hubo %d INSERT y %d filas", e.repo.inserts, len(e.repo.sesiones))
	}
	if e.repo.updates != 10 {
		t.Errorf("Se esperaban 10 UPDATE (uno por renovación), hubo %d", e.repo.updates)
	}

	// La fila y la réplica en el almacén tienen el último access emitido.
	c := claimsDe(t, tokens.AccessToken)
	sid := uuid.MustParse(c.SesionID)
	if e.repo.sesiones[sid].JtiAccess != c.ID {
		t.Error("La fila debe tener el jti_access del último access emitido")
	}
	if cacheada, err := e.cache.ObtenerSesion(context.Background(), sid); err != nil || cacheada.JtiAccess != c.ID {
		t.Errorf("La réplica en el almacén debe tener el último jti_access: %+v, %v", cacheada, err)
	}
}

// DoD: el pre-2FA no deja nada en PostgreSQL y se borra al superar el 2FA.
func TestPre2FA_SoloEnElAlmacenEfimero(t *testing.T) {
	e := nuevoEntornoAuth(t)
	res, err := e.svc.Login(context.Background(), emailPrueba, contrasenaPrueba)
	if err != nil {
		t.Fatal(err)
	}
	pre := claimsDe(t, res.JWTTemporal)

	if e.repo.inserts != 0 {
		t.Fatalf("El login no debe escribir en sesiones_activas, hubo %d INSERT", e.repo.inserts)
	}
	if usuario, err := e.cache.ObtenerPre2FA(context.Background(), pre.ID); err != nil || usuario != e.repo.usuario.ID {
		t.Fatalf("auth:pre2fa:<jti> debe existir con el usuario: %v, %v", usuario, err)
	}
	if err := e.svc.VerificarSesionPreAuth(context.Background(), pre.ID); err != nil {
		t.Errorf("El token temporal debe ser válido: %v", err)
	}

	codigo, _ := totp.GenerateCode(e.secretoTOTP, time.Now())
	if _, err := e.svc.VerificarTotp(context.Background(), pre.ID, codigo); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cache.ObtenerPre2FA(context.Background(), pre.ID); !errors.Is(err, ports.ErrSesionNoEncontrada) {
		t.Error("Después del 2FA la clave auth:pre2fa:<jti> debe estar eliminada")
	}
	if err := e.svc.VerificarSesionPreAuth(context.Background(), pre.ID); err == nil {
		t.Error("El token temporal ya usado no debe servir más")
	}
}

// DoD: fecha_ultimo_acceso se persiste al superar el 2FA (y no antes).
func TestUltimoAcceso_SeRegistraAlSuperarEl2FA(t *testing.T) {
	e := nuevoEntornoAuth(t)
	res, _ := e.svc.Login(context.Background(), emailPrueba, contrasenaPrueba)
	if e.repo.ultimoAcc != nil {
		t.Fatal("El login (sin 2FA) no debe registrar el último acceso")
	}
	pre := claimsDe(t, res.JWTTemporal)
	antes := time.Now()
	codigo, _ := totp.GenerateCode(e.secretoTOTP, time.Now())
	if _, err := e.svc.VerificarTotp(context.Background(), pre.ID, codigo); err != nil {
		t.Fatal(err)
	}
	if e.repo.ultimoAcc == nil || e.repo.ultimoAcc.Before(antes) {
		t.Errorf("fecha_ultimo_acceso debe registrarse al superar el 2FA: %v", e.repo.ultimoAcc)
	}
}

// DoD: al cerrar sesión se invalida al instante en el almacén y en PostgreSQL.
func TestCerrarSesion_InvalidaEnAmbosLados(t *testing.T) {
	e := nuevoEntornoAuth(t)
	tokens, _ := e.login(t)
	c := claimsDe(t, tokens.AccessToken)
	sid := uuid.MustParse(c.SesionID)

	if err := e.svc.VerificarSesionAccess(context.Background(), sid, c.ID); err != nil {
		t.Fatalf("Antes del logout el access debe ser válido: %v", err)
	}
	if err := e.svc.CerrarSesion(context.Background(), tokens.RefreshToken); err != nil {
		t.Fatal(err)
	}

	if e.repo.sesiones[sid].Activa {
		t.Error("La fila debe quedar con activa = false")
	}
	if _, err := e.cache.ObtenerSesion(context.Background(), sid); !errors.Is(err, ports.ErrSesionNoEncontrada) {
		t.Error("auth:session:<id> debe estar eliminada")
	}
	if err := e.svc.VerificarSesionAccess(context.Background(), sid, c.ID); err == nil {
		t.Error("Después del logout el access no debe servir")
	}
	if _, err := e.svc.RefrescarToken(context.Background(), tokens.RefreshToken); err == nil {
		t.Error("Después del logout el refresh no debe servir")
	}
	if e.repo.inserts != 1 {
		t.Errorf("El logout no debe insertar filas: %d INSERT", e.repo.inserts)
	}
}

// ==========================================
// Comportamiento adicional
// ==========================================

func TestRenovacion_InvalidaElAccessAnterior(t *testing.T) {
	e := nuevoEntornoAuth(t)
	tokens, _ := e.login(t)
	viejo := claimsDe(t, tokens.AccessToken)

	nuevo, err := e.svc.RefrescarToken(context.Background(), tokens.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	actual := claimsDe(t, nuevo.AccessToken)
	sid := uuid.MustParse(actual.SesionID)
	if viejo.SesionID != actual.SesionID {
		t.Error("La renovación debe mantener la misma sesión")
	}
	if err := e.svc.VerificarSesionAccess(context.Background(), sid, actual.ID); err != nil {
		t.Errorf("El access nuevo debe ser válido: %v", err)
	}
	if err := e.svc.VerificarSesionAccess(context.Background(), sid, viejo.ID); err == nil {
		t.Error("El access anterior no debe seguir siendo válido")
	}
}

// Si el almacén efímero pierde la sesión (reinicio de Redis, desalojo), la
// validación cae a PostgreSQL y la vuelve a replicar.
func TestVerificarAccess_RespaldoEnPostgreSQL(t *testing.T) {
	e := nuevoEntornoAuth(t)
	tokens, _ := e.login(t)
	c := claimsDe(t, tokens.AccessToken)
	sid := uuid.MustParse(c.SesionID)

	_ = e.cache.EliminarSesiones(context.Background(), sid) // simula que Redis la perdió
	if err := e.svc.VerificarSesionAccess(context.Background(), sid, c.ID); err != nil {
		t.Fatalf("Con la sesión solo en PostgreSQL debe seguir siendo válida: %v", err)
	}
	if _, err := e.cache.ObtenerSesion(context.Background(), sid); err != nil {
		t.Error("Después de validar contra PostgreSQL la sesión debe volver a estar en el almacén")
	}
}

func TestRevocarSesionesUsuario_BorraDelAlmacen(t *testing.T) {
	e := nuevoEntornoAuth(t)
	tokens1, _ := e.login(t)
	tokens2, _ := e.login(t) // dos navegadores = dos sesiones
	if len(e.repo.sesiones) != 2 {
		t.Fatalf("Dos logins deben ser dos sesiones, hay %d", len(e.repo.sesiones))
	}

	if err := e.svc.RevocarSesionesUsuario(context.Background(), e.repo.usuario.ID); err != nil {
		t.Fatal(err)
	}
	for _, tk := range []*ports.TokenResult{tokens1, tokens2} {
		c := claimsDe(t, tk.AccessToken)
		if err := e.svc.VerificarSesionAccess(context.Background(), uuid.MustParse(c.SesionID), c.ID); err == nil {
			t.Error("Después de revocar, ninguna sesión del usuario debe servir")
		}
	}
}

func TestVerificarTotp_TokenTemporalUsadoDosVeces(t *testing.T) {
	e := nuevoEntornoAuth(t)
	_, pre := e.login(t)
	e.repo.usuario.UltimoTotpPeriodo = nil
	codigo, _ := totp.GenerateCode(e.secretoTOTP, time.Now())
	if _, err := e.svc.VerificarTotp(context.Background(), pre.ID, codigo); err == nil {
		t.Fatal("Reusar un token temporal ya verificado debe fallar")
	}
	if e.repo.inserts != 1 {
		t.Errorf("El segundo intento no debe crear otra fila: %d INSERT", e.repo.inserts)
	}
}

func TestRefresh_TokenSinSesion(t *testing.T) {
	e := nuevoEntornoAuth(t)
	// Un refresh emitido con el esquema viejo (sin claim "sid") ya no sirve.
	viejo, _ := crypto.FirmarToken(crypto.JWTClaims{Tipo: crypto.TipoRefresh, Verificado2FA: true},
		"secreto-jwt-de-prueba-con-mas-de-32-caracteres", time.Hour)
	if _, err := e.svc.RefrescarToken(context.Background(), viejo); err == nil {
		t.Error("Un refresh sin sid debe ser rechazado")
	}
}
