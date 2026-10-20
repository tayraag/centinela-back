package ports

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
)

// ReferenciasInstancia traduce entre el VMID interno de Proxmox (base, Redis,
// Proxmox) y la referencia pública opaca ("inst_...") que es lo único que ve
// el front: en rutas, respuestas, eventos SSE, WebSocket, permisos y auditoría.
type ReferenciasInstancia interface {
	// OfuscarVMID devuelve la referencia pública de un VMID positivo.
	OfuscarVMID(vmid int) (string, error)
	// DesofuscarVMID devuelve el VMID de una referencia, o error si el token
	// fue alterado, es de otro servidor o no tiene el formato.
	DesofuscarVMID(ref string) (int, error)
}

// IDInstanciaPublico traduce un VMID guardado como texto ("110", así lo
// guardan la auditoría y las tareas) a su referencia pública. Lo que no es un
// VMID válido se devuelve vacío: nunca se expone el número.
func IDInstanciaPublico(refs ReferenciasInstancia, vmid string) string {
	n, err := strconv.Atoi(vmid)
	if err != nil {
		return ""
	}
	ref, err := refs.OfuscarVMID(n)
	if err != nil {
		return ""
	}
	return ref
}

// DetallesAuditoriaPublicos reemplaza los VMID que la auditoría guarda dentro
// de detalles: {"vmid": 110} (purga de permisos) y {"permisos": [{"vmid": 110,
// ...}]} (asignación) pasan a {"vmid": "inst_..."}, y el VMID de la
// instancia del registro (instanciaID) se reemplaza por su referencia en los
// textos, como los errores de Proxmox ("CT 110 not running"). Si no hay nada
// que traducir devuelve el mismo texto.
func DetallesAuditoriaPublicos(refs ReferenciasInstancia, detalles, instanciaID string) string {
	if detalles == "" {
		return detalles
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(detalles)))
	dec.UseNumber()
	var campos map[string]any
	if dec.Decode(&campos) != nil || campos == nil {
		return detalles
	}
	cambio := reemplazarVmid(refs, campos)
	if permisos, ok := campos["permisos"].([]any); ok {
		for _, p := range permisos {
			if permiso, ok := p.(map[string]any); ok && reemplazarVmid(refs, permiso) {
				cambio = true
			}
		}
	}
	if vmid, err := strconv.Atoi(instanciaID); err == nil {
		ref := IDInstanciaPublico(refs, instanciaID)
		var traducido any
		if traducido, cambio = ocultarEnValor(campos, vmid, ref, cambio); cambio {
			campos = traducido.(map[string]any)
		}
	}
	if !cambio {
		return detalles
	}
	traducidos, err := json.Marshal(campos)
	if err != nil {
		return detalles
	}
	return string(traducidos)
}

// OcultarVmidEnTexto reemplaza el VMID suelto dentro de un texto libre (los
// errores de Proxmox lo nombran: "CT 201 not running", "lock-110.conf") por
// su referencia pública. No toca números más largos que lo contienen (1101).
func OcultarVmidEnTexto(texto string, vmid int, ref string) string {
	if vmid <= 0 || texto == "" {
		return texto
	}
	if ref == "" {
		ref = "[instancia]"
	}
	patron := regexp.MustCompile(`\b` + strconv.Itoa(vmid) + `\b`)
	return patron.ReplaceAllLiteralString(texto, ref)
}

// OcultarVmidEnDetalles devuelve una copia de detalles con el VMID oculto en
// todos los textos. No modifica el mapa original (puede estar compartido).
func OcultarVmidEnDetalles(detalles map[string]any, vmid int, ref string) map[string]any {
	if detalles == nil {
		return nil
	}
	copia, _ := ocultarEnValor(detalles, vmid, ref, false)
	return copia.(map[string]any)
}

// ocultarEnValor recorre mapas, listas y textos y devuelve una copia con el
// VMID reemplazado; cambio queda en true si algo cambió.
func ocultarEnValor(valor any, vmid int, ref string, cambio bool) (any, bool) {
	switch v := valor.(type) {
	case string:
		oculto := OcultarVmidEnTexto(v, vmid, ref)
		return oculto, cambio || oculto != v
	case map[string]any:
		copia := make(map[string]any, len(v))
		for k, elem := range v {
			copia[k], cambio = ocultarEnValor(elem, vmid, ref, cambio)
		}
		return copia, cambio
	case []any:
		copia := make([]any, len(v))
		for i, elem := range v {
			copia[i], cambio = ocultarEnValor(elem, vmid, ref, cambio)
		}
		return copia, cambio
	}
	return valor, cambio
}

func reemplazarVmid(refs ReferenciasInstancia, campos map[string]any) bool {
	vmid, ok := campos["vmid"]
	if !ok {
		return false
	}
	delete(campos, "vmid")
	if ref := IDInstanciaPublico(refs, fmt.Sprint(vmid)); ref != "" {
		campos["vmid"] = ref
	}
	return true
}
