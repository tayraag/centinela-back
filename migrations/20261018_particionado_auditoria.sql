-- Ejecutar antes de reiniciar la API, en una ventana de mantenimiento:
-- psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" \
--   -v ON_ERROR_STOP=1 \
--   -f migrations/20261018_particionado_auditoria.sql
--
-- Requiere que auditoria ya exista como tabla particionada por fecha_hora.
-- Si encuentra límites antiguos (por ejemplo, creados en la zona horaria local),
-- reconstruye las particiones trimestrales conservando sus registros. Toma un
-- lock exclusivo sobre auditoria durante la operación.

BEGIN;
SET LOCAL TIME ZONE 'UTC';
LOCK TABLE auditoria IN ACCESS EXCLUSIVE MODE;

DO $migration$
DECLARE
    particion TEXT;
    oid_particion OID;
    limite_actual TEXT;
    limite_esperado TEXT;
    reconstruir BOOLEAN := FALSE;
    filas_default BIGINT;
    esquema TEXT := current_schema();
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = esquema
          AND c.relname = 'auditoria'
          AND c.relkind = 'p'
    ) THEN
        RAISE EXCEPTION 'auditoria debe existir como tabla particionada antes de aplicar esta migración';
    END IF;

    FOREACH particion IN ARRAY ARRAY[
        'auditoria_2026_q3',
        'auditoria_2026_q4',
        'auditoria_2027_q1'
    ] LOOP
        oid_particion := NULL;
        limite_actual := NULL;

        SELECT c.oid, pg_get_expr(c.relpartbound, c.oid)
        INTO oid_particion, limite_actual
        FROM pg_inherits i
        JOIN pg_class p ON p.oid = i.inhparent
        JOIN pg_namespace np ON np.oid = p.relnamespace
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE np.nspname = esquema
          AND p.relname = 'auditoria'
          AND c.relname = particion;

        IF oid_particion IS NULL THEN
            IF to_regclass(format('%I.%I', esquema, particion)) IS NOT NULL THEN
                RAISE EXCEPTION 'Existe %, pero no está adjunta a %. No se modificó ninguna tabla.',
                    particion, 'auditoria';
            END IF;
            reconstruir := TRUE;
        ELSE
            CASE particion
                WHEN 'auditoria_2026_q3' THEN
                    limite_esperado := format(
                        'FOR VALUES FROM (%L) TO (%L)',
                        '2026-07-01 00:00:00+00',
                        '2026-10-01 00:00:00+00'
                    );
                WHEN 'auditoria_2026_q4' THEN
                    limite_esperado := format(
                        'FOR VALUES FROM (%L) TO (%L)',
                        '2026-10-01 00:00:00+00',
                        '2027-01-01 00:00:00+00'
                    );
                WHEN 'auditoria_2027_q1' THEN
                    limite_esperado := format(
                        'FOR VALUES FROM (%L) TO (%L)',
                        '2027-01-01 00:00:00+00',
                        '2027-04-01 00:00:00+00'
                    );
            END CASE;

            IF limite_actual <> limite_esperado THEN
                reconstruir := TRUE;
            END IF;
        END IF;
    END LOOP;

    IF NOT reconstruir THEN
        RETURN;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM pg_inherits i
        JOIN pg_class p ON p.oid = i.inhparent
        JOIN pg_namespace np ON np.oid = p.relnamespace
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE np.nspname = esquema
          AND p.relname = 'auditoria'
          AND c.relname = 'auditoria_default'
    ) THEN
        EXECUTE format(
            'SELECT count(*) FROM %I.%I
             WHERE fecha_hora >= %L::timestamptz
               AND fecha_hora <  %L::timestamptz',
            esquema,
            'auditoria_default',
            '2026-07-01 00:00:00+00',
            '2027-04-01 00:00:00+00'
        ) INTO filas_default;

        IF filas_default > 0 THEN
            RAISE EXCEPTION
                'auditoria_default contiene % registros en los períodos que se van a particionar. No se modificó ninguna tabla; mover esos registros antes de reintentar.',
                filas_default;
        END IF;
    END IF;

    DROP TABLE IF EXISTS pg_temp.auditoria_particiones_stage;
    CREATE TEMP TABLE auditoria_particiones_stage
        ON COMMIT DROP
        AS SELECT * FROM auditoria WITH NO DATA;

    FOREACH particion IN ARRAY ARRAY[
        'auditoria_2026_q3',
        'auditoria_2026_q4',
        'auditoria_2027_q1'
    ] LOOP
        IF EXISTS (
            SELECT 1
            FROM pg_inherits i
            JOIN pg_class p ON p.oid = i.inhparent
            JOIN pg_namespace np ON np.oid = p.relnamespace
            JOIN pg_class c ON c.oid = i.inhrelid
            WHERE np.nspname = esquema
              AND p.relname = 'auditoria'
              AND c.relname = particion
        ) THEN
            EXECUTE format(
                'ALTER TABLE %I.%I DETACH PARTITION %I.%I',
                esquema, 'auditoria', esquema, particion
            );
            EXECUTE format(
                'INSERT INTO pg_temp.auditoria_particiones_stage SELECT * FROM %I.%I',
                esquema, particion
            );
            EXECUTE format('DROP TABLE %I.%I', esquema, particion);
        END IF;
    END LOOP;

    EXECUTE format(
        'CREATE TABLE %I.%I PARTITION OF %I.%I
         FOR VALUES FROM (%L) TO (%L)',
        esquema, 'auditoria_2026_q3', esquema, 'auditoria',
        '2026-07-01 00:00:00+00', '2026-10-01 00:00:00+00'
    );
    EXECUTE format(
        'CREATE TABLE %I.%I PARTITION OF %I.%I
         FOR VALUES FROM (%L) TO (%L)',
        esquema, 'auditoria_2026_q4', esquema, 'auditoria',
        '2026-10-01 00:00:00+00', '2027-01-01 00:00:00+00'
    );
    EXECUTE format(
        'CREATE TABLE %I.%I PARTITION OF %I.%I
         FOR VALUES FROM (%L) TO (%L)',
        esquema, 'auditoria_2027_q1', esquema, 'auditoria',
        '2027-01-01 00:00:00+00', '2027-04-01 00:00:00+00'
    );

    INSERT INTO auditoria SELECT * FROM pg_temp.auditoria_particiones_stage;
END
$migration$;

-- Guardas declarativas exigidas por el contrato de la migración. Normalmente
-- las particiones ya fueron verificadas o reconstruidas por el bloque anterior;
-- IF NOT EXISTS mantiene segura una reaplicación y cubre explícitamente el DDL
-- preventivo esperado por las herramientas de despliegue.
CREATE TABLE IF NOT EXISTS auditoria_2026_q3
    PARTITION OF auditoria
    FOR VALUES FROM ('2026-07-01 00:00:00+00')
                 TO ('2026-10-01 00:00:00+00');

CREATE TABLE IF NOT EXISTS auditoria_2026_q4
    PARTITION OF auditoria
    FOR VALUES FROM ('2026-10-01 00:00:00+00')
                 TO ('2027-01-01 00:00:00+00');

CREATE TABLE IF NOT EXISTS auditoria_2027_q1
    PARTITION OF auditoria
    FOR VALUES FROM ('2027-01-01 00:00:00+00')
                 TO ('2027-04-01 00:00:00+00');

CREATE INDEX IF NOT EXISTS idx_auditoria_query
    ON auditoria (fecha_hora DESC, usuario_id, accion);

COMMIT;
