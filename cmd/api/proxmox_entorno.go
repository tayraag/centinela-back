package main

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"time"
)

// nodoPorDefecto es el nodo que se usa si PROXMOX_NODE no está y no se puede
// descubrir uno solo en el cluster.
const nodoPorDefecto = "proxmox"

// timeoutDescubrimiento acota las consultas a Proxmox del arranque, para que un
// hipervisor caído no demore el inicio de la API.
const timeoutDescubrimiento = 5 * time.Second

// versionesPVE son los valores admitidos de PROXMOX_TARGET_PVE_VERSION.
var versionesPVE = []string{"auto", "8.4.21", "9.2.2"}

type consultaNodos interface {
	ListarNodos(ctx context.Context) ([]string, error)
}

type consultaVersion interface {
	VersionPVE(ctx context.Context) (string, error)
}

// versionObjetivoPVE valida PROXMOX_TARGET_PVE_VERSION. Vacía = "auto". Un
// valor fuera de los admitidos es un error: la API no arranca con un typo.
func versionObjetivoPVE(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "auto", nil
	}
	if !slices.Contains(versionesPVE, v) {
		return "", fmt.Errorf("PROXMOX_TARGET_PVE_VERSION=%q no es válida (opciones: %s)", v, strings.Join(versionesPVE, ", "))
	}
	return v, nil
}

// resolverNodo devuelve el nodo de Proxmox a usar en las rutas
// /nodes/{node}/...: PROXMOX_NODE si está; si no, el único nodo que informa
// GET /nodes; y si Proxmox no responde o hay varios nodos, nodoPorDefecto con
// un aviso. Nunca impide el arranque.
func resolverNodo(ctx context.Context, configurado string, proxmox consultaNodos) string {
	if nodo := strings.TrimSpace(configurado); nodo != "" {
		log.Printf("🖥️  Nodo de Proxmox: %s (PROXMOX_NODE)", nodo)
		return nodo
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutDescubrimiento)
	defer cancel()
	nodos, err := proxmox.ListarNodos(ctx)
	switch {
	case err != nil:
		log.Printf("⚠️  PROXMOX_NODE vacía y no se pudo consultar los nodos a Proxmox (%v): se usa %q", err, nodoPorDefecto)
	case len(nodos) == 1:
		log.Printf("🖥️  Nodo de Proxmox: %s (descubierto, PROXMOX_NODE vacía)", nodos[0])
		return nodos[0]
	case len(nodos) == 0:
		log.Printf("⚠️  PROXMOX_NODE vacía y Proxmox no informó nodos: se usa %q", nodoPorDefecto)
	default:
		log.Printf("⚠️  PROXMOX_NODE vacía y el cluster tiene %d nodos (%s): se usa %q; definí PROXMOX_NODE",
			len(nodos), strings.Join(nodos, ", "), nodoPorDefecto)
	}
	return nodoPorDefecto
}

// verificarVersionPVE consulta la versión de Proxmox y la deja en el log. Con
// una versión fijada en PROXMOX_TARGET_PVE_VERSION avisa si no coincide, pero
// la API sigue funcionando.
func verificarVersionPVE(ctx context.Context, objetivo string, proxmox consultaVersion) {
	ctx, cancel := context.WithTimeout(ctx, timeoutDescubrimiento)
	defer cancel()
	real, err := proxmox.VersionPVE(ctx)
	switch {
	case err != nil && objetivo == "auto":
		log.Printf("⚠️  No se pudo detectar la versión de Proxmox VE: %v", err)
	case err != nil:
		log.Printf("⚠️  No se pudo verificar la versión de Proxmox VE (%v): se asume %s (PROXMOX_TARGET_PVE_VERSION)", err, objetivo)
	case objetivo == "auto":
		log.Printf("🖥️  Proxmox VE %s (detectada)", real)
	case real != objetivo:
		log.Printf("⚠️  PROXMOX_TARGET_PVE_VERSION=%s pero Proxmox informa %s: revisar la compatibilidad", objetivo, real)
	default:
		log.Printf("🖥️  Proxmox VE %s (coincide con PROXMOX_TARGET_PVE_VERSION)", real)
	}
}

// informarVmidsProtegidos deja en el log qué instancias responden 403
// INSTANCE_PROTECTED (PROXMOX_PROTECTED_VMIDS).
func informarVmidsProtegidos(protegidos map[int]bool) {
	if len(protegidos) == 0 {
		log.Println("ℹ️  Sin VMIDs protegidos configurados (PROXMOX_PROTECTED_VMIDS vacía): ninguna instancia se bloquea con INSTANCE_PROTECTED")
		return
	}
	vmids := make([]int, 0, len(protegidos))
	for vmid := range protegidos {
		vmids = append(vmids, vmid)
	}
	sort.Ints(vmids)
	log.Printf("🛡️  VMIDs protegidos: %v (PROXMOX_PROTECTED_VMIDS)", vmids)
}
