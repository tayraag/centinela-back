package services

import (
	"encoding/json"
	"strings"
	"testing"

	"el-centinela/internal/core/ports"
)

// El RRD del nodo no siempre trae los totales: cuando falta maxmem o maxdisk el
// punto no puede normalizar RAM ni almacenamiento. Publicar 0% hacía que el
// Dashboard dibujara una caída al piso y que el gráfico pareciera volver a cero.
func TestNormalizarHistorialNodo_OmiteMetricasSinTotal(t *testing.T) {
	serie := normalizarHistorialNodo([]ports.MuestraNodoRRD{
		{
			Timestamp:  100,
			CPU:        0.10,
			MemUsada:   3350000000,
			MemTotal:   15300000000,
			DiscoUsado: 10730000000,
			DiscoTotal: 67730000000,
		},
		{Timestamp: 160, CPU: 0.20},
	})

	if len(serie) != 2 {
		t.Fatalf("se esperaban 2 puntos, llegaron %d", len(serie))
	}

	if serie[0].RAMPercent == nil || serie[0].StoragePercent == nil {
		t.Fatalf("el punto con totales debe traer RAM y almacenamiento: %+v", serie[0])
	}

	if *serie[0].RAMPercent <= 0 || *serie[0].StoragePercent <= 0 {
		t.Errorf("porcentajes inesperados en el punto con totales: %+v", serie[0])
	}

	if serie[1].RAMPercent != nil {
		t.Errorf("sin maxmem no hay RAM que publicar: %v", *serie[1].RAMPercent)
	}

	if serie[1].StoragePercent != nil {
		t.Errorf("sin maxdisk no hay almacenamiento que publicar: %v", *serie[1].StoragePercent)
	}

	if serie[1].CPUPercent <= 0 {
		t.Errorf("el CPU del punto sin totales se conserva: %+v", serie[1])
	}

	crudo, err := json.Marshal(serie[1])
	if err != nil {
		t.Fatalf("no se pudo serializar el punto: %v", err)
	}

	for _, clave := range []string{"ramPercent", "storagePercent"} {
		if strings.Contains(string(crudo), clave) {
			t.Errorf("el JSON no debe publicar %s cuando falta el total: %s", clave, crudo)
		}
	}
}
