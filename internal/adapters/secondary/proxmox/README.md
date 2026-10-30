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
PROXMOX_NODE=proxmox                               # vacío = se descubre en GET /nodes
PROXMOX_TARGET_PVE_VERSION=auto                    # auto | 8.4.21 | 9.2.2
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
| `GET /nodes`                                          | Al arrancar, descubrir el nodo si falta `PROXMOX_NODE` (`ListarNodos`) |
| `GET /version`                                        | Al arrancar, informar la versión de Proxmox VE y contrastarla con `PROXMOX_TARGET_PVE_VERSION` (`VersionPVE`). Acepta `version` `"9.2.2"` con `release` `"9.2"`, y `version` `"8.4"` con `release` `"21"` (→ `8.4.21`) |
| `GET /cluster/resources`                              | Inventario; se conservan solo `type=qemu` y `type=lxc`. También resuelve el nodo y tipo real de cada vmid |
| `POST /nodes/{node}/{qemu\|lxc}/{vmid}/status/start`  | Iniciar instancia (devuelve UPID)                               |
| `POST /nodes/{node}/{qemu\|lxc}/{vmid}/status/stop`   | Apagado forzado (devuelve UPID)                                 |

El cliente no tiene un nodo fijo. Las rutas de una instancia usan el nodo que informa `cluster/resources`; las del hipervisor (`/nodes/{node}/status`, `rrddata`, `storage`, creación de instancias) usan el nodo resuelto al arrancar (`PROXMOX_NODE` o el descubierto), que `cmd/api/main.go` inyecta en los servicios. No hay nombres de nodo escritos en el código.

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

Las instancias de la infraestructura del propio El Centinela se listan en `PROXMOX_PROTECTED_VMIDS` (ej. `200,201,202`), porque cambian según el servidor. Las acciones destructivas sobre ellas responden `403 INSTANCE_PROTECTED`, para todos los roles (middleware `RejectProtectedInstance`). Vacía = ninguna protegida: no hay VMIDs fijos en el código.
