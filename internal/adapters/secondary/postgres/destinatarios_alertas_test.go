package postgres

import (
	"context"
	"reflect"
	"testing"
	"time"

	"el-centinela/internal/core/domain"

	"github.com/google/uuid"
)

// Destinatarios del correo de una alerta: ADMIN activos y, para una instancia,
// también los OPERATOR activos con FULL_ACCESS sobre ella. Nunca suspendidos,
// eliminados ni operadores con READ_ONLY o de otra instancia.
func TestEmailsParaAlerta(t *testing.T) {
	t.Setenv("DB_DSN", baseVacia(t))
	t.Setenv("APP_ENV", "production") // sin administrador sembrado: la base arranca vacía
	db, err := InitDB()
	if err != nil {
		t.Fatal(err)
	}
	defer cerrar(db)

	org := domain.Organizacion{ID: uuid.New(), NombreOrg: "Org", CreadoPor: uuid.Nil}
	if err := db.Create(&org).Error; err != nil {
		t.Fatal(err)
	}
	eliminado := time.Now()
	usuarios := []struct {
		email, rol string
		activo     bool
		eliminado  *time.Time
		permisos   map[int]string
	}{
		{"admin@x.com", "ADMIN", true, nil, nil},
		{"admin.suspendido@x.com", "ADMIN", false, nil, nil},
		{"admin.eliminado@x.com", "ADMIN", false, &eliminado, nil},
		{"op.full@x.com", "OPERATOR", true, nil, map[int]string{110: "FULL_ACCESS"}},
		{"op.lectura@x.com", "OPERATOR", true, nil, map[int]string{110: "READ_ONLY"}},
		{"op.otra@x.com", "OPERATOR", true, nil, map[int]string{120: "FULL_ACCESS"}},
		{"op.suspendido@x.com", "OPERATOR", false, nil, map[int]string{110: "FULL_ACCESS"}},
	}
	for i, u := range usuarios {
		usuario := domain.Usuario{ID: uuid.New(), OrganizacionID: org.ID, NombreCompleto: u.email, NombreUsuario: "u" + string(rune('a'+i)),
			EmailUsuario: u.email, ContrasenaHash: "x", Rol: u.rol, Activo: true}
		if err := db.Create(&usuario).Error; err != nil {
			t.Fatal(err)
		}
		// La suspensión y la baja se aplican después: GORM no guarda activo=false al crear.
		if err := db.Model(&usuario).Updates(map[string]any{"activo": u.activo, "eliminado_en": u.eliminado}).Error; err != nil {
			t.Fatal(err)
		}
		for vmid, nivel := range u.permisos {
			if err := db.Create(&domain.PermisoInstancia{UsuarioID: usuario.ID, VmidProxmox: vmid, NivelAcceso: nivel}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}

	repo := NewUserRepository(db)
	for vmid, esperado := range map[int][]string{
		0:   {"admin@x.com"},
		110: {"admin@x.com", "op.full@x.com"},
		120: {"admin@x.com", "op.otra@x.com"},
		999: {"admin@x.com"},
	} {
		emails, err := repo.EmailsParaAlerta(context.Background(), vmid)
		if err != nil || !reflect.DeepEqual(emails, esperado) {
			t.Errorf("vmid %d: %v %v, se esperaba %v", vmid, emails, err, esperado)
		}
	}
}
