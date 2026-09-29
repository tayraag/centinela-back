// Package main levanta un simulador de Proxmox VE para desarrollar sin acceso
// al Proxmox real (sin Tailscale, sin VPN, sin depender de permisos del token).
//
// Imita la API REST de Proxmox (/api2/json/...) usando como referencia las
// respuestas reales capturadas en "API proxmox respuestas/": mismo formato
// {"data": ...}, mismos campos, mismos UPID y los mismos códigos de error.
// El estado (encendidas/apagadas, snapshots, instancias creadas) vive en
// memoria y vuelve al inventario inicial cada vez que se reinicia.
//
// Uso:
//
//	go run ./cmd/proxmox-simulador
//
// y en el .env del backend: PROXMOX_URL=http://localhost:8081/api2/json
//
// Documentación completa: docs/simulador-proxmox.md
package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()
	log.SetFlags(log.Ltime)

	cfg := Config{
		Nodo:          valorODefecto(os.Getenv("PROXMOX_NODE"), "proxmox"),
		TokenID:       os.Getenv("PROXMOX_TOKEN_ID"),
		TokenSecret:   os.Getenv("PROXMOX_TOKEN_SECRET"),
		DuracionTarea: time.Duration(entero(os.Getenv("PROXMOX_SIM_DURACION_TAREA"), 3)) * time.Second,
		Falla:         os.Getenv("PROXMOX_SIM_FALLA"),
	}
	if cfg.TokenID == "" || cfg.TokenSecret == "" {
		log.Fatal("❌ Definí PROXMOX_TOKEN_ID y PROXMOX_TOKEN_SECRET en el .env: el simulador los exige igual que el Proxmox real")
	}
	if cfg.Falla != "" && !fallasValidas[cfg.Falla] {
		log.Fatalf("❌ PROXMOX_SIM_FALLA=%q no es válida (opciones: caido, token, lento)", cfg.Falla)
	}
	puerto := valorODefecto(os.Getenv("PROXMOX_SIM_PORT"), "8081")

	sim := NuevoSimulador(cfg)
	log.Printf("🧪 Simulador de Proxmox VE escuchando en http://localhost:%s/api2/json", puerto)
	log.Printf("   Nodo %q · token %s · tareas de %s · %d instancias", cfg.Nodo, cfg.TokenID, cfg.DuracionTarea, len(sim.instancias))
	if cfg.Falla != "" {
		log.Printf("   ⚠️  Modo falla activo: %s", cfg.Falla)
	}
	if err := http.ListenAndServe(":"+puerto, sim); err != nil {
		log.Fatalf("❌ %v", err)
	}
}

func valorODefecto(v, defecto string) string {
	if v == "" {
		return defecto
	}
	return v
}

func entero(v string, defecto int) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return defecto
	}
	return n
}
