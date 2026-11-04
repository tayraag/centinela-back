package postgres

import (
	"fmt"
	"testing"

	"el-centinela/internal/core/ports"

	"github.com/google/uuid"
)

// RF-08: la migración traduce los códigos viejos al vocabulario cerrado, deja
// el trigger de inmutabilidad activo y es idempotente.
func TestNormalizarVocabularioAuditoria(t *testing.T) {
	db := conectarDBTest(t)
	marca := "test-vocab-" + uuid.New().String()[:8]
	viejos := []struct{ accion, resultado, accionEsperada, resultadoEsperado string }{
		{"START", "SUCCESS", ports.AccionIniciarVM, ports.ResultadoExito},
		{"STOP_VM", "FAILED", ports.AccionDetenerVM, ports.ResultadoFalla},
		{"start_vm", "PENDING", ports.AccionIniciarVM, ports.ResultadoPendiente},
		{"SHUTDOWN", "SUCCESS", ports.AccionApagarVM, ports.ResultadoExito},
		{"DELETE", "SUCCESS", ports.AccionEliminarVM, ports.ResultadoExito},
		{"CONFIG", "FAILED", ports.AccionConfigurarInstancia, ports.ResultadoFalla},
		{"CONFIG_INSTANCE", "SUCCESS", ports.AccionConfigurarInstancia, ports.ResultadoExito},
		{"CREAR_SNAPSHOT", "PENDIENTE", ports.AccionCrearSnapshot, ports.ResultadoPendiente},
		{"ROLLBACK_SNAPSHOT", "SUCCESS", ports.AccionRevertirSnapshot, ports.ResultadoExito},
		{"DELETE_SNAPSHOT", "SUCCESS", ports.AccionEliminarSnapshot, ports.ResultadoExito},
		{"CAMBIAR_CONTRASENA", "EXITO", ports.AccionCambiarContrasena, ports.ResultadoExito},
		{"LOGIN", "FALLA", ports.AccionLogin, ports.ResultadoFalla}, // ya canónica: no cambia
	}
	for i, v := range viejos {
		if err := db.Exec(`INSERT INTO auditoria (fecha_hora, accion, instancia_id, resultado, detalles) VALUES (now(), ?, ?, ?, ?)`,
			v.accion, marca, v.resultado, fmt.Sprintf(`{"orden":"%02d"}`, i)).Error; err != nil {
			t.Fatalf("insertar %s: %v", v.accion, err)
		}
	}

	normalizarVocabularioAuditoria(db)

	type fila struct{ Accion, Resultado, Detalles string }
	var filas []fila
	if err := db.Raw(`SELECT accion, resultado, detalles::text FROM auditoria WHERE instancia_id = ? ORDER BY detalles->>'orden'`, marca).Scan(&filas).Error; err != nil {
		t.Fatal(err)
	}
	if len(filas) != len(viejos) {
		t.Fatalf("se esperaban %d filas, hay %d", len(viejos), len(filas))
	}
	for i, v := range viejos {
		if filas[i].Accion != v.accionEsperada || filas[i].Resultado != v.resultadoEsperado {
			t.Errorf("%s/%s → %s/%s, se esperaba %s/%s", v.accion, v.resultado, filas[i].Accion, filas[i].Resultado, v.accionEsperada, v.resultadoEsperado)
		}
	}
	var viejosRestantes int64
	db.Raw(`SELECT count(*) FROM auditoria WHERE resultado IN ('SUCCESS', 'FAILED', 'PENDING')`).Scan(&viejosRestantes)
	if viejosRestantes != 0 {
		t.Errorf("DoD: no deben quedar resultados SUCCESS/FAILED/PENDING, quedan %d", viejosRestantes)
	}

	// El trigger quedó activo: la tabla sigue siendo append-only.
	var habilitado string
	db.Raw(`SELECT tgenabled FROM pg_trigger WHERE tgname = 'trg_auditoria_inmutable'`).Scan(&habilitado)
	if habilitado != "O" {
		t.Fatalf("trg_auditoria_inmutable quedó en %q", habilitado)
	}
	if err := db.Exec(`UPDATE auditoria SET resultado = 'FALLA' WHERE instancia_id = ?`, marca).Error; err == nil {
		t.Fatal("con el trigger activo, UPDATE debe fallar")
	}

	// Idempotente: una segunda corrida no falla ni cambia nada.
	normalizarVocabularioAuditoria(db)
	var despues []fila
	db.Raw(`SELECT accion, resultado, detalles::text FROM auditoria WHERE instancia_id = ? ORDER BY detalles->>'orden'`, marca).Scan(&despues)
	for i := range filas {
		if despues[i] != filas[i] {
			t.Errorf("la segunda corrida cambió %v → %v", filas[i], despues[i])
		}
	}
}
