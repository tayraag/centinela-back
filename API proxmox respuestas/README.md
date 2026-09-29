# Respuestas reales de la API de Proxmox

Capturas de lo que respondió el Proxmox real cuando le pegamos desde Postman. Son la **referencia** del [simulador de Proxmox](../docs/simulador-proxmox.md): el simulador arranca con este mismo inventario y responde con estos mismos campos.

| Archivo | Qué contiene |
| ------- | ------------ |
| `data extraida de las peticiones.md` | RF-02 a RF-11: inventario (`cluster/resources`), estado del nodo, acciones de energía, tareas (UPID), `status/current`, snapshots, creación de VMs/LXC y edición de config. |
| `rf05- vm- rrddata.md` | Métricas (`rrddata?timeframe=hour`) de una VM: 60 puntos, uno por minuto. Los últimos 11 son de cuando estaba apagada y solo traen `time`, `maxcpu`, `maxmem`, `maxdisk` y `disk`. |
| `rf05- lxc- rrddata.md` | Métricas de un contenedor LXC: 60 puntos, uno por minuto, todos encendido. |

Las capturas se hicieron con un token de prueba (`root@pam!pruebalucas`) y cuando el nodo se llamaba `pve`. La configuración que usa el backend está en `SETUP.md` → *Proxmox VE*.

**Al agregar una captura nueva:** nunca pegues el secreto del token ni la cabecera `Authorization` completa. Usá `<PROXMOX_TOKEN_SECRET>` en su lugar.
