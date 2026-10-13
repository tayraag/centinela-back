package main

import (
	"fmt"
	"net/http"
	"strings"
)

// storageInfo representa un almacenamiento en Proxmox VE según GET /nodes/{node}/storage.
type storageInfo struct {
	Storage string `json:"storage"`
	Type    string `json:"type"`
	Total   int64  `json:"total"`
	Used    int64  `json:"used"`
	Avail   int64  `json:"avail"`
	Active  int    `json:"active"`
	Content string `json:"content"`
	Shared  int    `json:"shared"`
	Enabled int    `json:"enabled"`
}

// storageContentItem representa un elemento de contenido (ISO, plantilla vztmpl, imagen de disco).
type storageContentItem struct {
	Volid   string `json:"volid"`
	Content string `json:"content"` // "iso", "vztmpl", "images", "rootdir", "backup"
	Format  string `json:"format"`  // "iso", "tar.zst", "raw", "qcow2"
	Size    int64  `json:"size"`
	CTime   int64  `json:"ctime,omitempty"`
}

// storagesDisponibles devuelve el catálogo de storages simulados para el nodo.
func (s *Simulador) storagesDisponibles() []storageInfo {
	// Disco LVM usado dinámico según instancias
	lvmUsado := s.discoLVMUsado()
	lvmTotal := int64(151636672512) // ~141 GB
	lvmAvail := lvmTotal - lvmUsado
	if lvmAvail < 0 {
		lvmAvail = 0
	}

	localTotal := int64(72722055168) // ~67 GB
	localUsado := int64(5477761024)  // ~5 GB
	localAvail := localTotal - localUsado

	nfsTotal := int64(1099511627776) // ~1 TB
	nfsUsado := int64(214748364800)  // ~200 GB
	nfsAvail := nfsTotal - nfsUsado

	return []storageInfo{
		{
			Storage: "local",
			Type:    "dir",
			Total:   localTotal,
			Used:    localUsado,
			Avail:   localAvail,
			Active:  1,
			Content: "iso,vztmpl,backup",
			Shared:  0,
			Enabled: 1,
		},
		{
			Storage: "local-lvm",
			Type:    "lvmthin",
			Total:   lvmTotal,
			Used:    lvmUsado,
			Avail:   lvmAvail,
			Active:  1,
			Content: "rootdir,images",
			Shared:  0,
			Enabled: 1,
		},
		{
			Storage: "shared-nfs",
			Type:    "nfs",
			Total:   nfsTotal,
			Used:    nfsUsado,
			Avail:   nfsAvail,
			Active:  1,
			Content: "iso,vztmpl,images,backup",
			Shared:  1,
			Enabled: 1,
		},
	}
}

// catalogoContenidos contiene las plantillas LXC (vztmpl) e imágenes ISO (iso) disponibles.
var catalogoContenidos = map[string][]storageContentItem{
	"local": {
		{
			Volid:   "local:iso/debian-13.6.0-amd64-netinst.iso",
			Content: "iso",
			Format:  "iso",
			Size:    791674880, // ~755 MB
			CTime:   1786823000,
		},
		{
			Volid:   "local:iso/ubuntu-24.04-live-server-amd64.iso",
			Content: "iso",
			Format:  "iso",
			Size:    2080374784, // ~1.9 GB
			CTime:   1786823100,
		},
		{
			Volid:   "local:vztmpl/debian-12-standard_12.2-1_amd64.tar.zst",
			Content: "vztmpl",
			Format:  "tar.zst",
			Size:    128450560, // ~122 MB
			CTime:   1786823200,
		},
		{
			Volid:   "local:vztmpl/ubuntu-22.04-standard_22.04-1_amd64.tar.zst",
			Content: "vztmpl",
			Format:  "tar.zst",
			Size:    141557760, // ~135 MB
			CTime:   1786823250,
		},
	},
	"shared-nfs": {
		{
			Volid:   "shared-nfs:iso/alpine-virt-3.19.1-x86_64.iso",
			Content: "iso",
			Format:  "iso",
			Size:    62914560, // ~60 MB
			CTime:   1786823300,
		},
		{
			Volid:   "shared-nfs:vztmpl/alpine-3.19-default_20240207_amd64.tar.xz",
			Content: "vztmpl",
			Format:  "tar.xz",
			Size:    3670016, // ~3.5 MB
			CTime:   1786823350,
		},
	},
	"local-lvm": {},
}

// ==========================================
// GET /nodes/{node}/storage
// ==========================================

func (s *Simulador) listarStorage(w http.ResponseWriter, r *http.Request) {
	if nodo := r.PathValue("node"); nodo != s.cfg.Nodo {
		errorPVE(w, http.StatusInternalServerError, fmt.Sprintf("hostname lookup '%s' failed - failed to get address info for: %s: Name or service not known", nodo, nodo))
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	storages := s.storagesDisponibles()
	// Filtro opcional por ?content=... como en Proxmox
	filtroContent := r.URL.Query().Get("content")
	if filtroContent != "" {
		filtrados := make([]storageInfo, 0, len(storages))
		for _, st := range storages {
			if strings.Contains(st.Content, filtroContent) {
				filtrados = append(filtrados, st)
			}
		}
		responder(w, filtrados)
		return
	}

	responder(w, storages)
}

// ==========================================
// GET /nodes/{node}/storage/{storage}/content
// ==========================================

func (s *Simulador) listarContenidoStorage(w http.ResponseWriter, r *http.Request) {
	if nodo := r.PathValue("node"); nodo != s.cfg.Nodo {
		errorPVE(w, http.StatusInternalServerError, fmt.Sprintf("hostname lookup '%s' failed - failed to get address info for: %s: Name or service not known", nodo, nodo))
		return
	}

	storageName := r.PathValue("storage")
	s.mu.Lock()
	defer s.mu.Unlock()

	// Verificar si el storage existe
	storages := s.storagesDisponibles()
	var stExiste bool
	for _, st := range storages {
		if st.Storage == storageName {
			stExiste = true
			break
		}
	}
	if !stExiste {
		errorPVE(w, http.StatusInternalServerError, fmt.Sprintf("storage '%s' does not exist", storageName))
		return
	}

	items := catalogoContenidos[storageName]
	if items == nil {
		items = []storageContentItem{}
	}

	// Filtro opcional por ?content=iso o ?content=vztmpl
	filtroContent := r.URL.Query().Get("content")
	if filtroContent != "" {
		filtrados := make([]storageContentItem, 0, len(items))
		for _, it := range items {
			if it.Content == filtroContent {
				filtrados = append(filtrados, it)
			}
		}
		responder(w, filtrados)
		return
	}

	responder(w, items)
}
