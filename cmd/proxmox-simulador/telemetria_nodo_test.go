package main

import "testing"

func TestRRDDataNodoExponeCamposRealesDeProxmox(t *testing.T) {
	e := nuevoEntorno(t, "")
	estado, data, _ := e.pedir("GET", "/nodes/proxmox/rrddata?timeframe=hour", nil)
	if estado != 200 {
		t.Fatalf("rrddata del nodo devolvió %d", estado)
	}
	puntos, ok := data.([]any)
	if !ok || len(puntos) != 60 {
		t.Fatalf("se esperaban 60 puntos del nodo: %v", data)
	}
	ultimo, ok := puntos[len(puntos)-1].(map[string]any)
	if !ok {
		t.Fatalf("formato del punto inesperado: %T", puntos[len(puntos)-1])
	}
	for _, campo := range []string{"memused", "memtotal", "rootused", "roottotal"} {
		if _, ok := ultimo[campo]; !ok {
			t.Errorf("el RRD del nodo no expone %q: %+v", campo, ultimo)
		}
	}
	for _, campo := range []string{"mem", "maxmem", "disk", "maxdisk", "memhost"} {
		if _, ok := ultimo[campo]; ok {
			t.Errorf("el RRD del nodo conserva el campo obsoleto %q", campo)
		}
	}
}
