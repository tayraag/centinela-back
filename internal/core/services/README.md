# Carpeta: /internal/core/services

Aquí viven los **Casos de Uso** (ej. `CreateInstanceService`)[cite: 1]. 

Un servicio orquesta la lógica de negocio pura: recibe órdenes a través de los puertos de entrada, aplica las reglas del dominio y se comunica con bases de datos o APIs externas utilizando los puertos de salida.

**Regla estricta:** El código aquí no sabe si está respondiendo a una petición HTTP, a un WebSocket o a un comando de terminal. Tampoco sabe si la base de datos es PostgreSQL o MongoDB.