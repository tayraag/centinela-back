# Carpeta: /internal/adapters/secondary/proxmox

Este es un **Adaptador de Salida (Secondary Adapter)**.

Contiene el cliente HTTP específico que habla directamente con la API de Proxmox. Su responsabilidad es implementar la interfaz `ProxmoxPort` definida en el núcleo. Se encarga de inyectar tokens, manejar cabeceras HTTP, certificados y mapear el JSON "sucio" de Proxmox a entidades puras de nuestro dominio.

## Archivos

| Archivo                       | Contenido                                                                 |
| ----------------------------- | ------------------------------------------------------------------------- |
| `client.go`                   | `Client`: implementación de `ports.ProxmoxPort` con `net/http`            |
| `tls.go`                      | `BuildTLSConfig()`: fingerprint pinning o `InsecureSkipVerify` según env  |
| `client_test.go`              | Tests contra un Proxmox simulado (`httptest`) que exige el API Token      |
| `client_integration_test.go`  | Test contra el Proxmox real; se saltea si no hay secreto real en `.env`   |

## Conexión

Configuración oficial (pasada por la PM). La tabla completa de variables está en `SETUP.md` → *Proxmox VE*.

```
PROXMOX_URL=https://10.10.20.1:8006/api2/json      # servidor (red interna vmbr1)
# PROXMOX_URL=http://localhost:8081/api2/json      # local, con go run ./cmd/proxmox-simulador
PROXMOX_NODE=proxmox
PROXMOX_TOKEN_ID=centinela-api@pve!backend-token
PROXMOX_TOKEN_SECRET=<pedírselo a la PM / infra, nunca commitearlo>
PROXMOX_INSECURE_SKIP_VERIFY=true
```

Autenticación: **solo API Token** del usuario de servicio `centinela-api@pve` (mínimo privilegio), enviado en cada request como

```
Authorization: PVEAPIToken=centinela-api@pve!backend-token=<PROXMOX_TOKEN_SECRET>
```

Si falta el token, el cliente no llama a Proxmox y devuelve `ErrProxmoxCredenciales`.

## Desarrollo local sin red

`cmd/proxmox-simulador` es un simulador de Proxmox que responde igual que el real (a partir de las capturas de `API proxmox respuestas/`) y exige el mismo token del `.env`. `simulador_test.go` incluye tests de contrato que corren este cliente contra el simulador. Ver [docs/simulador-proxmox.md](../../../../docs/simulador-proxmox.md).

## Endpoints de Proxmox que se usan

| Endpoint                                              | Para qué                                                        |
| ----------------------------------------------------- | --------------------------------------------------------------- |
| `GET /cluster/resources`                              | Inventario; se conservan solo `type=qemu` y `type=lxc`. También resuelve el nodo y tipo real de cada vmid |
| `POST /nodes/{node}/{qemu\|lxc}/{vmid}/status/start`  | Iniciar instancia (devuelve UPID)                               |
| `POST /nodes/{node}/{qemu\|lxc}/{vmid}/status/stop`   | Apagado forzado (devuelve UPID)                                 |

`PROXMOX_NODE` no se usa para armar rutas: el nodo de cada instancia sale de `cluster/resources`.

## Errores

| Situación                                  | Error del puerto                                      | HTTP hacia el cliente         |
| ------------------------------------------ | ----------------------------------------------------- | ----------------------------- |
| Token rechazado (401/403) o sin configurar | `ErrProxmoxCredenciales` + `ErrProxmoxNoDisponible`   | `502 PROXMOX_UNAVAILABLE`     |
| Proxmox no responde a tiempo               | `ErrProxmoxTimeout` + `ErrProxmoxNoDisponible`        | `504 PROXMOX_TIMEOUT`         |
| Red, otros 5xx o 404 (URL mal escrita)     | `ErrProxmoxNoDisponible`                              | `502 PROXMOX_UNAVAILABLE`     |
| vmid inexistente en `cluster/resources`    | `ErrInstanciaNoEncontrada`                            | `404 INSTANCE_NOT_FOUND`      |
| 500 con `can't lock file` o `is locked` (instancia ocupada con otra tarea) | `ErrInstanciaOcupada` (no envuelve `ErrProxmoxNoDisponible`) | `409 INSTANCE_BUSY` |

El detalle nunca llega al cliente, pero `mapearErrorProxmox` lo registra en el log del servidor.

## VMIDs protegidos

Las instancias `100`–`105` son la infraestructura del propio El Centinela. `POST /api/instances/:vmid/stop` responde `403 INSTANCE_PROTECTED` para ellas, para todos los roles (middleware `RejectProtectedInstance`). La lista se cambia con `PROXMOX_PROTECTED_VMIDS`.
