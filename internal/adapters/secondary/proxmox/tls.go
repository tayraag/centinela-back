package proxmox

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
)

// BuildTLSConfig arma el *tls.Config del cliente HTTP hacia Proxmox según las
// variables de entorno disponibles, en este orden de prioridad:
//
//  1. PROXMOX_TLS_FINGERPRINT seteada: se pinnea el certificado por su huella
//     SHA-256 (acepta el formato con ":" que muestra la UI de Proxmox o sin
//     separadores). Es el modo que se va a usar en el entorno real.
//  2. PROXMOX_TLS_INSECURE=true: se saltea toda verificación (InsecureSkipVerify).
//     Solo pensado para mientras no tengamos el fingerprint real a mano.
//  3. Ninguna de las dos: verificación TLS estándar (fallará contra un
//     certificado autofirmado, a propósito — no hay bypass silencioso).
func BuildTLSConfig() *tls.Config {
	if fp := normalizeFingerprint(os.Getenv("PROXMOX_TLS_FINGERPRINT")); fp != "" {
		return &tls.Config{
			// La verificación estándar de la cadena queda deshabilitada porque
			// la reemplazamos por completo con VerifyPeerCertificate: pinneamos
			// el certificado exacto en vez de confiar en una CA.
			InsecureSkipVerify: true,
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return fmt.Errorf("proxmox: el servidor no presentó ningún certificado")
				}
				sum := sha256.Sum256(rawCerts[0])
				got := fmt.Sprintf("%x", sum)
				if got != fp {
					return fmt.Errorf("proxmox: el fingerprint del certificado no coincide (esperado %s, recibido %s)", fp, got)
				}
				return nil
			},
		}
	}

	if strings.EqualFold(os.Getenv("PROXMOX_TLS_INSECURE"), "true") {
		return &tls.Config{InsecureSkipVerify: true}
	}

	return nil // nil == tls.Config por defecto (verificación estándar) para el http.Transport
}

// normalizeFingerprint deja el fingerprint en minúsculas y sin separadores,
// para comparar directamente contra el hex que produce sha256.Sum256.
func normalizeFingerprint(fp string) string {
	fp = strings.ToLower(strings.TrimSpace(fp))
	return strings.ReplaceAll(fp, ":", "")
}
