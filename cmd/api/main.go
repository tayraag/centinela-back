package main

import (
	"log"
	"el-centinela/internal/adapters/secondary/postgres"
)

func main() {
	log.Println("¡El Centinela está en línea!")

	// 1. Inicializar la conexión a la base de datos
	db, err := postgres.InitDB()
	if err != nil {
		log.Fatalf("❌ Error fatal al iniciar la base de datos: %v", err)
	}

	// Esto es solo para evitar que Go se queje de que no usamos la variable "db" por ahora
	_ = db

	log.Println("🛡️ Sistema inicializado correctamente. Esperando conexiones...")
	
	// (Más adelante, aquí arrancaremos el servidor web HTTP)
}