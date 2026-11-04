package migrations_test

import (
	"regexp"
	"strings"
	"testing"

	"el-centinela/internal/core/ports"
	"el-centinela/migrations"
)

// El script repite el vocabulario cerrado (para informar lo que queda afuera):
// tiene que coincidir con ports.AccionesAuditoriaValidas, y toda traducción
// tiene que llevar a un valor del contrato.
func TestVocabularioAuditoria_CoincideConElContrato(t *testing.T) {
	sql := migrations.VocabularioAuditoria
	bloque := sql[strings.Index(sql, "WHERE accion NOT IN ("):]
	bloque = bloque[:strings.Index(bloque, ");")]
	literales := regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(bloque, -1)
	var enScript []string
	for _, l := range literales {
		enScript = append(enScript, l[1])
	}
	if strings.Join(enScript, ",") != strings.Join(ports.AccionesAuditoriaValidas(), ",") {
		t.Errorf("la lista del script no coincide con ports.AccionesAuditoriaValidas():\nscript: %v\nports:  %v", enScript, ports.AccionesAuditoriaValidas())
	}
	for _, m := range regexp.MustCompile(`THEN '([^']+)'`).FindAllStringSubmatch(sql, -1) {
		if !ports.EsAccionAuditoriaValida(m[1]) && !ports.EsResultadoAuditoriaValido(m[1]) {
			t.Errorf("la traducción a %q no pertenece al vocabulario cerrado", m[1])
		}
	}
}
