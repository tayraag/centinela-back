# Carpeta: /internal/adapters/secondary/proxmox

Este es un **Adaptador de Salida (Secondary Adapter)**.

Contiene el cliente HTTP específico que habla directamente con la API de Proxmox[cite: 1]. Su responsabilidad es implementar la interfaz `ProxmoxPort` definida en el núcleo. Se encarga de inyectar tokens, manejar cabeceras HTTP, certificados y mapear el JSON "sucio" de Proxmox a entidades puras de nuestro dominio.