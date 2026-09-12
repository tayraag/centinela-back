# El Centinela — Setup y Flujo de Autenticación (RF-01)

Guía completa para levantar el proyecto desde cero, ejecutar el seed inicial y probar el flujo de autenticación con 2FA.

---

## Prerrequisitos

| Herramienta | Versión mínima | Verificar |
|---|---|---|
| Go | 1.21+ | `go version` |
| Docker + Docker Compose | Cualquiera reciente | `docker --version` |
| Git | Cualquiera | `git --version` |

---

## 1. Clonar el repositorio

```bash
git clone <URL-del-repo>
cd centinela-back
```

---

## 2. Configurar variables de entorno

```bash
# Copiar la plantilla
cp cmd/api/.env.example .env
```

Editar `.env` con valores reales. Los campos obligatorios son:

| Variable | Descripción | Ejemplo |
|---|---|---|
| `DB_DSN` | Cadena de conexión PostgreSQL | `host=localhost user=... port=5433 ...` |
| `JWT_SECRET` | Clave secreta para firmar JWT (≥32 chars) | `mi_clave_super_secreta_de_32_chars` |
| `JWT_ACCESS_TTL_HOURS` | Vida del access token en horas | `8` |
| `JWT_REFRESH_TTL_DAYS` | Vida del refresh token en días | `30` |
| `TOTP_ENCRYPTION_KEY` | Clave AES-256 para cifrar secretos TOTP (exactamente 32 chars ASCII) | `0123456789abcdef0123456789abcdef` |
| `APP_NAME` | Nombre que aparece en Google Authenticator | `El Centinela` |

> **Seguridad**: El `.env` ya está en `.gitignore`. Nunca lo commitees.

---

## 3. Levantar PostgreSQL con Docker

```bash
docker-compose up -d
```

Verificar que el container está corriendo:

```bash
docker-compose ps
# Debe mostrar centinela-postgres con STATUS "Up"
```

El script `scripts/init.sql` se ejecuta automáticamente al crear el container y habilita la extensión `pgcrypto` + la función `uuid_generate_v7()`.

---

## 4. Instalar dependencias Go

```bash
go mod tidy
```

---

## 5. Crear el primer usuario administrador (seed)

> ⚠️ **Solo se ejecuta UNA vez**, al configurar el proyecto por primera vez.
> No elimina nada, no pisa credenciales existentes.

```bash
go run ./cmd/seed/
```

El script es **interactivo** y pide los datos con valores por defecto entre corchetes.
Si simplemente presionás Enter, usa el valor por defecto:

```
  Nombre de la organización [El Centinela]:          ← Enter = usa "El Centinela"
  Nombre completo del admin [Administrador]: Juan Pérez
  Email del admin [admin@elcentinela.local]: juan@empresa.com
  Nombre de usuario [admin]:                         ← Enter = usa "admin"
  Contraseña (mínimo 8 caracteres): MiPass123!
```

Output esperado:
```
✅ Seed completado exitosamente
   Organización : El Centinela (uuid...)
   Usuario Admin : Juan Pérez (uuid...)
   Email         : juan@empresa.com
   Rol           : ADMIN
   2FA           : pendiente de vincular (primer login)
```

**¿Qué pasa si lo corrés de nuevo?**

- Si el email **ya existe** en la BD → el script para y avisa, sin tocar nada:
  ```
  ⚠️  Ya existe un usuario con el email 'juan@empresa.com'. Abortando para no duplicar.
  ```
- Si la organización ya existe pero el email es nuevo → reutiliza la organización y crea el usuario.
- **Nunca borra ni sobreescribe** datos existentes.


---

## 6. Arrancar el servidor

El servidor se ejecuta en una terminal que hay que **dejar abierta**.
Los logs aparecen ahí en tiempo real a medida que llegan requests.

```bash
go run ./cmd/api/
```

Output esperado al iniciar:
```
12:00:00 🚀 Iniciando El Centinela Backend...
12:00:00 ℹ️  Variables de entorno cargadas desde: .env
12:00:00 ✅ Conexión exitosa a PostgreSQL
12:00:00 🔄 Ejecutando auto-migración de tablas...
12:00:01 ✅ Migración completada exitosamente
12:00:01 🛡️ Servidor HTTP escuchando en el puerto 8080...
```

> El servidor queda bloqueando esa terminal. Para detenerlo: `Ctrl + C`.

### Flujo de trabajo con dos terminales

Para probar los endpoints necesitás **dos terminales abiertas al mismo tiempo**:

```
┌─────────────────────────────────────────┐  ┌─────────────────────────────────────────┐
│  Terminal 1 — Servidor (dejar corriendo) │  │  Terminal 2 — Requests (curl / frontend) │
├─────────────────────────────────────────┤  ├─────────────────────────────────────────┤
│  go run ./cmd/api/                       │  │  curl http://localhost:8080/api/auth/... │
│                                          │  │                                          │
│  12:00:01 🛡️ Servidor escuchando...      │  │  {"jwtTemporal": "eyJ..."}               │
│  12:00:05 [AUTH] login attempt | ...     │◄─┤                                          │
│  12:00:05 ✅ 200 | 255ms | resp: {...}   │  │                                          │
└─────────────────────────────────────────┘  └─────────────────────────────────────────┘
```

**En VS Code**: abrí una segunda terminal integrada con `Ctrl + Shift + `` ` ` ` (backtick) o desde el menú Terminal → New Terminal.

**En Windows**: abrí una ventana de **Símbolo del sistema (cmd.exe)** o **PowerShell** desde el mismo directorio del proyecto.


---

## 7. Flujo completo de autenticación — paso a paso

> 💡 **Comandos universales de una sola línea**:
> Todos los comandos están en **una sola línea** (sin `\`, sin `` ` ``, sin `^`).
> Funcionan directamente copiando y pegando en **cmd.exe (Símbolo del sistema)**, **PowerShell**, **Git Bash**, **Linux** o **macOS**.

---

### 7.1 Login → JWT temporal

Ejecutá en tu terminal:

```bash
curl -X POST http://localhost:8080/api/auth/login -H "Content-Type: application/json" -d "{\"email\":\"juan@empresa.com\",\"contrasena\":\"MiPass123!\"}"
```

> *(Ajustá `juan@empresa.com` y `MiPass123!` por las credenciales que configuraste en el seed)*

**Respuesta esperada:**
```json
{
  "jwtTemporal": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "totpVinculado": false
}
```

**Logs en la terminal del servidor:**
```
→ POST /api/auth/login | body: {"email":"juan@empresa.com","contrasena":"***"}
[AUTH] login attempt | email=juan@empresa.com
[AUTH] login ok | user=juan@empresa.com | rol=ADMIN | totp_vinculado=false | jti=abc123...
✅ 200 | 254ms | resp: {"jwtTemporal":"eyJ…[jwt]","totpVinculado":false}
```

> 📋 **Copiá el valor de `jwtTemporal`** (el texto largo sin comillas) para pegarlo en `<JWT_TEMPORAL>` en los siguientes pasos.

---

### 7.2 Obtener QR de vinculación (primera vez)

> Solo necesario si `totpVinculado: false`. Reemplazá `<JWT_TEMPORAL>` con el token obtenido en el login:

```bash
curl -X GET http://localhost:8080/api/auth/2fa/qr -H "Authorization: Bearer <JWT_TEMPORAL>"
```

**Respuesta esperada:**
```json
{
  "secretoManual": "K5IJR2GW2KMO6EAW7446LMV7PIW7LRAA",
  "qrBase64": "data:image/png;base64,iVBORw0KGgo..."
}
```

**Logs en el servidor:**
```
→ GET /api/auth/2fa/qr
[2FA] qr requested | user=juan@empresa.com | jti=abc123...
[2FA] qr generated | user=juan@empresa.com | secreto_len=32 | qr_bytes=1024
✅ 200 | 38ms | resp: {"qrBase64":"data:imag…[jwt]","secretoManual":"K5IJR2GW…"}
```

**Vincular en tu aplicación de autenticación:**
- En Google Authenticator / Authy: tocá el botón `+` → `Introducir clave de configuración` (o configuración manual) e ingresá el valor de `secretoManual`.

---

### 7.3 Verificar TOTP → Access + Refresh tokens

Ingresá el código de 6 dígitos que muestra tu app autenticadora (ejemplo: `123456`):

```bash
curl -X POST http://localhost:8080/api/auth/2fa/verify -H "Authorization: Bearer <JWT_TEMPORAL>" -H "Content-Type: application/json" -d "{\"codigo\":\"123456\"}"
```

**Respuesta esperada:**
```json
{
  "accessToken":  "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "refreshToken": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "expiresIn":    28800
}
```

**Logs en el servidor:**
```
→ POST /api/auth/2fa/verify | body: {"codigo":"123456"}
[2FA] totp verify attempt | user=juan@empresa.com | codigo=123456
[2FA] first link confirmed | user=juan@empresa.com
[AUTH] tokens issued | user=juan@empresa.com | access_jti=xyz... | refresh_jti=uvw... | access_ttl=8h0m0s
✅ 200 | 103ms | resp: {"accessToken":"eyJ…[jwt]","expiresIn":28800,"refreshToken":"eyJ…[jwt]"}
```

> 📋 **Copiá el `accessToken` y el `refreshToken`** para usarlos en peticiones protegidas.

---

### 7.4 Logins siguientes (TOTP ya vinculado)

Cuando `totpVinculado: true`, el flujo no pide QR y se hace directamente:

**1. Login:**
```bash
curl -X POST http://localhost:8080/api/auth/login -H "Content-Type: application/json" -d "{\"email\":\"juan@empresa.com\",\"contrasena\":\"MiPass123!\"}"
```

**2. Verificar TOTP (con el código actual del autenticador):**
```bash
curl -X POST http://localhost:8080/api/auth/2fa/verify -H "Authorization: Bearer <JWT_TEMPORAL>" -H "Content-Type: application/json" -d "{\"codigo\":\"<CODIGO_6_DIGITOS>\"}"
```

---

### 7.5 Renovar access token con refresh token

```bash
curl -X POST http://localhost:8080/api/auth/refresh -H "Content-Type: application/json" -d "{\"refreshToken\":\"<REFRESH_TOKEN>\"}"
```

**Respuesta esperada:**
```json
{
  "accessToken":  "eyJ... (nuevo)",
  "refreshToken": "eyJ... (mismo)",
  "expiresIn":    28800
}
```

**Logs en el servidor:**
```
→ POST /api/auth/refresh | body: {"refreshToken":"eyJ…[jwt]"}
[AUTH] refresh attempt
[AUTH] refresh ok | user=juan@empresa.com | new_access_jti=aaa...
✅ 200 | 4ms | resp: {"accessToken":"eyJ…[jwt]","expiresIn":28800,"refreshToken":"eyJ…[jwt]"}
```

---

### 7.6 Reset de 2FA de un usuario (solo ADMIN)

```bash
curl -i -X POST http://localhost:8080/api/auth/2fa/relink -H "Authorization: Bearer <ACCESS_TOKEN>" -H "Content-Type: application/json" -d "{\"usuarioId\":\"<UUID_DEL_USUARIO>\"}"
```

**Respuesta esperada:** HTTP `204 No Content` (sin body).

**Logs en el servidor:**
```
→ POST /api/auth/2fa/relink | body: {"usuarioId":"uuid..."}
[ADMIN] relink requested | target_user=uuid... | admin_jti=xyz...
[ADMIN] relink done | target_user=uuid... | totp_reset=true | sessions_invalidated=true
✅ 204 | 42ms | resp: 
```

---

## 8. Casos de error (verificar comportamiento)

> El flag `-i` en curl muestra los encabezados de respuesta con el código HTTP (`401`, `403`, etc.).

### Token sin enviar → 401
```bash
curl -i -X POST http://localhost:8080/api/auth/2fa/relink -H "Content-Type: application/json" -d "{\"usuarioId\":\"test\"}"
```
- **Código esperado**: `HTTP/1.1 401 Unauthorized`
- **Log servidor**: `🔒 401 | 0ms | resp: {"errorCode":"MISSING_TOKEN",...}`

### JWT pre-auth en ruta que pide access token → 403
```bash
curl -i -X POST http://localhost:8080/api/auth/2fa/relink -H "Authorization: Bearer <JWT_TEMPORAL>" -H "Content-Type: application/json" -d "{\"usuarioId\":\"test\"}"
```
- **Código esperado**: `HTTP/1.1 403 Forbidden`
- **Log servidor**: `🚫 403 | 0ms | resp: {"errorCode":"WRONG_TOKEN_TYPE",...}`

### Contraseña incorrecta → 401
```bash
curl -i -X POST http://localhost:8080/api/auth/login -H "Content-Type: application/json" -d "{\"email\":\"juan@empresa.com\",\"contrasena\":\"incorrecta\"}"
```
- **Código esperado**: `HTTP/1.1 401 Unauthorized`
- **Log servidor**: `[AUTH] login failed | email=juan@empresa.com | reason=wrong_password`

### Código TOTP incorrecto → 401
```bash
curl -i -X POST http://localhost:8080/api/auth/2fa/verify -H "Authorization: Bearer <JWT_TEMPORAL>" -H "Content-Type: application/json" -d "{\"codigo\":\"000000\"}"
```
- **Código esperado**: `HTTP/1.1 401 Unauthorized`
- **Log servidor**: `[2FA] totp invalid | user=juan@empresa.com`
```

---

## 9. Verificación en base de datos

```bash
# Conectarse al container
docker exec -it centinela-postgres psql -U centinela_admin -d centinela_db

# Ver usuarios (contraseña hasheada, secreto cifrado)
SELECT nombre_usuario, rol, activo, totp_vinculado,
       LEFT(contrasena_hash, 7) || '...' AS hash_preview,
       CASE WHEN secreto_totp_cifrado = '' THEN 'vacío'
            ELSE LEFT(secreto_totp_cifrado, 20) || '...(cifrado AES)' END AS secreto
FROM usuarios;

# Ver sesiones activas
SELECT usuario_id, LEFT(jti_token, 8) || '...' AS jti, activa, estado2fa, fecha_expiracion
FROM sesion_activas
ORDER BY fecha_creacion DESC
LIMIT 10;
```

**Output esperado:**
```
 nombre_usuario | rol   | activo | totp_vinculado | hash_preview | secreto
----------------+-------+--------+----------------+--------------+----------------------------
 admin          | ADMIN | t      | t              | $2a$12$...   | QZpG3e1n5Svz44puigZN...(cifrado AES)
```

---

## 10. Estructura de archivos relevantes

```
centinela-back/
├── cmd/
│   ├── api/
│   │   ├── main.go              ← Punto de entrada, DI, rutas
│   │   ├── .env.example         ← Plantilla de variables de entorno
│   │   └── .env                 ← (gitignored) Variables reales
│   └── seed/
│       └── main.go              ← Script de seed interactivo
├── internal/
│   ├── core/
│   │   ├── domain/
│   │   │   └── models.go        ← Modelos de dominio (Usuario, SesionActiva, etc.)
│   │   ├── ports/
│   │   │   └── auth_port.go     ← Interfaces + DTOs (AuthRepository, AuthService)
│   │   └── services/
│   │       └── auth_service.go  ← Lógica de negocio completa
│   ├── adapters/
│   │   ├── primary/http/
│   │   │   ├── auth_handler.go  ← Handlers HTTP
│   │   │   └── middleware/
│   │   │       ├── auth_middleware.go    ← RequirePreAuth, RequireAuth, RequireRole
│   │   │       └── logger_middleware.go ← Logger conciso de requests/responses
│   │   └── secondary/postgres/
│   │       ├── db.go            ← Inicialización de GORM
│   │       └── auth_repository.go ← Implementación del repositorio
│   └── infrastructure/crypto/
│       ├── password.go          ← bcrypt cost 12
│       ├── totp.go              ← AES-256-GCM + validación TOTP
│       └── jwt.go               ← HS256 con claims personalizados
├── scripts/
│   └── init.sql                 ← pgcrypto + uuid_generate_v7()
└── docker-compose.yml           ← PostgreSQL 16
```

---

## 11. Rutas registradas

| Método | Ruta | Middleware | Descripción |
|---|---|---|---|
| POST | `/api/auth/login` | — | Login email+contraseña → JWT temporal (5 min) |
| POST | `/api/auth/refresh` | — | Refresh token → nuevo access token |
| GET | `/api/auth/2fa/qr` | `RequirePreAuth` | Generar QR para vincular TOTP |
| POST | `/api/auth/2fa/verify` | `RequirePreAuth` | Validar código TOTP → access+refresh tokens |
| POST | `/api/auth/2fa/relink` | `RequireAuth` + `RequireRole("ADMIN")` | Admin resetea 2FA de un usuario |
