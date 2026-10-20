package topology

import (
	"strings"
	"testing"
)

func catalogoPrueba(t *testing.T) *Catalogo {
	t.Helper()
	c, err := NuevoCatalogo(map[string]string{"proxmox": AliasNodoPorDefecto}, AliasStoragePorDefecto, "secreto-de-prueba")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMaskYResolve_Catalogo(t *testing.T) {
	c := catalogoPrueba(t)
	if got := c.MaskNode("proxmox"); got != "nodo-principal" {
		t.Errorf("MaskNode(proxmox) = %q", got)
	}
	if got := c.ResolveNode("nodo-principal"); got != "proxmox" {
		t.Errorf("ResolveNode(nodo-principal) = %q", got)
	}
	for real, alias := range map[string]string{"local-lvm": "Almacenamiento Local Rápido", "local": "Almacenamiento Estándar"} {
		if got := c.MaskStorage(real); got != alias {
			t.Errorf("MaskStorage(%s) = %q", real, got)
		}
		if got := c.ResolveStorage(alias); got != real {
			t.Errorf("ResolveStorage(%s) = %q", alias, got)
		}
	}
	// Un ADMIN manda el nombre real: se usa tal cual.
	if c.ResolveStorage("local-lvm") != "local-lvm" || c.ResolveNode("proxmox") != "proxmox" {
		t.Error("Un nombre real debe resolverse a sí mismo")
	}
	if c.MaskNode("") != "" || c.MaskStorage("") != "" {
		t.Error("Vacío queda vacío")
	}
}

// Lo que no está en el catálogo recibe un alias neutro, estable, que no
// contiene el nombre real y que se puede resolver de vuelta.
func TestMask_AliasGenericoSinFiltrarElNombre(t *testing.T) {
	c := catalogoPrueba(t)
	alias := c.MaskStorage("shared-nfs")
	if !strings.HasPrefix(alias, "almacenamiento-") || strings.Contains(alias, "nfs") || !esGenerico(alias, "almacenamiento-") {
		t.Fatalf("alias genérico inesperado: %q", alias)
	}
	if c.MaskStorage("shared-nfs") != alias {
		t.Error("El alias genérico debe ser estable")
	}
	if c.ResolveStorage(alias) != "shared-nfs" {
		t.Errorf("El alias genérico debe resolverse: %q", c.ResolveStorage(alias))
	}
	nodo := c.MaskNode("pve-02")
	if !strings.HasPrefix(nodo, "nodo-") || strings.Contains(nodo, "pve") || c.ResolveNode(nodo) != "pve-02" {
		t.Errorf("alias genérico de nodo: %q", nodo)
	}
	otro, _ := NuevoCatalogo(nil, nil, "otro-secreto")
	if otro.MaskStorage("shared-nfs") == alias {
		t.Error("Con otro secreto el alias genérico cambia (no se puede adivinar)")
	}
}

func TestNuevoCatalogo_RechazaCatalogosAmbiguos(t *testing.T) {
	casos := map[string]map[string]string{
		"alias repetido":              {"local": "Rápido", "local-lvm": "Rápido"},
		"alias que es otro real":      {"local": "local-lvm", "local-lvm": "Rápido"},
		"alias con forma de genérico": {"local": "almacenamiento-0a1b2c3d"},
		"vacío":                       {"local": " "},
	}
	for nombre, storages := range casos {
		if _, err := NuevoCatalogo(nil, storages, "s"); err == nil {
			t.Errorf("%s: se esperaba error", nombre)
		}
	}
	if _, err := NuevoCatalogo(nil, nil, ""); err == nil {
		t.Error("Sin secreto debe fallar")
	}
}

func TestMaskTexto_SoloNombresCompletos(t *testing.T) {
	c := catalogoPrueba(t)
	casos := map[string]string{
		"storage 'local-lvm' does not exist":            "storage 'Almacenamiento Local Rápido' does not exist",
		"storage 'local' does not exist":                "storage 'Almacenamiento Estándar' does not exist",
		"no space left on local-lvm (node proxmox)":     "no space left on Almacenamiento Local Rápido (node nodo-principal)",
		"local local-lvm local":                         "Almacenamiento Estándar Almacenamiento Local Rápido Almacenamiento Estándar",
		"localhost:8006 y local.conf no son el storage": "localhost:8006 y local.conf no son el storage",
		"Proxmox VE no respondió":                       "Proxmox VE no respondió",
		"error en shared-nfs":                           "error en " + c.MaskStorage("shared-nfs"),
	}
	for texto, esperado := range casos {
		if got := c.MaskTexto(texto, nil, []string{"shared-nfs"}); got != esperado {
			t.Errorf("MaskTexto(%q)\n  = %q\n  se esperaba %q", texto, got, esperado)
		}
	}
}

func TestParseAlias(t *testing.T) {
	alias, err := ParseAlias(" local-lvm = Almacenamiento Local Rápido , local=Almacenamiento Estándar ")
	if err != nil || len(alias) != 2 || alias["local-lvm"] != "Almacenamiento Local Rápido" || alias["local"] != "Almacenamiento Estándar" {
		t.Errorf("%v %v", alias, err)
	}
	if alias, err := ParseAlias("  "); err != nil || alias != nil {
		t.Errorf("vacío: %v %v", alias, err)
	}
	for _, malo := range []string{"local", "local=", "=alias", "a=b,a=c"} {
		if _, err := ParseAlias(malo); err == nil {
			t.Errorf("%q: se esperaba error", malo)
		}
	}
}
