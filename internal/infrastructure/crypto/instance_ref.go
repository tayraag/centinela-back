package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"math/big"
	"strings"
	"unicode/utf8"
)

// Referencias públicas de instancias: el VMID de Proxmox (100, 101, ...) nunca
// sale de la API; el front recibe y manda un token opaco "inst_" + 22 caracteres.
//
// El token es un bloque AES-256 de 16 bytes (12 bytes en cero + el VMID como
// uint32) escrito en base62. Es determinístico (el mismo VMID da siempre el
// mismo token, así sirve de clave estable en el front) y solo se puede revertir
// con INSTANCE_ID_SALT. Los 12 bytes en cero verifican el token al descifrarlo:
// uno alterado o inventado da otros bytes y se rechaza (acertar uno válido al
// azar tiene probabilidad 2^-96).

// PrefijoReferenciaInstancia identifica el dominio del token.
const PrefijoReferenciaInstancia = "inst_"

const (
	largoMinimoSaltInstancia = 32
	largoCuerpoReferencia    = 22 // 62^22 > 2^128: entra un bloque AES completo
	alfabetoBase62           = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
)

var (
	// ErrSaltInstanciaInvalido: INSTANCE_ID_SALT falta o es demasiado corto.
	ErrSaltInstanciaInvalido = errors.New("INSTANCE_ID_SALT debe tener al menos 32 caracteres")
	// ErrVMIDInvalido: solo se ofuscan enteros positivos de hasta 32 bits.
	ErrVMIDInvalido = errors.New("el VMID debe ser un entero positivo de hasta 32 bits")
	// ErrReferenciaInstanciaInvalida: el token no tiene el formato o no pasa la verificación.
	ErrReferenciaInstanciaInvalida = errors.New("referencia de instancia inválida")
)

// OfuscadorVMID traduce VMIDs a referencias públicas y viceversa.
type OfuscadorVMID struct {
	bloque cipher.Block
}

// NuevoOfuscadorVMID deriva la clave AES-256 de INSTANCE_ID_SALT.
func NuevoOfuscadorVMID(salt string) (*OfuscadorVMID, error) {
	if utf8.RuneCountInString(salt) < largoMinimoSaltInstancia {
		return nil, ErrSaltInstanciaInvalido
	}
	// Separación de dominio: la clave no coincide con otros usos del mismo secreto.
	clave := sha256.Sum256([]byte("centinela:instance-ref:v1:" + salt))
	bloque, err := aes.NewCipher(clave[:])
	if err != nil {
		return nil, err
	}
	return &OfuscadorVMID{bloque: bloque}, nil
}

// OfuscarVMID traduce un VMID a su referencia pública (ej. 101 -> "inst_3kX...").
func (o *OfuscadorVMID) OfuscarVMID(vmid int) (string, error) {
	if vmid <= 0 || vmid > math.MaxUint32 {
		return "", ErrVMIDInvalido
	}
	var plano, cifrado [aes.BlockSize]byte
	binary.BigEndian.PutUint32(plano[12:], uint32(vmid))
	o.bloque.Encrypt(cifrado[:], plano[:])

	texto := new(big.Int).SetBytes(cifrado[:]).Text(62)
	return PrefijoReferenciaInstancia + strings.Repeat("0", largoCuerpoReferencia-len(texto)) + texto, nil
}

// DesofuscarVMID devuelve el VMID de una referencia pública. Cualquier token
// alterado, truncado o inventado devuelve ErrReferenciaInstanciaInvalida.
func (o *OfuscadorVMID) DesofuscarVMID(ref string) (int, error) {
	cuerpo, ok := strings.CutPrefix(ref, PrefijoReferenciaInstancia)
	if !ok || len(cuerpo) != largoCuerpoReferencia {
		return 0, ErrReferenciaInstanciaInvalida
	}
	// big.Int.SetString aceptaría un signo inicial: se exige solo base62.
	for i := 0; i < len(cuerpo); i++ {
		if !strings.ContainsRune(alfabetoBase62, rune(cuerpo[i])) {
			return 0, ErrReferenciaInstanciaInvalida
		}
	}
	n, ok := new(big.Int).SetString(cuerpo, 62)
	if !ok || n.BitLen() > aes.BlockSize*8 {
		return 0, ErrReferenciaInstanciaInvalida
	}
	var cifrado, plano [aes.BlockSize]byte
	n.FillBytes(cifrado[:])
	o.bloque.Decrypt(plano[:], cifrado[:])

	for _, b := range plano[:12] {
		if b != 0 {
			return 0, ErrReferenciaInstanciaInvalida
		}
	}
	vmid := int(binary.BigEndian.Uint32(plano[12:]))
	if vmid == 0 {
		return 0, ErrReferenciaInstanciaInvalida
	}
	return vmid, nil
}
