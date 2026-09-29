# cmd/proxmox-simulador

Simulador de la API de Proxmox VE para desarrollar sin acceso al Proxmox real.

```bash
go run ./cmd/proxmox-simulador      # escucha en http://localhost:8081/api2/json
```

Y en el `.env`: `PROXMOX_URL=http://localhost:8081/api2/json`.

Qué imita, cómo se comporta, modos de falla y cómo extenderlo: **[docs/simulador-proxmox.md](../../docs/simulador-proxmox.md)**.

Las respuestas de referencia están en [`API proxmox respuestas/`](../../API%20proxmox%20respuestas/).
