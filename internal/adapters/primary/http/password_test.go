package http

import (
	"el-centinela/internal/core/ports"
	"el-centinela/internal/infrastructure/crypto"
	"fmt"
	"github.com/gin-gonic/gin/binding"
	"golang.org/x/crypto/bcrypt"
	"strings"
	"testing"
)

func TestContrasenasHasta128Caracteres(t *testing.T) {
	for _, n := range []int{8, 16, 32, 64, 72, 73, 128} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			raw := "A1!" + strings.Repeat("a", n-3)
			if err := crypto.ValidarComplejidadContrasena(raw); err != nil {
				t.Fatal(err)
			}
			hash, err := crypto.HashContrasena(raw)
			if err != nil {
				t.Fatal(err)
			}
			if !crypto.VerificarContrasena(hash, raw) {
				t.Fatal("no verifica la contraseña completa")
			}
			distinta := raw[:len(raw)-1] + "b"
			if crypto.VerificarContrasena(hash, distinta) {
				t.Fatal("acepta una contraseña distinta, posiblemente truncada")
			}
		})
	}
}

func TestContrasenaUnicodeYCota(t *testing.T) {
	raw := "A1!" + strings.Repeat("ñ", 125)
	if err := crypto.ValidarComplejidadContrasena(raw); err != nil {
		t.Fatal(err)
	}
	hash, err := crypto.HashContrasena(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !crypto.VerificarContrasena(hash, raw) {
		t.Fatal("no verifica 128 caracteres Unicode")
	}
	for _, larga := range []string{raw + "a", "A1!" + strings.Repeat("a", 126)} {
		if crypto.ValidarComplejidadContrasena(larga) == nil {
			t.Fatal("acepta más de 128 caracteres")
		}
		if _, err := crypto.HashContrasena(larga); err == nil {
			t.Fatal("hashea más de 128 caracteres")
		}
		if crypto.VerificarContrasena(hash, larga) {
			t.Fatal("verifica más de 128 caracteres")
		}
	}
}

func TestCompatibilidadBcryptYComplejidad(t *testing.T) {
	raw := "Clave123!"
	hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if !crypto.VerificarContrasena(string(hash), raw) {
		t.Fatal("no verifica hash existente")
	}
	for _, raw := range []string{"A1!aaaa", "minuscula123!", "SinNumero!", "SinSimbolo123"} {
		if crypto.ValidarComplejidadContrasena(raw) == nil {
			t.Fatalf("acepta complejidad insuficiente: %q", raw)
		}
	}
	prefix := strings.Repeat("a", 72)
	hash, err = bcrypt.GenerateFromPassword([]byte(prefix), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if crypto.VerificarContrasena(string(hash), prefix+"extra") {
		t.Fatal("acepta sufijo ignorado por bcrypt")
	}
}

func TestDTOsLongitudContrasena(t *testing.T) {
	for _, n := range []int{7, 8, 16, 32, 64, 128, 129} {
		raw := "A1!" + strings.Repeat("a", n-3)
		for _, dto := range []any{
			LoginRequest{Email: "user@example.com", Contrasena: raw},
			ports.CambiarContrasenaInput{ContrasenaActual: "Clave123!", ContrasenaNueva: raw},
			confirmarRecuperacionRequest{Email: "user@example.com", Codigo: "123456", NuevaContrasena: raw},
		} {
			err := binding.Validator.ValidateStruct(dto)
			if (err == nil) != (n >= 8 && n <= 128) {
				t.Errorf("%T longitud %d: %v", dto, n, err)
			}
		}
	}
}
