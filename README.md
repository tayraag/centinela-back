# El Centinela - Backend

Este es el repositorio del backend del proyecto **El Centinela**. Su rol principal es actuar de escudo, traductor y orquestador entre el frontend y la infraestructura de Proxmox.

## Arquitectura

El proyecto sigue una **Arquitectura Hexagonal (Puertos y Adaptadores)** adaptada a las convenciones de Go.

### Capas principales

| Capa                        | Carpeta                        | Responsabilidad                                              |
| --------------------------- | ------------------------------ | ------------------------------------------------------------ |
| **Core / Dominio**          | `internal/core/`               | Lógica de negocio pura, sin dependencias externas            |
| **Adaptadores primarios**   | `internal/adapters/primary/`   | Reciben peticiones del exterior (HTTP handlers, middlewares) |
| **Adaptadores secundarios** | `internal/adapters/secondary/` | Se conectan hacia afuera (PostgreSQL, Proxmox API)           |
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
│   └── seed/                    # Scripts de seed de datos iniciales
│
├── docs/                        # Archivos generados por Swaggo (NO editar a mano)
│
├── internal/
│   ├── core/                    # ★ NÚCLEO DE DOMINIO (sin dependencias externas)
│   │   ├── domain/
│   │   │   └── models.go        # Entidades: Usuario, SesionActiva, PermisoInstancia, etc.
│   │   ├── ports/
│   │   │   ├── auth_port.go     # Interfaces AuthRepository + AuthService + DTOs de auth
│   │   │   ├── user_port.go     # Interfaces UserRepository + UserService + DTOs de usuarios
│   │   │   └── instance_port.go # Interface InstanceRepository (autorización por recurso)
│   │   └── services/
│   │       ├── auth_service.go  # Lógica: login, 2FA, tokens, refresh, logout
│   │       └── user_service.go  # Lógica: CRUD usuarios, permisos, auditoría, perfil
│   │
│   ├── adapters/
│   │   ├── primary/             # Adaptadores de ENTRADA (quien llama al core)
│   │   │   └── http/
│   │   │       ├── auth_handler.go       # Endpoints de autenticación
│   │   │       ├── user_handler.go       # Endpoints de gestión de usuarios (ADMIN)
│   │   │       ├── account_handler.go    # Endpoints de perfil propio
│   │   │       ├── responses.go          # Helpers de respuestas HTTP
│   │   │       └── middleware/
│   │   │           ├── auth_middleware.go    # RequireAuth, RequirePreAuth, RequireRole
│   │   │           ├── instance_guard.go     # RequireInstanceAccess (guard de VMID)
│   │   │           └── logger_middleware.go  # Logger de peticiones HTTP
│   │   │
│   │   └── secondary/           # Adaptadores de SALIDA (implementan puertos del core)
│   │       ├── postgres/
│   │       │   ├── db.go                # InitDB(), AutoMigrate
│   │       │   ├── auth_repository.go   # Implementa AuthRepository
│   │       │   ├── user_repository.go   # Implementa UserRepository
│   │       │   └── instance_repository.go # Implementa InstanceRepository (VerificarAcceso)
│   │       └── proxmox/                 # Adapter Proxmox (pendiente de implementar)
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
go run cmd/api/main.go
```

Si todo está en orden, verás el mensaje de confirmación en tu consola:

```text
¡El Centinela está en línea!
```
