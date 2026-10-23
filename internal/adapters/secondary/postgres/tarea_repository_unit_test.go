package postgres

import (
	"testing"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
)

func TestDebeAsignarPermisoCreacion(t *testing.T) {
	casos := []struct {
		nombre     string
		accion     string
		estado     string
		exitstatus string
		esperado   bool
	}{
		{"VM OK", ports.AccionProxmoxCrearVM, ports.TareaCompleted, "OK", true},
		{"LXC con advertencias", ports.AccionProxmoxCrearLXC, ports.TareaCompleted, "WARNINGS: 1", false},
		{"creación fallida", ports.AccionProxmoxCrearVM, ports.TareaFailed, "unable to create VM", false},
		{"acción operativa", "start", ports.TareaCompleted, "OK", false},
		{"completed sin éxito Proxmox", ports.AccionCrearLXC, ports.TareaCompleted, "error", false},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			tarea := domain.TareaAsincrona{Accion: caso.accion}
			obtenido := debeAsignarPermisoCreacion(tarea, caso.estado, map[string]any{"exitstatus": caso.exitstatus})
			if obtenido != caso.esperado {
				t.Fatalf("debeAsignarPermisoCreacion() = %v, se esperaba %v", obtenido, caso.esperado)
			}
		})
	}
}

func TestDebePurgarPermisosDestruccion(t *testing.T) {
	casos := []struct {
		nombre, accion, estado, exitstatus string
		esperado                           bool
	}{
		{"delete OK", "delete", ports.TareaCompleted, "OK", true},
		{"qmdestroy OK", "qmdestroy", ports.TareaCompleted, "OK", true},
		{"vzdestroy OK", "vzdestroy", ports.TareaCompleted, "OK", true},
		{"destroy fallido", "qmdestroy", ports.TareaFailed, "TASK ERROR", false},
		{"destroy con warnings", "vzdestroy", ports.TareaCompleted, "WARNINGS: 1", false},
		{"creación", ports.AccionProxmoxCrearVM, ports.TareaCompleted, "OK", false},
		{"otra acción", "stop", ports.TareaCompleted, "OK", false},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			tarea := domain.TareaAsincrona{Accion: caso.accion}
			obtenido := debePurgarPermisosDestruccion(tarea, caso.estado, map[string]any{"exitstatus": caso.exitstatus})
			if obtenido != caso.esperado {
				t.Fatalf("debePurgarPermisosDestruccion() = %v, se esperaba %v", obtenido, caso.esperado)
			}
		})
	}
}
