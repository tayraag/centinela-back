package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/core/ports"
	"el-centinela/internal/core/services"
	"el-centinela/internal/infrastructure/crypto"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type repoUsuarioCambioContrasena struct {
	ports.UserRepository
	repo *repoSesiones
}

func (r *repoUsuarioCambioContrasena) ActualizarUsuario(_ context.Context, id uuid.UUID, cambios map[string]any) error {
	r.repo.mu.Lock()
	defer r.repo.mu.Unlock()
	if id != r.repo.usuario.ID {
		return ports.ErrUsuarioNoEncontrado
	}
	if hash, ok := cambios["contrasena_hash"].(string); ok {
		r.repo.usuario.ContrasenaHash = hash
	}
	if requerido, ok := cambios["cambio_contrasena"].(bool); ok {
		r.repo.usuario.CambioContrasena = requerido
	}
	return nil
}

func TestCambiarContrasena_RevocaOtrasSesionesYConservaLaActual(t *testing.T) {
	e := nuevoEntornoAuth(t)
	actual, _ := e.login(t)
	comprometida, _ := e.login(t)
	claimsActual := claimsDe(t, actual.AccessToken)
	claimsComprometida := claimsDe(t, comprometida.AccessToken)
	sidActual := uuid.MustParse(claimsActual.SesionID)
	sidComprometida := uuid.MustParse(claimsComprometida.SesionID)

	// También cubre la clave histórica auth:refresh:<sid>, si existe en un
	// despliegue actualizado desde una versión anterior.
	ctx := context.Background()
	if err := e.kv.Set(ctx, "auth:refresh:"+sidComprometida.String(), "refresh", time.Hour); err != nil {
		t.Fatal(err)
	}
	mensajes, cancelar, err := e.kv.Subscribe(ctx, ports.CanalEventos)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelar()

	auditoria := &auditoriaGrabadora{}
	usuarios := &repoUsuarioCambioContrasena{repo: e.repo}
	servicio := services.NewUserService(usuarios, e.repo, e.kv, auditoria, nil, nil, nil)
	if err := servicio.CambiarContrasena(ctx, e.repo.usuario.ID, sidActual, ports.CambiarContrasenaInput{
		ContrasenaActual: contrasenaPrueba,
		ContrasenaNueva:  "NuevaClave456!",
	}); err != nil {
		t.Fatal(err)
	}

	if err := e.svc.VerificarSesionAccess(ctx, sidActual, claimsActual.ID); err != nil {
		t.Fatalf("la sesión solicitante debe continuar: %v", err)
	}
	if err := e.svc.VerificarSesionAccess(ctx, sidComprometida, claimsComprometida.ID); !errors.Is(err, ports.ErrSesionRevocada) {
		t.Fatalf("el access de otra sesión debe quedar revocado: %v", err)
	}
	if _, err := e.svc.RefrescarToken(ctx, comprometida.RefreshToken); err == nil {
		t.Fatal("el refresh de otra sesión no debe poder renovarse")
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/protegido", middleware.RequireAuth(e.svc), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	respuesta := httptest.NewRecorder()
	peticion := httptest.NewRequest(http.MethodGet, "/protegido", nil)
	peticion.Header.Set("Authorization", "Bearer "+comprometida.AccessToken)
	router.ServeHTTP(respuesta, peticion)
	var cuerpo map[string]string
	if err := json.Unmarshal(respuesta.Body.Bytes(), &cuerpo); err != nil {
		t.Fatal(err)
	}
	if respuesta.Code != http.StatusUnauthorized || cuerpo["errorCode"] != "TOKEN_REVOKED" {
		t.Fatalf("la sesión revocada debe recibir 401 TOKEN_REVOKED: status=%d body=%s", respuesta.Code, respuesta.Body.String())
	}
	if _, err := e.kv.Get(ctx, "auth:session:"+sidComprometida.String()); !errors.Is(err, ports.ErrClaveNoEncontrada) {
		t.Error("auth:session:<sid> de la sesión revocada debe eliminarse")
	}
	if _, err := e.kv.Get(ctx, "auth:refresh:"+sidComprometida.String()); !errors.Is(err, ports.ErrClaveNoEncontrada) {
		t.Error("auth:refresh:<sid> de la sesión revocada debe eliminarse")
	}
	if _, err := e.kv.Get(ctx, "auth:session:"+sidActual.String()); err != nil {
		t.Error("la réplica Redis de la sesión actual debe conservarse")
	}
	if !crypto.VerificarContrasena(e.repo.usuario.ContrasenaHash, "NuevaClave456!") {
		t.Error("la nueva contraseña debe quedar persistida")
	}

	select {
	case recibido := <-mensajes:
		var msg ports.MensajeBus
		if err := json.Unmarshal([]byte(recibido.Contenido), &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Tipo != ports.MensajeSesionesRevocadas || msg.SesionID == nil || *msg.SesionID != sidComprometida {
			t.Fatalf("aviso de revocación selectiva inesperado: %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("faltó publicar el aviso de revocación en tiempo real")
	}

	registro := auditoria.buscar(ports.AccionCambiarContrasena)
	if registro == nil || registro.Accion != "PASSWORD_CHANGED" || registro.Detalles["sesionesRevocadas"] != true || registro.Detalles["sesionConservada"] != sidActual.String() {
		t.Fatalf("auditoría incompleta: %+v", registro)
	}
}
