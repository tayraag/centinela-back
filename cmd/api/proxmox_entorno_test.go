package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
)

type proxmoxArranqueFalso struct {
	nodos       []string
	version     string
	err         error
	consultados int
}

func (p *proxmoxArranqueFalso) ListarNodos(context.Context) ([]string, error) {
	p.consultados++
	return p.nodos, p.err
}

func (p *proxmoxArranqueFalso) VersionPVE(context.Context) (string, error) {
	return p.version, p.err
}

func capturarLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	original := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(original) })
	return &logs
}

// DoD: el nodo de las rutas /nodes/{node}/... sale de PROXMOX_NODE; si falta,
// se descubre en /nodes y, si no se puede, se usa "proxmox" sin frenar el arranque.
func TestResolverNodo(t *testing.T) {
	caido := errors.New("connection refused")
	casos := []struct {
		nombre, configurado string
		proxmox             *proxmoxArranqueFalso
		esperado, enLog     string
		consulta            bool
	}{
		{"PROXMOX_NODE manda", "pve-cluster01", &proxmoxArranqueFalso{nodos: []string{"otro"}}, "pve-cluster01", "(PROXMOX_NODE)", false},
		{"espacios alrededor", "  pve  ", &proxmoxArranqueFalso{}, "pve", "(PROXMOX_NODE)", false},
		{"un solo nodo se descubre", "", &proxmoxArranqueFalso{nodos: []string{"pve-cluster01"}}, "pve-cluster01", "descubierto", true},
		{"varios nodos", "", &proxmoxArranqueFalso{nodos: []string{"pve1", "pve2"}}, "proxmox", "tiene 2 nodos (pve1, pve2)", true},
		{"ningún nodo", "", &proxmoxArranqueFalso{}, "proxmox", "no informó nodos", true},
		{"Proxmox caído", "", &proxmoxArranqueFalso{err: caido}, "proxmox", "connection refused", true},
	}
	for _, caso := range casos {
		logs := capturarLog(t)
		nodo := resolverNodo(context.Background(), caso.configurado, caso.proxmox)
		if nodo != caso.esperado || !strings.Contains(logs.String(), caso.enLog) || (caso.proxmox.consultados > 0) != caso.consulta {
			t.Errorf("%s: nodo=%q consultó=%d log=%q", caso.nombre, nodo, caso.proxmox.consultados, logs.String())
		}
	}
}

func TestVersionObjetivoPVE(t *testing.T) {
	for raw, esperado := range map[string]string{"": "auto", " auto ": "auto", "8.4.21": "8.4.21", "9.2.2": "9.2.2"} {
		if v, err := versionObjetivoPVE(raw); err != nil || v != esperado {
			t.Errorf("%q: %q %v", raw, v, err)
		}
	}
	for _, raw := range []string{"9", "8.4", "AUTO", "latest"} {
		if _, err := versionObjetivoPVE(raw); err == nil || !strings.Contains(err.Error(), "auto, 8.4.21, 9.2.2") {
			t.Errorf("%q debía rechazarse con las opciones válidas: %v", raw, err)
		}
	}
}

// La versión real solo se informa: si no coincide con la fijada se avisa y la
// API sigue funcionando.
func TestVerificarVersionPVE(t *testing.T) {
	caido := errors.New("timeout")
	casos := []struct {
		objetivo string
		proxmox  *proxmoxArranqueFalso
		enLog    string
	}{
		{"auto", &proxmoxArranqueFalso{version: "9.2.2"}, "Proxmox VE 9.2.2 (detectada)"},
		{"8.4.21", &proxmoxArranqueFalso{version: "8.4.21"}, "coincide con PROXMOX_TARGET_PVE_VERSION"},
		{"8.4.21", &proxmoxArranqueFalso{version: "9.2.2"}, "PROXMOX_TARGET_PVE_VERSION=8.4.21 pero Proxmox informa 9.2.2"},
		{"auto", &proxmoxArranqueFalso{err: caido}, "No se pudo detectar"},
		{"9.2.2", &proxmoxArranqueFalso{err: caido}, "se asume 9.2.2"},
	}
	for _, caso := range casos {
		logs := capturarLog(t)
		verificarVersionPVE(context.Background(), caso.objetivo, caso.proxmox)
		if !strings.Contains(logs.String(), caso.enLog) {
			t.Errorf("%s/%+v: log=%q", caso.objetivo, caso.proxmox, logs.String())
		}
	}
}

func TestInformarVmidsProtegidos(t *testing.T) {
	logs := capturarLog(t)
	informarVmidsProtegidos(map[int]bool{})
	informarVmidsProtegidos(map[int]bool{202: true, 200: true, 201: true})
	if !strings.Contains(logs.String(), "Sin VMIDs protegidos configurados") || !strings.Contains(logs.String(), "VMIDs protegidos: [200 201 202]") {
		t.Errorf("log=%q", logs.String())
	}
}
