package http

import (
	"context"
	"el-centinela/internal/core/ports"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"net/http/httptest"
	"strings"
	"testing"
)

type perfilEmailServiceTest struct {
	ports.UserService
	err error
}

func (s perfilEmailServiceTest) ActualizarPerfil(context.Context, uuid.UUID, ports.ActualizarPerfilInput) (*ports.UsuarioResumenDTO, error) {
	return nil, s.err
}
func TestPerfilEmailErroresHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{ports.ErrPerfilInvalido, 400, "INVALID_REQUEST"},
		{ports.ErrPerfilAuth, 401, "AUTH_FAILED"},
		{ports.ErrPerfilOTP, 401, "INVALID_OTP_CODE"},
		{ports.ErrEmailYaRegistrado, 409, "USER_EMAIL_ALREADY_EXISTS"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			r := gin.New()
			h := NewAccountHandler(perfilEmailServiceTest{err: tc.err}, RefsPrueba)
			r.PUT("/profile", h.ActualizarPerfil)
			w := httptest.NewRecorder()
			req := httptest.NewRequest("PUT", "/profile", strings.NewReader(`{"emailUsuario":"nuevo@example.com"}`))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}
