// Package topology reemplaza los nombres reales de la infraestructura (nodos
// de Proxmox y pools de almacenamiento) por alias neutros, para que un usuario
// que no es ADMIN no pueda mapear el hipervisor (hostnames, tipos de storage).
package topology

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Prefijos de los alias genéricos para los nombres que no están en el catálogo.
const (
	prefijoNodo    = "nodo-"
	prefijoStorage = "almacenamiento-"
)

// AliasStoragePorDefecto son los alias de storage cuando no se configura
// TOPOLOGY_STORAGE_ALIASES.
var AliasStoragePorDefecto = map[string]string{
	"local-lvm": "Almacenamiento Local Rápido",
	"local":     "Almacenamiento Estándar",
}

// AliasNodoPorDefecto es el alias del nodo configurado (PROXMOX_NODE) cuando
// no se configura TOPOLOGY_NODE_ALIASES.
const AliasNodoPorDefecto = "nodo-principal"

// Catalogo traduce nombres reales a alias públicos y viceversa.
type Catalogo struct {
	nodos, storages               traduccion
	secreto                       []byte
	muGenericos                   sync.RWMutex
	nodosGenericos, storagesGener map[string]string // alias genérico -> real
}

type traduccion struct {
	alias map[string]string // real -> alias
	real  map[string]string // alias -> real
}

// NuevoCatalogo arma el catálogo. nodos y storages van de nombre real a alias.
// secreto deriva los alias genéricos de los nombres que no están en el
// catálogo: sin él se podrían adivinar ("nodo-" + hash de "pve").
func NuevoCatalogo(nodos, storages map[string]string, secreto string) (*Catalogo, error) {
	if secreto == "" {
		return nil, fmt.Errorf("el catálogo de topología necesita un secreto")
	}
	n, err := nuevaTraduccion(nodos, prefijoNodo)
	if err != nil {
		return nil, fmt.Errorf("alias de nodos: %w", err)
	}
	s, err := nuevaTraduccion(storages, prefijoStorage)
	if err != nil {
		return nil, fmt.Errorf("alias de storages: %w", err)
	}
	return &Catalogo{nodos: n, storages: s, secreto: []byte("centinela:topology:v1:" + secreto),
		nodosGenericos: map[string]string{}, storagesGener: map[string]string{}}, nil
}

func nuevaTraduccion(alias map[string]string, prefijoGenerico string) (traduccion, error) {
	t := traduccion{alias: map[string]string{}, real: map[string]string{}}
	for real, a := range alias {
		real, a = strings.TrimSpace(real), strings.TrimSpace(a)
		if real == "" || a == "" {
			return t, fmt.Errorf("nombre real y alias no pueden estar vacíos (%q=%q)", real, a)
		}
		if otro, repetido := t.real[a]; repetido {
			return t, fmt.Errorf("el alias %q se repite (%q y %q): no se podría volver al nombre real", a, otro, real)
		}
		if strings.HasPrefix(a, prefijoGenerico) && esGenerico(a, prefijoGenerico) {
			return t, fmt.Errorf("el alias %q tiene la forma de un alias genérico", a)
		}
		t.alias[real], t.real[a] = a, real
	}
	// Un alias que es el nombre real de otro recurso haría que el nombre real
	// que manda un ADMIN se resuelva al recurso equivocado.
	for a, real := range t.real {
		if _, esReal := t.alias[a]; esReal && a != real {
			return t, fmt.Errorf("el alias %q de %q es el nombre real de otro recurso", a, real)
		}
	}
	return t, nil
}

var hexGenerico = regexp.MustCompile(`^[0-9a-f]{8}$`)

func esGenerico(alias, prefijo string) bool {
	return hexGenerico.MatchString(strings.TrimPrefix(alias, prefijo))
}

// generico deriva un alias estable y no adivinable para un nombre sin alias.
func (c *Catalogo) generico(real, prefijo string, registro map[string]string) string {
	mac := hmac.New(sha256.New, c.secreto)
	mac.Write([]byte(prefijo + real))
	alias := prefijo + hex.EncodeToString(mac.Sum(nil))[:8]
	c.muGenericos.Lock()
	registro[alias] = real
	c.muGenericos.Unlock()
	return alias
}

func (c *Catalogo) enmascarar(t traduccion, real, prefijo string, registro map[string]string) string {
	if real == "" {
		return ""
	}
	if alias, ok := t.alias[real]; ok {
		return alias
	}
	return c.generico(real, prefijo, registro)
}

func (c *Catalogo) resolver(t traduccion, alias string, registro map[string]string) string {
	if real, ok := t.real[alias]; ok {
		return real
	}
	c.muGenericos.RLock()
	defer c.muGenericos.RUnlock()
	if real, ok := registro[alias]; ok {
		return real
	}
	// No es un alias conocido: se usa tal cual (un ADMIN manda el nombre real).
	return alias
}

// MaskNode devuelve el alias público de un nodo físico.
func (c *Catalogo) MaskNode(realNode string) string {
	return c.enmascarar(c.nodos, realNode, prefijoNodo, c.nodosGenericos)
}

// ResolveNode devuelve el nodo real de un alias; un nombre que no es alias se devuelve igual.
func (c *Catalogo) ResolveNode(alias string) string {
	return c.resolver(c.nodos, alias, c.nodosGenericos)
}

// MaskStorage devuelve el alias público de un pool de almacenamiento.
func (c *Catalogo) MaskStorage(realStorage string) string {
	return c.enmascarar(c.storages, realStorage, prefijoStorage, c.storagesGener)
}

// ResolveStorage devuelve el storage real de un alias; un nombre que no es alias se devuelve igual.
func (c *Catalogo) ResolveStorage(alias string) string {
	return c.resolver(c.storages, alias, c.storagesGener)
}

// MaskTexto reemplaza en un texto libre (los errores de Proxmox: "storage
// 'local-lvm' does not exist") los nodos y storages del catálogo, los que ya
// recibieron un alias genérico y los adicionales que se pasen, por sus alias. Solo reemplaza nombres completos:
// "local" no se toca dentro de "local-lvm" ni de "localhost".
func (c *Catalogo) MaskTexto(texto string, nodos, storages []string) string {
	if texto == "" {
		return texto
	}
	reemplazos := map[string]string{}
	for real, alias := range c.storages.alias {
		reemplazos[real] = alias
	}
	for _, s := range storages {
		if s != "" {
			reemplazos[s] = c.MaskStorage(s)
		}
	}
	for real, alias := range c.nodos.alias {
		reemplazos[real] = alias
	}
	for _, n := range nodos {
		if n != "" {
			reemplazos[n] = c.MaskNode(n)
		}
	}
	// Los nombres sin alias del catálogo que ya se enmascararon alguna vez.
	c.muGenericos.RLock()
	for alias, real := range c.storagesGener {
		reemplazos[real] = alias
	}
	for alias, real := range c.nodosGenericos {
		reemplazos[real] = alias
	}
	c.muGenericos.RUnlock()
	nombres := make([]string, 0, len(reemplazos))
	for n := range reemplazos {
		nombres = append(nombres, regexp.QuoteMeta(n))
	}
	if len(nombres) == 0 {
		return texto
	}
	// Los más largos primero, para que "local-lvm" gane sobre "local".
	sort.Slice(nombres, func(i, j int) bool { return len(nombres[i]) > len(nombres[j]) })
	patron := regexp.MustCompile(`(^|[^A-Za-z0-9_.\-])(` + strings.Join(nombres, "|") + `)([^A-Za-z0-9_.\-]|$)`)
	// Dos pasadas: una coincidencia consume el separador y la siguiente,
	// pegada ("local local"), quedaría sin reemplazar en una sola.
	for i := 0; i < 2; i++ {
		texto = patron.ReplaceAllStringFunc(texto, func(m string) string {
			p := patron.FindStringSubmatch(m)
			return p[1] + reemplazos[p[2]] + p[3]
		})
	}
	return texto
}

// ParseAlias lee "real=alias,real=alias" (TOPOLOGY_NODE_ALIASES y
// TOPOLOGY_STORAGE_ALIASES). Vacío devuelve nil.
func ParseAlias(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	alias := map[string]string{}
	for _, par := range strings.Split(raw, ",") {
		real, a, ok := strings.Cut(par, "=")
		real, a = strings.TrimSpace(real), strings.TrimSpace(a)
		if !ok || real == "" || a == "" {
			return nil, fmt.Errorf("se esperaba real=alias y vino %q", strings.TrimSpace(par))
		}
		if _, repetido := alias[real]; repetido {
			return nil, fmt.Errorf("%q aparece dos veces", real)
		}
		alias[real] = a
	}
	return alias, nil
}
