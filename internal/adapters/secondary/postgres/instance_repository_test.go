package postgres

import (
	"context"
	"encoding/json"
	"testing"

	"el-centinela/internal/core/domain"
	"el-centinela/internal/core/ports"
)

func TestInstanceRepository_PurgarPermisosPorVmid(t *testing.T) {
	db := conectarDBTest(t)
	repo := NewInstanceRepository(db)
	ctx := context.Background()

	// 1. Obtener un usuario de prueba existente
	var usuario domain.Usuario
	if err := db.First(&usuario).Error; err != nil {
		t.Skipf("Se requiere al menos un usuario en la base para el test de integración: %v", err)
	}

	testVmidPurga := 99991
	testVmidConserva := 99992

	// Limpiar posibles residuos previos
	_ = db.Where("vmid_proxmox IN (?, ?)", testVmidPurga, testVmidConserva).Delete(&domain.PermisoInstancia{}).Error

	// 2. Crear permisos de prueba
	p1 := domain.PermisoInstancia{
		UsuarioID:   usuario.ID,
		VmidProxmox: testVmidPurga,
		NivelAcceso: ports.NivelAccesoFullAccess,
	}
	p2 := domain.PermisoInstancia{
		UsuarioID:   usuario.ID,
		VmidProxmox: testVmidConserva,
		NivelAcceso: ports.NivelAccesoReadOnly,
	}
	if err := db.Create(&p1).Error; err != nil {
		t.Fatalf("Error al crear permiso de prueba 1: %v", err)
	}
	if err := db.Create(&p2).Error; err != nil {
		t.Fatalf("Error al crear permiso de prueba 2: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Where("vmid_proxmox IN (?, ?)", testVmidPurga, testVmidConserva).Delete(&domain.PermisoInstancia{}).Error
	})

	// 3. Ejecutar purga de testVmidPurga
	adminID := usuario.ID
	afectados, err := repo.PurgarPermisosPorVmid(ctx, testVmidPurga, ports.RecursoVM, &adminID)
	if err != nil {
		t.Fatalf("Error en PurgarPermisosPorVmid: %v", err)
	}

	// 4. Verificar lista de afectados
	if len(afectados) != 1 || afectados[0] != usuario.ID.String() {
		t.Errorf("Afectados esperados [%s], obtenidos %v", usuario.ID, afectados)
	}

	// 5. Verificar que el permiso de testVmidPurga ya NO existe en BD
	tieneAccesoPurga, err := repo.VerificarAcceso(ctx, usuario.ID, testVmidPurga, ports.NivelAccesoReadOnly)
	if err != nil {
		t.Fatalf("Error al verificar acceso: %v", err)
	}
	if tieneAccesoPurga {
		t.Errorf("El permiso para el VMID purgado %d todavía existe en la base", testVmidPurga)
	}

	// 6. Verificar que el permiso de testVmidConserva se mantiene intacto
	tieneAccesoConserva, err := repo.VerificarAcceso(ctx, usuario.ID, testVmidConserva, ports.NivelAccesoReadOnly)
	if err != nil {
		t.Fatalf("Error al verificar acceso: %v", err)
	}
	if !tieneAccesoConserva {
		t.Errorf("El permiso para el VMID conservado %d debió mantenerse", testVmidConserva)
	}

	// 7. Verificar traza inmutable en auditoria
	var traza domain.Auditoria
	if err := db.Where("accion = ? AND instancia_id = ?", ports.AccionInstancePermissionsPurged, "99991").
		Order("fecha_hora desc").
		First(&traza).Error; err != nil {
		t.Fatalf("No se encontró el registro de auditoría de la purga: %v", err)
	}

	if traza.Resultado != "SUCCESS" {
		t.Errorf("Resultado esperado 'SUCCESS', obtenido '%s'", traza.Resultado)
	}
	if traza.Detalles == nil {
		t.Fatal("Detalles de auditoría es nil")
	}

	var det map[string]any
	if err := json.Unmarshal([]byte(*traza.Detalles), &det); err != nil {
		t.Fatalf("Error al deserializar detalles: %v", err)
	}
	if det["vmid"] != float64(testVmidPurga) {
		t.Errorf("vmid en detalles esperado %d, obtenido %v", testVmidPurga, det["vmid"])
	}
	if usuarios, ok := det["usuariosAfectados"].([]any); !ok || len(usuarios) != 1 || usuarios[0] != usuario.ID.String() {
		t.Errorf("usuariosAfectados inesperado: %v", det["usuariosAfectados"])
	}
}
