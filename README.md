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
  - *Nota:* Una vez instalado, reinicia tu terminal (o PowerShell / VS Code) para que reconozca el comando `go` en el PATH del sistema.

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
