-- Habilitar extensión para generar bytes aleatorios
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- Función para generar UUIDv7 nativamente en PostgreSQL
CREATE OR REPLACE FUNCTION uuid_generate_v7()
RETURNS uuid AS $$
DECLARE
  unix_time_ms bytea;
  uuid_bytes bytea;
BEGIN
  unix_time_ms := substring(decode(lpad(to_hex(floor(extract(epoch from clock_timestamp()) * 1000)::bigint), 16, '0'), 'hex') from 3 for 6);
  uuid_bytes := unix_time_ms || gen_random_bytes(10);
  uuid_bytes := set_byte(uuid_bytes, 6, (get_byte(uuid_bytes, 6) & 15) | 112);
  uuid_bytes := set_byte(uuid_bytes, 8, (get_byte(uuid_bytes, 8) & 63) | 128);
  RETURN encode(uuid_bytes, 'hex')::uuid;
END;
$$ LANGUAGE plpgsql VOLATILE;