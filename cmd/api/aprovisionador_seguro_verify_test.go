package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	apihttp "el-centinela/internal/adapters/primary/http"
	"el-centinela/internal/adapters/secondary/memoria"
	"el-centinela/internal/core/domain/topology"

	"github.com/gin-gonic/gin"
)

func TestVerifyP4_FallbackMemoriaDevuelve503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	provider := aprovisionadorSeguro(memoria.Nuevo(), nil)
	if provider == nil {
		t.Fatal("aprovisionadorSeguro no debe devolver nil")
	}
	topologia, err := topology.NuevoCatalogo(nil, nil, "secreto-de-prueba")
	if err != nil {
		t.Fatal(err)
	}
	h := apihttp.NewInstanceHandler(nil, nil, nil, nil, nil, nil, nil, topologia, provider)
	router := gin.New()
	router.POST("/api/instances", h.CrearInstancia)
	body := `{"type":"VM","node":"pve1","name":"web","cpu":{"cores":1},` +
		`"ram":{"memoryMb":512},"disk":{"storageId":"local-lvm","sizeGb":1},` +
		`"network":{"bridge":"vmbr0","ipv4":{"type":"dhcp"}},` +
		`"os":{"templateId":"local:iso/debian.iso"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/instances", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503; body=%s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("PROVISIONING_COORDINATION_UNAVAILABLE")) {
		t.Fatalf("falta el código de error en el cuerpo: %s", w.Body.String())
	}
}
