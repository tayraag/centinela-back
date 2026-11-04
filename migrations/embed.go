// Package migrations expone los scripts SQL de esta carpeta para que la API
// pueda aplicar los que son idempotentes al arrancar (ver
// postgres.normalizarVocabularioAuditoria). Los demás se corren a mano (SETUP.md).
package migrations

import _ "embed"

// VocabularioAuditoria normaliza los códigos viejos de auditoria.accion y
// auditoria.resultado al vocabulario cerrado (RF-08).
//
//go:embed 20261104_vocabulario_auditoria.sql
var VocabularioAuditoria string
