# El Centinela - Backend

Este es el repositorio del backend del proyecto **El Centinela**. Su rol principal es actuar de escudo, traductor y orquestador entre el frontend y la infraestructura de Proxmox.

## Arquitectura

El proyecto sigue una **Arquitectura Hexagonal (Puertos y Adaptadores)** adaptada a las convenciones de Go:

- **Núcleo (`internal/core`)**: Contiene el dominio (entidades) y los casos de uso (servicios). Todo el código aquí es "puro" y no depende de tecnologías externas.
- **Adaptadores (`internal/adapters`)**: Comunicación con el exterior (Controladores HTTP, Cliente de Proxmox, Base de datos PostgreSQL).

## Estructura de Carpetas

```text
el-centinela/
├── cmd/
│   └── api/                 # Punto de entrada (main.go)
├── internal/
│   ├── core/                # Lógica pura del negocio
│   │   ├── domain/          # Entidades (User, Instance, etc.)
│   │   ├── ports/           # Interfaces de entrada y salida
│   │   └── services/        # Casos de uso
│   └── adapters/            # Detalles de infraestructura
│       ├── primary/         # REST API, WebSockets
│       └── secondary/       # PostgreSQL, Proxmox Client
├── pkg/                     # Utilidades transversales (JWT, Hashing)
├── go.mod                   # Gestor de dependencias
└── go.sum                   # Checksums de dependencias
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

#### Opción A: Run en VS Code, sin PostgreSQL

Selecciona **Centinela API (memoria)** en **Run and Debug** y presiona Run. Este modo crea un usuario demo en memoria y muestra los eventos de autenticación en la terminal.

Credenciales demo:

- Email: `demo@centinela.local`
- Contraseña: `Centinela123!`

Los datos se borran al detener el proceso. Es un modo de prueba y no debe usarse en producción.

#### Opción B: PowerShell

Ejecuta el punto de entrada principal del backend:

```powershell
$env:AUTH_MODE="memory"
$env:JWT_SECRET="development-only-jwt-secret-change-me-32"
$env:TOTP_ENCRYPTION_KEY="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
go run cmd/api/main.go
```

Si todo está en orden, verás el mensaje de confirmación en tu consola:

```text
¡El Centinela está en línea!
```

## Autenticación

La API implementa login por email, contraseña y 2FA TOTP obligatorio. Los tokens se devuelven en JSON.

| Método | Ruta               | Propósito                                                    |
| ------ | ------------------ | ------------------------------------------------------------ |
| `POST` | `/auth/login`      | Valida credenciales y crea un desafío temporal.              |
| `POST` | `/auth/2fa/setup`  | Genera el secreto TOTP inicial y un desafío de verificación. |
| `POST` | `/auth/2fa/verify` | Consume el desafío y emite access/refresh tokens.            |
| `POST` | `/auth/refresh`    | Rota el refresh token.                                       |
| `POST` | `/auth/logout`     | Revoca el refresh token.                                     |

Variables requeridas:

- `AUTH_MODE`: `memory` para pruebas sin base de datos o `postgres` para usar PostgreSQL.
- `JWT_SECRET`: secreto de firma de al menos 32 caracteres.
- `TOTP_ENCRYPTION_KEY`: clave AES-GCM de 32 bytes codificada en base64url sin padding.
- `DATABASE_URL`: obligatorio únicamente con `AUTH_MODE=postgres`.

Variables opcionales:

- `HTTP_ADDR`: dirección HTTP, por defecto `:8080`.
- `TOTP_ISSUER`: nombre mostrado en el autenticador.
- `AUTH_CHALLENGE_TTL`: duración del desafío, por defecto `5m`.
- `AUTH_ACCESS_TTL`: duración del access token, por defecto `15m`.
- `AUTH_REFRESH_TTL`: duración del refresh con `recordarSesion=false`, por defecto `24h`.
- `AUTH_REMEMBER_REFRESH_TTL`: duración del refresh con `recordarSesion=true`, por defecto `720h`.

## Prueba manual en modo memoria

Con la API ejecutándose en `http://localhost:8080`, desde otra terminal PowerShell:

```powershell
$login = Invoke-RestMethod -Method Post http://localhost:8080/auth/login -ContentType "application/json" -Body (@{
  email = "demo@centinela.local"
  password = "Centinela123!"
  recordarSesion = $true
} | ConvertTo-Json)
$login | ConvertTo-Json -Depth 6
```

La primera respuesta contiene `challengeToken` y `requiresTwoFactorSetup=true`. Usa el token para generar el secreto TOTP:

```powershell
$setup = Invoke-RestMethod -Method Post http://localhost:8080/auth/2fa/setup -ContentType "application/json" -Body (@{
  challengeToken = $login.challengeToken
} | ConvertTo-Json)
$setup | ConvertTo-Json -Depth 6
```

Agrega el `otpAuthURL` a una aplicación autenticadora, genera el código de seis dígitos y verifica el desafío:

```powershell
$verify = Invoke-RestMethod -Method Post http://localhost:8080/auth/2fa/verify -ContentType "application/json" -Body (@{
  challengeToken = $setup.challengeToken
  code = "123456"
} | ConvertTo-Json)
$verify | ConvertTo-Json -Depth 6
```

Reemplaza `123456` por el código actual de tu aplicación TOTP. En la terminal del backend verás los eventos `[audit]`, `[memory]` y los refresh creados o rotados.
