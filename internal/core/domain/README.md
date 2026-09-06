# Carpeta: /internal/core/domain

El corazón del sistema. Aquí viven las **Entidades del negocio** (ej. User, Instance, AuditLog)[cite: 1].

Estos archivos contienen los *Structs* principales y las reglas de negocio puras. Al ser el centro del hexágono, este paquete no depende de absolutamente nada externo. 

**Regla estricta:** No importar frameworks HTTP, ni librerías de bases de datos externas aquí. Todo el código debe ser Go puro.