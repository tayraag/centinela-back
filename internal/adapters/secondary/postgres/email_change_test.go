package postgres_test

import (
	"context"
	"el-centinela/internal/adapters/secondary/postgres"
	"el-centinela/internal/core/ports"
	"errors"
	"testing"
)

func TestEmailConfirmacionTransaccion(t *testing.T) {
	db, id, _ := baseRF08(t)
	for _, ddl := range []string{
		"ALTER TABLE usuarios ADD COLUMN email_usuario text, ADD COLUMN activo boolean DEFAULT true, ADD COLUMN eliminado_en timestamptz",
		"CREATE UNIQUE INDEX uq_email_test ON usuarios (lower(btrim(email_usuario))) WHERE eliminado_en IS NULL",
		"UPDATE usuarios SET email_usuario = 'original@example.com'",
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	repo := postgres.NewUserRepository(db)
	ctx := context.Background()
	audit := ports.RegistrarAuditoriaInput{UsuarioID: id, Accion: "UPDATE_PROFILE_EMAIL_CONFIRMED", Resultado: "SUCCESS", Detalles: map[string]any{"solicitudId": "test"}}
	// Forzar fallo real de INSERT para verificar que el correo se revierte.
	if err := db.Exec("ALTER TABLE auditoria ADD CONSTRAINT rechazar_confirmacion CHECK (accion <> 'UPDATE_PROFILE_EMAIL_CONFIRMED')").Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.ConfirmarCambioEmail(ctx, id, "original@example.com", "nuevo@example.com", audit); err == nil {
		t.Fatal("se aceptó auditoría fallida")
	}
	var email string
	if err := db.Raw("SELECT email_usuario FROM usuarios WHERE id = ?", id).Scan(&email).Error; err != nil {
		t.Fatal(err)
	}
	if email != "original@example.com" {
		t.Fatal("no se revirtió el correo", email)
	}
	if err := db.Exec("ALTER TABLE auditoria DROP CONSTRAINT rechazar_confirmacion").Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.ConfirmarCambioEmail(ctx, id, email, "nuevo@example.com", audit); err != nil {
		t.Fatal(err)
	}
	if err := repo.ConfirmarCambioEmail(ctx, id, email, "otro@example.com", audit); !errors.Is(err, ports.ErrEmailToken) {
		t.Fatal("se aceptó solicitud obsoleta", err)
	}
	var count int64
	if err := db.Table("auditoria").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("auditoría duplicada", count)
	}
}
