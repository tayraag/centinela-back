package crypto

import (
	"errors"
	"math"
	"regexp"
	"strings"
	"testing"
)

const saltPrueba = "salt-de-prueba-de-al-menos-32-caracteres!"

func ofuscadorPrueba(t *testing.T, salt string) *OfuscadorVMID {
	t.Helper()
	o, err := NuevoOfuscadorVMID(salt)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestNuevoOfuscadorVMID_ExigeSaltDe32Caracteres(t *testing.T) {
	for _, salt := range []string{"", "corto", strings.Repeat("x", 31)} {
		if _, err := NuevoOfuscadorVMID(salt); !errors.Is(err, ErrSaltInstanciaInvalido) {
			t.Errorf("salt de %d caracteres: se esperaba ErrSaltInstanciaInvalido, vino %v", len(salt), err)
		}
	}
	if _, err := NuevoOfuscadorVMID(strings.Repeat("x", 32)); err != nil {
		t.Errorf("32 caracteres alcanzan: %v", err)
	}
}

func TestOfuscarVMID_IdaYVuelta(t *testing.T) {
	o := ofuscadorPrueba(t, saltPrueba)
	formato := regexp.MustCompile(`^inst_[0-9A-Za-z]{22}$`)
	vistos := map[string]int{}
	for _, vmid := range []int{1, 100, 101, 102, 110, 201, 9003, 999999999, math.MaxUint32} {
		ref, err := o.OfuscarVMID(vmid)
		if err != nil {
			t.Fatalf("%d: %v", vmid, err)
		}
		if !formato.MatchString(ref) {
			t.Errorf("%d: formato inesperado %q", vmid, ref)
		}
		if otro, ok := vistos[ref]; ok {
			t.Errorf("%d y %d comparten referencia %q", vmid, otro, ref)
		}
		vistos[ref] = vmid
		if de, err := o.DesofuscarVMID(ref); err != nil || de != vmid {
			t.Errorf("%q debía volver a %d, vino %d (%v)", ref, vmid, de, err)
		}
		if otra, _ := o.OfuscarVMID(vmid); otra != ref {
			t.Errorf("%d: no es determinístico (%q y %q)", vmid, ref, otra)
		}
	}
}

// VMIDs consecutivos no dan referencias parecidas: no se puede deducir el
// orden ni la cantidad de instancias comparando tokens.
func TestOfuscarVMID_ConsecutivosNoSeCorrelacionan(t *testing.T) {
	o := ofuscadorPrueba(t, saltPrueba)
	a, _ := o.OfuscarVMID(100)
	b, _ := o.OfuscarVMID(101)
	iguales := 0
	for i := len(PrefijoReferenciaInstancia); i < len(a); i++ {
		if a[i] == b[i] {
			iguales++
		}
	}
	if iguales > 6 {
		t.Errorf("100 y 101 comparten %d de 22 caracteres: %q %q", iguales, a, b)
	}
}

func TestOfuscarVMID_RechazaFueraDeRango(t *testing.T) {
	o := ofuscadorPrueba(t, saltPrueba)
	for _, vmid := range []int{0, -1, -100, math.MaxUint32 + 1} {
		if _, err := o.OfuscarVMID(vmid); !errors.Is(err, ErrVMIDInvalido) {
			t.Errorf("%d: se esperaba ErrVMIDInvalido, vino %v", vmid, err)
		}
	}
}

func TestDesofuscarVMID_RechazaTokensInvalidos(t *testing.T) {
	o := ofuscadorPrueba(t, saltPrueba)
	ref, _ := o.OfuscarVMID(101)
	cuerpo := strings.TrimPrefix(ref, PrefijoReferenciaInstancia)

	// Cambiar un solo carácter del cuerpo.
	alterado := []byte(ref)
	if alterado[10] == 'a' {
		alterado[10] = 'b'
	} else {
		alterado[10] = 'a'
	}

	casos := map[string]string{
		"vacío":                 "",
		"VMID numérico":         "101",
		"sin prefijo":           cuerpo,
		"otro prefijo":          "vm_" + cuerpo,
		"truncado":              ref[:len(ref)-1],
		"con un carácter extra": ref + "0",
		"alterado":              string(alterado),
		"signo":                 PrefijoReferenciaInstancia + "+" + cuerpo[1:],
		"guion bajo":            PrefijoReferenciaInstancia + "_" + cuerpo[1:],
		"fuera de 128 bits":     PrefijoReferenciaInstancia + strings.Repeat("Z", largoCuerpoReferencia),
		"todo ceros":            PrefijoReferenciaInstancia + strings.Repeat("0", largoCuerpoReferencia),
		"ejemplo de la tarea":   "inst_k8Pq2v",
	}
	for nombre, token := range casos {
		if vmid, err := o.DesofuscarVMID(token); !errors.Is(err, ErrReferenciaInstanciaInvalida) {
			t.Errorf("%s (%q): se esperaba ErrReferenciaInstanciaInvalida, vino %d %v", nombre, token, vmid, err)
		}
	}
}

// Con otro salt, los tokens cambian y los de un servidor no sirven en otro.
func TestDesofuscarVMID_OtroSaltNoLoAcepta(t *testing.T) {
	a := ofuscadorPrueba(t, saltPrueba)
	b := ofuscadorPrueba(t, saltPrueba+"-otro")
	refA, _ := a.OfuscarVMID(101)
	refB, _ := b.OfuscarVMID(101)
	if refA == refB {
		t.Fatalf("Dos salts distintos dieron la misma referencia %q", refA)
	}
	if _, err := b.DesofuscarVMID(refA); !errors.Is(err, ErrReferenciaInstanciaInvalida) {
		t.Errorf("Un token de otro salt debe rechazarse: %v", err)
	}
}
