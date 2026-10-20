package http

import (
	"regexp"
	"strconv"

	"el-centinela/internal/adapters/primary/http/middleware"
	"el-centinela/internal/core/domain/topology"
	"el-centinela/internal/infrastructure/crypto"
)

// Ayudas de los tests (también para el paquete http_test) para trabajar con
// referencias públicas de instancias.

// RefsPrueba traduce VMIDs con un salt fijo de prueba.
var RefsPrueba = func() *crypto.OfuscadorVMID {
	o, err := crypto.NuevoOfuscadorVMID("salt-de-prueba-de-al-menos-32-caracteres!")
	if err != nil {
		panic(err)
	}
	return o
}()

// ResolverPrueba es ResolveInstanceRef sobre el parámetro :vmid de las rutas de prueba.
var ResolverPrueba = middleware.ResolveInstanceRef(RefsPrueba, "vmid")

// TopologiaPrueba es el catálogo de alias por defecto con el nodo "pve1" (el
// de los mocks) como "nodo-principal".
var TopologiaPrueba = func() *topology.Catalogo {
	c, err := topology.NuevoCatalogo(map[string]string{"pve1": topology.AliasNodoPorDefecto}, topology.AliasStoragePorDefecto, "secreto-de-prueba")
	if err != nil {
		panic(err)
	}
	return c
}()

// RefInst devuelve la referencia pública de un VMID.
func RefInst(vmid int) string {
	ref, err := RefsPrueba.OfuscarVMID(vmid)
	if err != nil {
		panic(err)
	}
	return ref
}

var vmidEnRuta = regexp.MustCompile(`/instances/(\d+)`)

// ConRef reemplaza el VMID de una ruta (/api/instances/110/start) por su referencia.
func ConRef(ruta string) string {
	return vmidEnRuta.ReplaceAllStringFunc(ruta, func(m string) string {
		vmid, _ := strconv.Atoi(m[len("/instances/"):])
		return "/instances/" + RefInst(vmid)
	})
}
