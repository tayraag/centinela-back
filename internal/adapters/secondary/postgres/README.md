# Carpeta: /internal/adapters/secondary/postgres

Este es un **Adaptador de Salida (Secondary Adapter)** dedicado a la persistencia.

Aquí se implementan los repositorios de base de datos usando un ORM (como GORM o pgx)[cite: 1]. Su trabajo es implementar las interfaces definidas en `core/ports` (ej. `UserRepository`). Convierte las instrucciones puras del sistema en consultas SQL reales.