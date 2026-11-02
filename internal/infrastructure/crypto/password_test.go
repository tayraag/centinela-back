package crypto_test

import (
	"strings"
	"testing"

	"el-centinela/internal/infrastructure/crypto"
)

// NIST SP 800-63B: cualquier signo de puntuación ASCII cuenta como especial,
// incluidos los que generan 1Password, Bitwarden o KeePass.
func TestValidarComplejidad_CadaCaracterEspecial(t *testing.T) {
	for _, c := range crypto.CaracteresEspeciales {
		clave := "Clave123" + string(c)
		if err := crypto.ValidarComplejidadContrasena(clave); err != nil {
			t.Errorf("%q: %v", clave, err)
		}
	}
	if len(crypto.CaracteresEspeciales) != 31 {
		t.Errorf("Se esperaban los 31 símbolos acordados con el front, hay %d: %s", len(crypto.CaracteresEspeciales), crypto.CaracteresEspeciales)
	}
}

func TestValidarComplejidad_GestoresDeClavesYFrases(t *testing.T) {
	aceptadas := []string{
		"Xk9.vTqRm2wz",               // solo punto
		"Ab3,cd5,ef7,",               // solo comas
		"Ruta/Segura9",               // barra
		"Que2tal?Bien",               // signo de pregunta
		"Tilde~9abcde",               // tilde
		"Pa(re)nt3sis",               // paréntesis
		"Dos:puntos;8",               // dos puntos y punto y coma
		`Cita"Comilla'1`,             // comillas
		`Barra\Invertida7`,           // barra invertida
		"Llaves{1}[2]<3>|",           // llaves, corchetes, mayor/menor y barra vertical
		"Xq7.R,v/M?k~2;p:T",          // contraseña típica de un gestor
		"correcto caballo Bateria 9", // frase de paso: el espacio cuenta
		"Clave 1234567",              // 13 caracteres: el espacio cuenta
	}
	for _, clave := range aceptadas {
		if err := crypto.ValidarComplejidadContrasena(clave); err != nil {
			t.Errorf("%q debía aceptarse: %v", clave, err)
		}
	}

	rechazadas := map[string]string{
		"Clave 12345":       "especial", // 11 caracteres: el espacio no alcanza
		"Clave 123456":      "especial", // 12 caracteres: tampoco
		"Clave1234":         "especial",
		"Clave1234\tabcdef": "especial", // el tabulador no es un símbolo
		"clave.1234":        "mayúscula",
		"Clave.abcd":        "número",
		"Cl.1":              "8 caracteres",
	}
	for clave, motivo := range rechazadas {
		err := crypto.ValidarComplejidadContrasena(clave)
		if err == nil || !strings.Contains(err.Error(), motivo) {
			t.Errorf("%q debía rechazarse por %q: %v", clave, motivo, err)
		}
	}
}

// El mensaje lista los símbolos válidos para que el usuario sepa qué usar.
func TestValidarComplejidad_MensajeListaLosSimbolos(t *testing.T) {
	err := crypto.ValidarComplejidadContrasena("Clave1234")
	if err == nil || !strings.Contains(err.Error(), crypto.CaracteresEspeciales) || !strings.Contains(err.Error(), "espacio") {
		t.Errorf("mensaje: %v", err)
	}
}
