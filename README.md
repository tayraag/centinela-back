# El Centinela - Backend

Este es el repositorio del backend del proyecto **El Centinela**. Su rol principal es actuar de escudo, traductor y orquestador entre el frontend y la infraestructura de Proxmox.

## Arquitectura

El proyecto sigue una **Arquitectura Hexagonal (Puertos y Adaptadores)** adaptada a las convenciones de Go.

### Capas principales

| Capa                        | Carpeta                        | Responsabilidad                                              |
| --------------------------- | ------------------------------ | ------------------------------------------------------------ |
| **Core / Dominio**          | `internal/core/`               | Lógica de negocio pura, sin dependencias externas            |
| **Adaptadores primarios**   | `internal/adapters/primary/`   | Reciben peticiones del exterior (HTTP handlers, middlewares) |
| **Adaptadores secundarios** | `internal/adapters/secondary/` | Se conectan hacia afuera (PostgreSQL, Redis, Proxmox API)    |
| **Infraestructura**         | `internal/infrastructure/`     | Utilidades transversales (JWT, bcrypt, TOTP)                 |

### Flujo de una petición

```
     [HTTP Request]
          │
          ▼
┌─────────────────────┐
│   Middleware Chain  │  RequireAuth → RequireRole → RequireInstanceAccess → Logger
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│   Primary Adapter   │  auth_handler / user_handler / account_handler
│   (HTTP Handler)    │  Parsea el body → llama al Service (interface)
└──────────┬──────────┘
           │  llama a interface (Port)
           ▼
┌─────────────────────┐
│   Core / Domain     │  auth_service / user_service
│  (Services + Ports) │  Lógica de negocio pura, sin frameworks
└────┬──────────┬─────┘
     │          │
     │ repo     │ crypto
     ▼          ▼
┌─────────┐  ┌──────────────────┐
│Secondary│  │  Infrastructure  │
│ Adapter │  │  crypto/jwt.go   │
│(GORM)   │  │  crypto/password │
└─────────┘  │  crypto/totp     │
     │       └──────────────────┘
     ▼
[PostgreSQL DB]
```

> **Regla de oro**: `core/` no importa nada de `adapters/` ni `infrastructure/`. La dependencia siempre va hacia adentro.

## Estructura de Carpetas

```text
centinela-back/
├── cmd/
│   ├── api/
│   │   └── main.go              # Punto de entrada: wiring, router, arranque HTTP
│   ├── proxmox-simulador/       # Simulador de Proxmox para desarrollar sin red (docs/simulador-proxmox.md)
│   └── seed/                    # Scripts de seed de datos iniciales
│
├── docs/                        # Archivos generados por Swaggo (NO editar a mano)
│
├── internal/
│   ├── core/                    # ★ NÚCLEO DE DOMINIO (sin dependencias externas)
│   │   ├── domain/
│   │   │   ├── models.go        # Entidades: Usuario, SesionActiva, PermisoInstancia, etc.
│   │   │   └── metricas.go      # DTOs de telemetría y métricas (PuntoMetricaDTO, MetricasEnVivoDTO)
│   │   ├── ports/
│   │   │   ├── auth_port.go     # Interfaces AuthRepository + AuthService + DTOs de auth
│   │   │   ├── user_port.go     # Interfaces UserRepository + UserService + DTOs de usuarios
│   │   │   ├── instance_port.go # Interface InstanceRepository (autorización por recurso)
│   │   │   ├── proxmox_port.go  # Interface ProxmoxPort (comunicación con hipervisor)
│   │   │   ├── event_port.go    # Interface EventBus y contrato RealtimeEvent
│   │   │   └── audit_port.go    # Interface AuditRepository + AuditService
│   │   └── services/
│   │       ├── auth_service.go             # Lógica: login, 2FA, tokens, refresh, logout
│   │       ├── user_service.go             # Lógica: CRUD usuarios, permisos, auditoría, perfil
│   │       ├── nodo_service.go             # Consolidación de estado, storages y plantillas del nodo
│   │       ├── inventario_service.go       # Inventario de VMs/CTs filtrado por rol y permisos
│   │       ├── seguimiento_tareas.go       # Worker pool concurrente de sondeo de UPID y push de TASK_FINISHED
│   │       ├── aprovisionamiento_service.go# Creación coordinada de VMs/CTs con reserva de cuotas
│   │       ├── audit_service.go            # Registro y consulta de auditoría particionada
│   │       └── eventos_service.go          # Despacho de eventos en tiempo real hacia Pub/Sub
│   │
│   ├── adapters/
│   │   ├── primary/             # Adaptadores de ENTRADA (quien llama al core)
│   │   │   └── http/
│   │   │       ├── auth_handler.go       # Endpoints de autenticación (/api/auth/*) y ws-ticket
│   │   │       ├── user_handler.go       # Endpoints de gestión de usuarios ADMIN (/api/admin/users/*)
│   │   │       ├── node_handler.go       # Endpoints de telemetría, storages y templates del nodo (/api/node/*)
│   │   │       ├── instance_handler.go   # Endpoints de inventario, ciclo de vida y telemetría RRD (/api/instances/*)
│   │   │       ├── ws_metrics_handler.go # Canal WebSocket de telemetría en vivo (/api/ws/instances/:vmid/metrics)
│   │   │       ├── events_handler.go     # Canal de eventos SSE (/api/events)
│   │   │       ├── account_handler.go    # Endpoints de perfil propio (/api/account/*)
│   │   │       ├── audit_handler.go      # Endpoints de auditoría global (/api/admin/audit)
│   │   │       ├── responses.go          # Helpers de respuestas HTTP estándar
│   │   │       └── middleware/
│   │   │           ├── auth_middleware.go    # RequireAuth, RequirePreAuth, RequireRole
│   │   │           ├── instance_guard.go     # RequireInstanceAccess (guard de VMID)
│   │   │           ├── protected_instance.go # RejectProtectedInstance (VMIDs de infraestructura)
│   │   │           └── logger_middleware.go  # Logger de peticiones HTTP
│   │   │
│   │   └── secondary/           # Adaptadores de SALIDA (implementan puertos del core)
│   │       ├── postgres/
│   │       │   ├── db.go                # InitDB(), AutoMigrate, particionamiento trimestral
│   │       │   ├── auth_repository.go   # Implementa AuthRepository
│   │       │   ├── user_repository.go   # Implementa UserRepository
│   │       │   ├── instance_repository.go # Implementa InstanceRepository (VerificarAcceso)
│   │       │   └── audit_repository.go  # Implementa AuditRepository sobre tabla particionada
│   │       ├── redis/                   # Implementa KeyValueStore sobre Redis (clave-valor + Pub/Sub), ver docs/redis.md
│   │       ├── memoria/                 # Implementa KeyValueStore en memoria (modo degradado si no hay Redis)
│   │       ├── email/                   # Proveedor de correo (SMTP o Mock)
│   │       ├── kvtest/                  # Test de contrato común a redis/ y memoria/
│   │       └── proxmox/                 # Adapter Proxmox VE (API Token, RRDdata, /status/current), ver su README.md
│   │           ├── client.go            # Implementa ProxmoxPort
│   │           └── tls.go               # Verificación TLS (fingerprint / skip verify)
│   │
│   └── infrastructure/          # Utilidades técnicas transversales
│       └── crypto/
│           ├── jwt.go           # FirmarToken / VerificarToken (HS256)
│           ├── password.go      # Hash/verify bcrypt
│           └── totp.go          # QR TOTP, cifrado AES del secreto
│
├── scripts/                     # Scripts de utilidad
├── docker-compose.yml
├── go.mod                       # Gestor de dependencias
└── go.sum                       # Checksums de dependencias
```

## Requisitos Previos

Para contribuir o ejecutar este proyecto necesitas tener instaladas las siguientes herramientas:

### 1. Git

Necesario para clonar el repositorio y gestionar versiones de código.

- Comprueba si lo tienes instalado:
  ```bash
  git --version
  ```
- Si no lo tienes, descárgalo e instálalo desde [git-scm.com](https://git-scm.com/).

### 2. Go (Golang)

El backend está desarrollado en Go (se recomienda tener **Go 1.22 o superior**).

#### Instalación según tu Sistema Operativo:

- **Windows:**
  - **Opción recomendada:** Descarga el instalador oficial `.msi` desde [go.dev/dl](https://go.dev/dl/) y sigue el asistente de instalación.
  - **Vía terminal (PowerShell):**
    ```powershell
    winget install GoLang.Go
    ```
  - _Nota:_ Una vez instalado, reinicia tu terminal (o PowerShell / VS Code) para que reconozca el comando `go` en el PATH del sistema.

- **macOS:**
  - **Opción recomendada:** Descarga el instalador `.pkg` desde [go.dev/dl](https://go.dev/dl/).
  - **Vía Homebrew:**
    ```bash
    brew install go
    ```

- **Linux (Ubuntu / Debian):**
  - Vía gestor de paquetes:
    ```bash
    sudo apt update
    sudo apt install golang-go
    ```
  - O instala la versión más reciente siguiendo las [instrucciones oficiales de Go para Linux](https://go.dev/doc/install).

#### Verificación de la instalación:

Abre una terminal nueva y ejecuta:

```bash
go version
```

Deberías ver una salida similar a:

```text
go version go1.22.x windows/amd64  # (o linux/darwin según tu SO)
```

---

## Puesta en Marcha (Nueva Máquina)

Con Git y Go instalados, sigue estos pasos para poner a correr el proyecto:

### 1. Clonar el repositorio

Abre tu terminal y clona el proyecto en la carpeta que desees:

```bash
git clone https://github.com/tayraag/centinela-back.git
cd centinela-back
```

### 2. Descargar módulos y dependencias

Descarga las dependencias declaradas en el proyecto:

```bash
go mod download
```

> **Tip:** Cuando se agreguen librerías externas o se limpien paquetes no utilizados, se utiliza el comando:
>
> ```bash
> go mod tidy
> ```

### 3. Correr el proyecto

Ejecuta el punto de entrada principal del backend:

```bash
go run ./cmd/api
```

Si todo está en orden, verás los logs de inicialización del servidor:

```text
22:00:00 🚀 Iniciando El Centinela Backend...
22:00:00 ✅ Variables de entorno cargadas desde el archivo .env
22:00:00 ✅ Conexión exitosa a PostgreSQL
22:00:00 🔄 Ejecutando auto-migración de tablas...
22:00:01 ✅ Migración completada exitosamente
22:00:01 [INFO] Conexión con Redis establecida exitosamente.
22:00:01 🔁 Seguimiento de tareas iniciado: 8 workers
22:00:01 📖 Swagger UI disponible en: http://localhost:8080/swagger/index.html
22:00:01 [INFO] Servidor HTTP escuchando en :8080
```
