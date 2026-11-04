-- Normalización histórica del vocabulario de auditoría (RF-08).
--
-- Traduce los códigos viejos de auditoria.resultado y auditoria.accion al
-- vocabulario cerrado del backend (ports.ResultadosAuditoriaValidos y
-- ports.AccionesAuditoriaValidas):
--
--   resultado: SUCCESS -> EXITO, FAILED -> FALLA, PENDING -> PENDIENTE
--   accion:    START, START_VM -> INICIAR_VM      STOP, STOP_VM -> DETENER_VM
--              SHUTDOWN -> APAGAR_VM              REBOOT -> REINICIAR_VM
--              RESUME -> REANUDAR_VM              DELETE -> ELIMINAR_VM
--              CONFIG, CONFIG_INSTANCE -> CONFIG_UPDATED
--              CREAR_SNAPSHOT, SNAPSHOT -> SNAPSHOT_CREATE
--              ROLLBACK_SNAPSHOT -> SNAPSHOT_ROLLBACK
--              DELETE_SNAPSHOT -> SNAPSHOT_DELETE
--              CAMBIAR_CONTRASENA -> PASSWORD_CHANGED
--
-- Todo es un único bloque DO, que PostgreSQL ejecuta como una sola sentencia
-- atómica: el trigger de inmutabilidad (trg_auditoria_inmutable) se desactiva
-- solo dentro de esta transacción y, si algo falla, no queda nada aplicado ni
-- el trigger apagado. Ninguna otra sesión ve el trigger desactivado.
--
-- Es idempotente: si no hay códigos viejos no modifica la tabla. La API la
-- ejecuta al arrancar (postgres.normalizarVocabularioAuditoria); también se
-- puede correr a mano:
--
--   psql "$DB_DSN" -v ON_ERROR_STOP=1 -f migrations/20261104_vocabulario_auditoria.sql
DO $$
DECLARE
	resultados INTEGER := 0;
	acciones   INTEGER := 0;
	fuera      TEXT;
BEGIN
	IF NOT EXISTS (
		SELECT 1 FROM auditoria
		WHERE resultado IN ('SUCCESS', 'FAILED', 'PENDING')
		   OR upper(accion) IN ('START', 'START_VM', 'STOP', 'STOP_VM', 'SHUTDOWN', 'REBOOT', 'RESUME',
		                        'DELETE', 'CONFIG', 'CONFIG_INSTANCE', 'CREAR_SNAPSHOT', 'SNAPSHOT',
		                        'ROLLBACK_SNAPSHOT', 'DELETE_SNAPSHOT', 'CAMBIAR_CONTRASENA')
	) THEN
		RETURN;
	END IF;

	ALTER TABLE auditoria DISABLE TRIGGER trg_auditoria_inmutable;

	UPDATE auditoria SET resultado = CASE resultado
		WHEN 'SUCCESS' THEN 'EXITO'
		WHEN 'FAILED'  THEN 'FALLA'
		WHEN 'PENDING' THEN 'PENDIENTE'
	END
	WHERE resultado IN ('SUCCESS', 'FAILED', 'PENDING');
	GET DIAGNOSTICS resultados = ROW_COUNT;

	UPDATE auditoria SET accion = CASE upper(accion)
		WHEN 'START'              THEN 'INICIAR_VM'
		WHEN 'START_VM'           THEN 'INICIAR_VM'
		WHEN 'STOP'               THEN 'DETENER_VM'
		WHEN 'STOP_VM'            THEN 'DETENER_VM'
		WHEN 'SHUTDOWN'           THEN 'APAGAR_VM'
		WHEN 'REBOOT'             THEN 'REINICIAR_VM'
		WHEN 'RESUME'             THEN 'REANUDAR_VM'
		WHEN 'DELETE'             THEN 'ELIMINAR_VM'
		WHEN 'CONFIG'             THEN 'CONFIG_UPDATED'
		WHEN 'CONFIG_INSTANCE'    THEN 'CONFIG_UPDATED'
		WHEN 'CREAR_SNAPSHOT'     THEN 'SNAPSHOT_CREATE'
		WHEN 'SNAPSHOT'           THEN 'SNAPSHOT_CREATE'
		WHEN 'ROLLBACK_SNAPSHOT'  THEN 'SNAPSHOT_ROLLBACK'
		WHEN 'DELETE_SNAPSHOT'    THEN 'SNAPSHOT_DELETE'
		WHEN 'CAMBIAR_CONTRASENA' THEN 'PASSWORD_CHANGED'
	END
	WHERE upper(accion) IN ('START', 'START_VM', 'STOP', 'STOP_VM', 'SHUTDOWN', 'REBOOT', 'RESUME',
	                        'DELETE', 'CONFIG', 'CONFIG_INSTANCE', 'CREAR_SNAPSHOT', 'SNAPSHOT',
	                        'ROLLBACK_SNAPSHOT', 'DELETE_SNAPSHOT', 'CAMBIAR_CONTRASENA');
	GET DIAGNOSTICS acciones = ROW_COUNT;

	ALTER TABLE auditoria ENABLE TRIGGER trg_auditoria_inmutable;

	IF EXISTS (SELECT 1 FROM auditoria WHERE resultado IN ('SUCCESS', 'FAILED', 'PENDING')) THEN
		RAISE EXCEPTION 'quedaron resultados SUCCESS/FAILED/PENDING en auditoria';
	END IF;

	RAISE NOTICE 'auditoria: % resultados y % acciones traducidos al vocabulario cerrado', resultados, acciones;

	-- Lo que no tiene traducción (ej. acciones de pruebas locales) se informa y
	-- se deja como está.
	SELECT string_agg(DISTINCT accion, ', ') INTO fuera FROM auditoria
	WHERE accion NOT IN ('LOGIN', 'LOGIN_FALLA', 'VERIFICAR_2FA', '2FA_FALLA', 'LOGOUT',
		'CREAR_USUARIO', 'ACTUALIZAR_USUARIO', 'ELIMINAR_USUARIO', 'ASIGNAR_PERMISOS',
		'RESETEAR_CONTRASENA', 'RESETEAR_TOTP', 'PASSWORD_CHANGED',
		'UPDATE_PROFILE_EMAIL_REQUESTED', 'UPDATE_PROFILE_EMAIL_CONFIRMED',
		'CREATE_INSTANCE', 'INICIAR_VM', 'DETENER_VM', 'APAGAR_VM', 'REINICIAR_VM',
		'REANUDAR_VM', 'ELIMINAR_VM', 'CONFIG_UPDATED',
		'SNAPSHOT_CREATE', 'SNAPSHOT_ROLLBACK', 'SNAPSHOT_DELETE',
		'INSTANCE_PERMISSIONS_PURGED', 'SYSTEM_INSTANCE_STOPPED_EXTERNALLY',
		'ALERT_TRIGGERED', 'ALERT_RESOLVED', 'EVENTOS_CONEXION', 'EVENTOS_CIERRE');
	IF fuera IS NOT NULL THEN
		RAISE NOTICE 'auditoria: acciones fuera del vocabulario sin traducción conocida: %', fuera;
	END IF;
END
$$;
