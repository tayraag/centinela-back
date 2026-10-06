package postgres

import (
	"context"
	"log"
	"time"

	"gorm.io/gorm"
)

const intervaloPurga = 1 * time.Hour

// PurgaWorker elimina periódicamente las sesiones inactivas o expiradas de la
// tabla sesiones_activas, evitando la acumulación indefinida de filas en disco.
//
// El DELETE se limita a las sesiones que cumplen al menos una de estas condiciones:
//   - activa = false  → el usuario cerró sesión explícitamente (logout)
//   - fecha_expiracion < NOW() → el refresh token ya venció (sesión muerta)
//
// El índice parcial idx_sesiones_activas_vigentes (WHERE activa = true) excluye
// estas filas de las búsquedas de autenticación, por lo que no degradan la
// performance del sistema antes de ser purgadas; aun así se limpian para evitar
// el crecimiento indefinido de la tabla y mantener el vacío de PostgreSQL liviano.
type PurgaWorker struct {
	db        *gorm.DB
	intervalo time.Duration
}

// NuevoPurgaWorker crea un PurgaWorker listo para iniciar.
// El intervalo de ejecución es configurable para poder acortarlo en tests.
func NuevoPurgaWorker(db *gorm.DB, intervalo time.Duration) *PurgaWorker {
	return &PurgaWorker{db: db, intervalo: intervalo}
}

// Iniciar arranca el loop de purga en una goroutine separada.
// Se detiene limpiamente cuando el contexto ctx es cancelado.
// Llamar con `go worker.Iniciar(ctx)` desde main.
func (w *PurgaWorker) Iniciar(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	log.Printf("🧹 Worker de purga de sesiones iniciado (intervalo: %s)", w.intervalo)

	// Ejecutar una purga inmediata al arranque para limpiar lo acumulado
	// durante el tiempo en que el servidor estuvo apagado.
	w.purgar()

	for {
		select {
		case <-ctx.Done():
			log.Println("🧹 Worker de purga de sesiones detenido.")
			return
		case <-ticker.C:
			w.purgar()
		}
	}
}

// purgar ejecuta el DELETE y registra el resultado en el log del servidor.
// Los errores no se propagan: una purga fallida es no-crítica; la siguiente
// ejecución lo intentará de nuevo.
func (w *PurgaWorker) purgar() {
	resultado := w.db.Exec(`
		DELETE FROM sesiones_activas
		WHERE activa = false OR fecha_expiracion < NOW()
	`)
	if resultado.Error != nil {
		log.Printf("⚠️  [PURGA] Error al purgar sesiones expiradas: %v", resultado.Error)
		return
	}
	if resultado.RowsAffected > 0 {
		log.Printf("🧹 [PURGA] %d sesión(es) expirada(s)/inactiva(s) eliminada(s)", resultado.RowsAffected)
	}
}
