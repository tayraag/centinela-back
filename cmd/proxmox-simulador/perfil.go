package main

import (
	"fmt"
	"slices"
	"strings"
)

// PerfilVersion es la versión de Proxmox VE que imita el simulador
// (PROXMOX_SIM_TARGET_VERSION). Reúne las firmas que cambian entre versiones:
// lo que responde GET /version y la firma y el kernel de GET /nodes/{node}/status.
// Las variaciones de una versión (formatos de discos, storages...) se acoplan
// leyendo s.obtenerPerfilVersion().Version en el handler correspondiente.
type PerfilVersion struct {
	Version string // identificador del perfil: "9.2.2" o "8.4.21"

	// GET /version
	VersionAPI string // campo "version"
	ReleaseAPI string // campo "release"
	RepoID     string // campo "repoid"

	// GET /nodes/{node}/status
	PVEManager    string // "pveversion"
	KernelRelease string // "current-kernel.release"
	KernelBuild   string // "current-kernel.version"
}

// Kversion es la línea completa del kernel ("kversion" en el estado del nodo).
func (p PerfilVersion) Kversion() string {
	return "Linux " + p.KernelRelease + " " + p.KernelBuild
}

// VersionSimPorDefecto es el perfil sin PROXMOX_SIM_TARGET_VERSION: el del
// servidor físico de destino.
const VersionSimPorDefecto = "8.4.21"

var perfilesVersion = map[string]PerfilVersion{
	// Contrastado con las capturas de Proxmox VE 9.2.2.
	"9.2.2": {
		Version:    "9.2.2",
		VersionAPI: "9.2.2", ReleaseAPI: "9.2", RepoID: "b9984c6d90a4bd80",
		PVEManager:    "pve-manager/9.2-2/b9984c6d90a4bd80",
		KernelRelease: "7.0.2-6-pve",
		KernelBuild:   "#1 SMP PREEMPT_DYNAMIC PMX 7.0.2-6 (2026-05-20T08:55Z)",
	},
	// Servidor de destino, PVE 8.4.21 con kernel 6.8.
	"8.4.21": {
		Version:    "8.4.21",
		VersionAPI: "8.4", ReleaseAPI: "21", RepoID: "12345678",
		PVEManager:    "pve-manager/8.4-21/12345678",
		KernelRelease: "6.8.8-2-pve",
		KernelBuild:   "#1 SMP PREEMPT_DYNAMIC PMX 6.8.8-2",
	},
}

// PerfilDeVersion devuelve el perfil de PROXMOX_SIM_TARGET_VERSION. Vacía =
// VersionSimPorDefecto; un valor no soportado es un error.
func PerfilDeVersion(raw string) (PerfilVersion, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		v = VersionSimPorDefecto
	}
	perfil, ok := perfilesVersion[v]
	if !ok {
		return PerfilVersion{}, fmt.Errorf("PROXMOX_SIM_TARGET_VERSION=%q no es válida (opciones: %s)", raw, strings.Join(versionesSimSoportadas(), ", "))
	}
	return perfil, nil
}

func versionesSimSoportadas() []string {
	versiones := make([]string, 0, len(perfilesVersion))
	for v := range perfilesVersion {
		versiones = append(versiones, v)
	}
	slices.Sort(versiones)
	return versiones
}

// obtenerPerfilVersion es el punto de extensión para las respuestas que
// cambian según la versión de Proxmox VE que se imita.
func (s *Simulador) obtenerPerfilVersion() PerfilVersion {
	return s.perfil
}
