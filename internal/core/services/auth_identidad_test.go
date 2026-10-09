package services_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"el-centinela/internal/core/ports"
	"el-centinela/internal/infrastructure/crypto"

	"github.com/pquerna/otp/totp"
)

// Una cuenta suspendida solo se informa como tal con la contraseña correcta:
// con una incorrecta la respuesta es la misma que para cualquier otra cuenta.
func TestLogin_CuentaSuspendida(t *testing.T) {
	e := nuevoEntornoAuth(t)
	e.repo.usuario.Activo = false

	_, err := e.svc.Login(context.Background(), emailPrueba, "ClaveIncorrecta1!")
	if err == nil || errors.Is(err, ports.ErrCuentaSuspendida) {
		t.Errorf("Con clave incorrecta no debe revelarse la suspensión: %v", err)
	}
	if _, err := e.svc.Login(context.Background(), emailPrueba, contrasenaPrueba); !errors.Is(err, ports.ErrCuentaSuspendida) {
		t.Errorf("Con clave correcta debe informarse ErrCuentaSuspendida: %v", err)
	}
}

// El token temporal pre-2FA vive 5 minutos: si la cuenta se elimina o se
// suspende en ese lapso, el segundo paso no debe abrir una sesión.
func TestVerificarTotp_RechazaCuentaEliminadaOSuspendidaDuranteElLogin(t *testing.T) {
	casos := []struct {
		nombre     string
		aplicar    func(e *entornoAuth)
		suspension bool
	}{
		{"eliminada", func(e *entornoAuth) {
			ahora := time.Now().UTC()
			e.repo.usuario.Activo, e.repo.usuario.EliminadoEn = false, &ahora
		}, false},
		{"suspendida", func(e *entornoAuth) { e.repo.usuario.Activo = false }, true},
	}
	for _, c := range casos {
		e := nuevoEntornoAuth(t)
		res, err := e.svc.Login(context.Background(), emailPrueba, contrasenaPrueba)
		if err != nil {
			t.Fatal(err)
		}
		pre, _ := crypto.VerificarToken(res.JWTTemporal, "secreto-jwt-de-prueba-con-mas-de-32-caracteres")
		c.aplicar(e)

		codigo, _ := totp.GenerateCode(e.secretoTOTP, time.Now())
		_, err = e.svc.VerificarTotp(context.Background(), pre.ID, codigo)
		if err == nil {
			t.Errorf("%s: VerificarTotp no debe abrir sesión", c.nombre)
		}
		if errors.Is(err, ports.ErrCuentaSuspendida) != c.suspension {
			t.Errorf("%s: error inesperado %v", c.nombre, err)
		}
		if e.repo.inserts != 0 {
			t.Errorf("%s: no debe crearse ninguna sesión (%d)", c.nombre, e.repo.inserts)
		}
	}
}

// Tampoco se puede vincular el 2FA de una cuenta eliminada.
func TestObtenerQR_RechazaCuentaEliminada(t *testing.T) {
	e := nuevoEntornoAuth(t)
	e.repo.usuario.TotpVinculado, e.repo.usuario.SecretoTotpCifrado = false, ""
	res, err := e.svc.Login(context.Background(), emailPrueba, contrasenaPrueba)
	if err != nil {
		t.Fatal(err)
	}
	pre, _ := crypto.VerificarToken(res.JWTTemporal, "secreto-jwt-de-prueba-con-mas-de-32-caracteres")
	ahora := time.Now().UTC()
	e.repo.usuario.Activo, e.repo.usuario.EliminadoEn = false, &ahora
	if _, err := e.svc.ObtenerQRParaVinculacion(context.Background(), pre.ID); err == nil {
		t.Error("No debe entregarse el QR de una cuenta eliminada")
	}
}
