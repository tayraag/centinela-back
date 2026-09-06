# Carpeta: /cmd/api

Este es el punto de entrada de la aplicación[cite: 1]. 

Aquí reside el archivo `main.go`. Su única responsabilidad es actuar como el gran orquestador (Dependency Injection):
1. Leer variables de entorno y configuración.
2. Inicializar las conexiones a la base de datos y clientes externos.
3. Conectar ("inyectar") los adaptadores de infraestructura con los servicios del núcleo.
4. Arrancar el servidor web.

**Regla estricta:** No debe haber lógica de negocio aquí.