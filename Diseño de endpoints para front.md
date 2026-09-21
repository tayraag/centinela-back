#### 0.1 Recuperar contraseña (Solicitud)

El backend recibe el email, valida su formato, genera el código temporal de 6 dígitos y define su fecha de expiración.&nbsp;

- El usuario presiona "Enviar enlace de recuperación" en la primera pantalla.&nbsp;
- **POST /api/auth/password/forgot**

**1\. Petición (Frontend ➡️ Backend)**

```json
// Body
{
  "email": "ejemplo@correo.com"
}
```

**2\. Respuesta (Backend ➡️ Frontend)**&nbsp;

El frontend recibe un mensaje genérico para evitar ataques de enumeración (evita confirmar si el correo existe o no explícitamente).&nbsp;

```json
{
  "message": "Si el correo está registrado, recibirás un código de recuperación en unos minutos."
}
```

#### 0.2 Recuperar contraseña (Verificar y Restablecer)

El backend recibe el email, el código de verificación y la nueva contraseña. Valida que el código sea correcto y no haya expirado, y luego establece la nueva contraseña.

- Cuando el usuario ingresa los 6 dígitos y la nueva contraseña, y presiona "Verificar y Restablecer" en la segunda pantalla.
- **POST /api/auth/password/reset**&nbsp;

**1\. Petición (Frontend ➡️ Backend)**

```json
{
  "email": "ejemplo@correo.com",
  "codigo": "123456",
  "nuevaContrasena": "MiSuperPassword123!"
}
```

&nbsp;

**2\. Respuesta (Backend ➡️ Frontend)**&nbsp;

Respuesta Exitosa (200 OK):

El sistema restablece la contraseña e invalida el código (de un solo uso) para impedir su reutilización. Adicionalmente, se revocan todas las sesiones activas del usuario.&nbsp;

```json
{
  "message": "Contraseña recuperada exitosamente."
}
```

**Respuestas de Error (400 Bad Request):**

```json
// Si el código expira, se equivoca, o intenta fuerza bruta (>3 intentos)
{
  "errorCode": "RESET_FAILED",
  "message": "código inválido o expirado"
}
```

```json
// Si la contraseña nueva es idéntica a la anterior
{
  "errorCode": "RESET_FAILED",
  "message": "la nueva contraseña no puede ser igual a la actual"
}
```

&nbsp;

**Plantilla de Errores para todo el flujo:**

El backend debe devolver los errores necesarios para que el frontend maneje el flujo. Si el código expira, la contraseña no cumple las reglas, o el email no existe, el frontend interceptará una respuesta de error (ej. HTTP 400 o 404\) con esta estructura estándar:

```
{
  "errorCode": "RECOVERY_INVALID_CODE",
  "message": "El código ingresado es incorrecto o ha expirado."
}
```

&nbsp;

Notas sobre las urls: que es eso de /api/v1/ ?? es para el **versionado de APIs**. El proyecto lo mas probable es que nunca tenga una version 2\. Si queremos ir a la purificacion total lo ideal seria algo como POST /auth/login. PERO recomendacion tecnica es mantener al menos el prefijo /api/ para que el enrutador de Go separe claramente lo que es tráfico de datos de lo que son los archivos estáticos del frontend.&nbsp;

#### 1\. Inicio de sesión

El frontend envía el email y la contraseña. El backend valida credenciales, verifica que la cuenta esté activa, y devuelve un **JWT temporal** (válido por 5 minutos) que autoriza únicamente el flujo de 2FA. El token definitivo **no se emite en este paso**.

&nbsp;

Petición (Frontend ➡️ Backend)

**POST /api/auth/login**

```json
{
  "email": "ejemplo@correo.com",
  "contrasena": "MiSuperPassword123!"
}
```

> ⚠️ El campo es `contrasena`, no `password`. El frontend debe usar este nombre exacto.

&nbsp;

**Respuestas Exitosas (200 OK)**

Ambos escenarios devuelven el mismo formato. La diferencia está en el campo `totpVinculado`, que le indica al frontend qué pantalla mostrar a continuación.

&nbsp;

**Escenario A: Usuario sin 2FA configurado (`totpVinculado: false`)**

El frontend debe redirigir al flujo de vinculación inicial: primero llama a `GET /api/auth/2fa/qr` para obtener el QR, y luego a `POST /api/auth/2fa/verify` para confirmarlo.

```json
{
  "jwtTemporal": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "totpVinculado": false
}
```

&nbsp;

**Escenario B: Usuario con 2FA ya configurado (`totpVinculado: true`)**

El frontend debe mostrar la pantalla de ingreso del código TOTP de 6 dígitos. El siguiente paso es `POST /api/auth/2fa/verify`.

```json
{
  "jwtTemporal": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "totpVinculado": true
}
```

> 📌 El `jwtTemporal` debe guardarse temporalmente (memoria/estado de la app) y enviarse como `Authorization: Bearer <jwtTemporal>` en los siguientes pasos del flujo 2FA. **No persiste en localStorage**; solo vive durante el flujo de login (5 min).

&nbsp;

**Respuestas de Error (401 Unauthorized / 403 Forbidden)**

El backend nunca indica si el email existe o no (prevención de enumeración de usuarios). Los errores de credenciales siempre devuelven el mismo mensaje genérico.

```json
{
  "errorCode": "AUTH_FAILED",
  "message": "Credenciales inválidas."
}
```

```json
{
  "errorCode": "AUTH_FAILED",
  "message": "Cuenta desactivada, contacte al administrador."
}
```

(Otros `errorCode` posibles: `INVALID_REQUEST` si el body no tiene el formato correcto).

#### 2\. Crear organización / Administrador inicial

El frontend necesita un único endpoint donde envíe los datos de la nueva organización y los de la persona que será el dueño (Administrador). Al ser una creación, Go deberá responder con un código HTTP **201 Created**.&nbsp;

&nbsp;

Petición (Frontend ➡️ Backend)

**POST /api/organizations**&nbsp;

&nbsp;

```json
{
  "organizationName": "Tecnología Global S.A.",
  "fullName": "Lisandro",
  "username": "lisandro_admin",
  "email": "lisandro@tecnologiaglobal.com",
  "password": "MiSuperPassword123!"
}
```

**Respuesta Exitosa (201 Created)**

El backend procesa la creación de la organización, le asigna automáticamente el rol de Administrador inicial al usuario creador, y devuelve los recursos generados.

&nbsp;

```json
{
  "id": "org-uuid-9876-5432",
  "name": "Tecnología Global S.A.",
  "createdAt": "2026-08-27T18:25:00Z",
  "adminUser": {
    "id": "user-uuid-1234-5678",
    "fullName": "Lisandro",
    "username": "lisandro_admin",
    "email": "lisandro@tecnologiaglobal.com",
    "role": "ADMIN",
    "isActive": true
  }
}
```

&nbsp;

**Respuestas de Error (400 Bad Request / 409 Conflict)**

Se deben contemplar las validaciones típicas de registro, como que el correo ya exista o la contraseña sea débil.&nbsp;

#### 3\. Configuración inicial de 2FA (Obtener QR)

Este paso aplica **solo cuando `totpVinculado: false`** en la respuesta del login. El frontend solicita el código QR que el usuario debe escanear con su app autenticadora (Google Authenticator, Authy, etc.). El backend genera un nuevo secreto TOTP, lo cifra y lo guarda, y devuelve el QR como imagen PNG en base64 junto con el secreto en texto para ingreso manual.

**Este es el único momento en que el secreto TOTP se expone en texto plano. Nunca más se vuelve a devolver.**

&nbsp;

Petición (Frontend ➡️ Backend)

**GET /api/auth/2fa/qr**

```
Headers:
  Authorization: Bearer <jwtTemporal>   ← el JWT recibido en el paso de login
```

_No se envía body._

**Respuesta Exitosa (200 OK)**

```json
{
  "qrBase64": "data:image/png;base64,iVBORw0KGgo...",
  "secretoManual": "JBSWY3DPEHPK3PXP"
}
```

- `qrBase64`: imagen PNG codificada en base64 lista para renderizar como `<img src="...">`. Contiene la URL `otpauth://` completa.
- `secretoManual`: el secreto en texto (base32) para los usuarios que no puedan escanear el QR y deban ingresar la clave manualmente en su app autenticadora.

Una vez que el usuario escaneó el QR o ingresó la clave manual, el frontend pasa al paso 4 (Verificación 2FA) para confirmar que la vinculación fue exitosa.

**Respuestas de Error (400 / 401 / 409)**

```json
{
  "errorCode": "INVALID_TOKEN",
  "message": "Token inválido o expirado."
}
```

```json
{
  "errorCode": "WRONG_TOKEN_TYPE",
  "message": "Se requiere un token pre-autenticación para esta ruta."
}
```

```json
// 409 Conflict — El usuario ya tiene 2FA activo
{
  "errorCode": "TOTP_ALREADY_LINKED",
  "message": "el doble factor ya está activo. Para regenerar el QR se requiere un restablecimiento administrativo"
}
```

> ⚠️ Si el frontend recibe `409 TOTP_ALREADY_LINKED`, **no** debe mostrar la pantalla de QR. Significa que el usuario ya completó la vinculación y solo un administrador puede resetearla vía `POST /api/admin/users/:id/2fa/reset`.

#### 4\. Verificación 2FA

Este endpoint es el paso final del flujo de login **en ambos escenarios** (vinculación inicial y login normal). El frontend envía el código TOTP de 6 dígitos que el usuario ve en su app autenticadora. El `jwtTemporal` viaja en el header, no en el body.

Si era la primera vinculación (Escenario A), este paso también confirma que el secreto quedó activo (`totpVinculado` pasa a `true` en la BD).

&nbsp;

Petición (Frontend ➡️ Backend)

**POST /api/auth/2fa/verify**

```
Headers:
  Authorization: Bearer <jwtTemporal>   ← el JWT recibido en el paso de login
```

```json
{
  "codigo": "123456"
}
```

> ⚠️ El campo es `codigo`, no `code`. No hay campo `rememberDevice` (el 2FA siempre se exige, confirmado por diseño).

**Respuesta Exitosa (200 OK)**

Al validar el código correctamente, el backend entrega el par de tokens definitivo. El `expiresIn` es el tiempo de vida del `accessToken` en segundos (por defecto 8 horas = 28800 s, configurable).

```json
{
  "accessToken": "eyJhbGciOiJIUzI1NiIsInR...",
  "refreshToken": "eyJhbGciOiJIUzI1NiIsInR...",
  "expiresIn": 28800
}
```

> 📌 El `accessToken` y el `refreshToken` son JWT firmados que contienen los claims del usuario (ID, rol, estado 2FA verificado). El frontend no necesita recibir un objeto `user` separado: puede decodificar el payload del JWT para leer `sub` (userID) y `rol`. El `refreshToken` tiene una vida de 30 días (configurable).

**Respuestas de Error (400 / 401)**

```json
{
  "errorCode": "TOTP_FAILED",
  "message": "Código TOTP incorrecto."
}
```

```json
{
  "errorCode": "TOTP_FAILED",
  "message": "código TOTP ya utilizado, espere al siguiente código"
}
```

```json
{
  "errorCode": "TOTP_FAILED",
  "message": "Sesión ya fue verificada."
}
```

```json
{
  "errorCode": "TOTP_FAILED",
  "message": "El usuario no tiene TOTP configurado, obtenga primero el QR."
}
```

#### 5\. Recuperación / Revinculación de 2FA

> 🚧 **Estado: pendiente de definición de alcance.** El flujo de auto-recuperación por email (con código temporal + SMTP) está en evaluación y puede quedar fuera del alcance del proyecto.
>
> **Workaround actual implementado:** Un administrador puede resetear el 2FA de un usuario vía el siguiente endpoint protegido. El usuario afectado deberá completar el flujo de vinculación inicial (QR) en su próximo login.
>
> **POST /api/admin/users/:id/2fa/reset**
>
> ```
> Headers:
>   Authorization: Bearer <accessToken de un ADMIN>
> ```
>
> _(El `id` del usuario a resetear se envía en la URL)_
>
> Respuesta exitosa: `200 OK`. El sistema invalida todas las sesiones activas del usuario y resetea su TOTP.

---

_El flujo de auto-recuperación propuesto a continuación es preliminar y está sujeto a revisión:_

#### 5\. Recuperación / Revinculación de 2FA (propuesta preliminar)

Petición (Frontend ➡️ Backend):&nbsp;

**POST /api/auth/2fa/recovery/request**&nbsp;

```json
{
  "email": "ejemplo@correo.com"
}
```

**Respuesta Exitosa (200 OK):**&nbsp;

&nbsp;

```json
{
  "message": "Código de verificación enviado al correo.",
  "is2faActive": true,
  "expires_in": 600,
  "resend_delay": 49
}
```

&nbsp;

Petición (Frontend ➡️ Backend):&nbsp;

Para validar identidad y generar nuevo TOTP. El frontend envía el código del email y la contraseña actual al mismo tiempo para validar la identidad completa.&nbsp;

**POST /api/Auth/2fa/recovery/verify-identity**&nbsp;

&nbsp;

```json
{
  "email": "ejemplo@correo.com",
  "emailCode": "852963",
  "password": "MiPasswordSeguro123!"
}
```

**Respuesta Exitosa (200 OK):**&nbsp;

El backend valida ambos datos. Si son correctos, genera los datos del nuevo TOTP pero aún no borra el viejo (por si el usuario cancela a la mitad).&nbsp;

```json
{
  "message": "Identidad y contraseña verificadas correctamente.",
  "recovery_token": "eyJhbGciOiJIUzI1..."
}
```

(Nota: Este `recovery_token` funciona como una "llave temporal" que le da permiso al usuario para usar los endpoints de la configuración inicial del 2fa).

&nbsp;

En lugar de crear un endpoint nuevo, el frontend le pega al mismo endpoint de la Configuración Inicial de 2FA. La única diferencia es que en los _Headers_ envían el `recovery_token` que obtuvieron en el paso anterior.&nbsp;

**GET /api/auth/2fa/setup**&nbsp;&nbsp;

**Respuesta Exitosa (200 OK):**&nbsp;

```json
{
  "secret": "JBSWY3DPEHPK3PXP",
  "qrCodeUrl": "otpauth://totp/Propex:ejemplo@correo.com?secret=JBSWY3DPEHPK3PXP&issuer=Propex",
  "expires_in": 300
}
```

&nbsp;

Confirmar y Sobrescribir: Nuevamente, reutilizamos el endpoint que confirma la configuración inicial. El frontend envía el código de 6 dígitos que escaneó de la nueva app.

**POST /api/auth/2fa/setup/confirm**&nbsp;

&nbsp;

```json
{
  "codigo_totp": "123456"
}
```

**Respuesta Exitosa (200 OK):**&nbsp;

```json
{
  "message": "2FA configurado y revinculado exitosamente."
}
```

(Aquí el backend hace la magia: valida el código nuevo, sobrescribe el secreto_totp_cifrado viejo en la base de datos con el nuevo, e invalida los códigos de respaldo anteriores).&nbsp;

#### 6\. Dashboard

\-Se pide el estado de la infraestructura métricas con series temporales, instancias recientes, actividad, alertas, permisos y tareas en curso.

\--Dividir todos los datos que se piden en el dashboard en json separados por temáticas, el frontend puede cargar los componentes gráficos en paralelo sin tener que esperar un json gigantesco que el backend tarda en procesar.

    \-El documento exige que el usuario pueda seleccionar un rango para ver las métricas (minuto, última hora, 6 horas, día o 7 días). El `timeframe` es el parámetro que el frontend debe enviar en la URL (por ejemplo: `?timeframe=6h`) para avisar qué opción eligió el usuario en la interfaz.

Cuando recibís ese parámetro, tu backend consulta a la base de datos RRD de Proxmox. La principal ventaja de la base de datos RRD es que agrupa y promedia los datos en intervalos de tiempo para ahorrar espacio. Entonces, si el frontend te pide un filtro temporal de 7 días, tu backend no va a devolver millones de registros (uno por cada segundo de la semana). En su lugar, RRD entrega los datos ya promediados y agrupados, lo que te permite pasarle un array compacto al frontend para que el gráfico se dibuje rápido y sin colapsar.

**Tema 1 (Resumen) y Tema 3 (Feed):** No necesitan este parámetro en su URL. Estos endpoints siempre devuelven la "foto" exacta del instante actual (estado, recursos totales, tareas activas) y los eventos más recientes, independientemente del rango de tiempo que el usuario quiera ver en los gráficos.

**Tema 2 (Métricas):** Aquí es donde el frontend inyecta la elección del usuario enviando el parámetro con los valores definidos en el requerimiento (minuto, última hora, 6 horas, día, 7 días). Al recibir este filtro temporal, tu backend sabe exactamente qué porción del histórico debe extraer de la base de datos para armar la serie temporal de CPU, RAM y Almacenamiento. Al aislar este parámetro en una sola petición, el frontend puede actualizar la fecha de los gráficos sin tener que recargar pesadamente todo el resto del dashboard.

&nbsp;

**Tema 1: Estado y Resumen General**

Este endpoint carga de forma instantánea los números principales, el estado de conexión, los permisos y las tareas que se encuentran en ejecución.

&nbsp;

**GET /api/dashboard/summary**

```json
{
  "infrastructureStatus": {
    "isConnected": true,
    "node": "pve1",
    "isApiOperational": true,
    "lastSyncAt": "2026-08-28T16:50:00Z",
    "syncErrors": null
  },
  "summary": {
    "totalInstances": 15,
    "running": 10,
    "stopped": 5,
    "templatesAvailable": 3,
    "runningPercent": 66.67,
    "stoppedPercent": 33.33
  },
  "permissions": ["instance:start", "instance:stop", "instance:reboot"],
  "activeOperations": [
    {
      "taskId": "UPID:pve1:0001:...",
      "type": "BACKUP",
      "resource": "web-server",
      "status": "RUNNING",
      "progress": 45
    }
  ]
}
```

&nbsp;

**Tema 2: Métricas del Host (Gráficos Históricos)**&nbsp;

Aquí es donde interviene el filtro temporal. El frontend solicita los datos de CPU, RAM y Almacenamiento enviando el rango deseado en la URL para dibujar la serie temporal de cada muestra.

&nbsp;

**GET /api/dashboard/metrics?timeframe=1h**

```json
{
  "cpu": {
    "usedPercent": 45.5,
    "totalCores": 32,
    "usedCores": 14.5,
    "history": [
      { "timestamp": "2026-08-28T15:50:00Z", "usedPercent": 40.0 },
      { "timestamp": "2026-08-28T16:50:00Z", "usedPercent": 45.5 }
    ]
  },
  "ram": {
    "usedGb": 64.5,
    "totalGb": 128.0,
    "availableGb": 63.5,
    "usedPercent": 50.4,
    "history": [
      { "timestamp": "2026-08-28T15:50:00Z", "usedPercent": 48.0 },
      { "timestamp": "2026-08-28T16:50:00Z", "usedPercent": 50.4 }
    ]
  },
  "storage": {
    "usedGb": 500.0,
    "totalGb": 1000.0,
    "availableGb": 500.0,
    "usedPercent": 50.0,
    "history": [
      { "timestamp": "2026-08-28T15:50:00Z", "usedPercent": 49.5 },
      { "timestamp": "2026-08-28T16:50:00Z", "usedPercent": 50.0 }
    ]
  }
}
```

&nbsp;

**Tema 3: Novedades y Sucesos**&nbsp;

Este endpoint se encarga de alimentar las listas o paneles laterales del Dashboard: qué instancias tuvieron actividad reciente, el registro de la actividad en sí y qué alertas se dispararon.

&nbsp;

**GET /api/dashboard/feed**&nbsp;

```json
{
  "recentInstances": [
    {
      "id": "qemu/102",
      "name": "web-server",
      "type": "VM",
      "status": "running",
      "vCpu": 4,
      "ramGb": 8.0,
      "lastActivityAt": "2026-08-28T16:45:00Z",
      "lastActivityType": "START"
    }
  ],
  "recentActivity": [
    {
      "id": "audit-uuid-1",
      "action": "START",
      "resourceName": "web-server",
      "resourceId": "qemu/102",
      "user": "Lisandro",
      "createdAt": "2026-08-28T16:45:00Z",
      "status": "EXITO",
      "eventType": "USER_ACTION"
    }
  ],
  "alerts": [
    {
      "id": "alert-uuid-1",
      "type": "RESOURCE_SATURATION",
      "severity": "WARNING",
      "message": "Uso de CPU supera el 80%",
      "createdAt": "2026-08-28T16:30:00Z",
      "resourceName": "db-server",
      "resourceId": "lxc/105",
      "isActive": true
    }
  ]
}
```

#### 7\. Inventario de instancias

El contrato centraliza el resumen numérico y la lista de recursos en una única respuesta estructurada. Las búsquedas por ID o nombre, junto con los filtros por estado, tipo y nodo exigidos por el documento, se manejan enviándolos como Query Parameters.

El uso de Query Parameters en el requerimiento 7 cumple la función de delegar el trabajo pesado al backend. En lugar de que el frontend descargue todo el volumen de instancias creadas para ocultarlas visualmente en el navegador, el frontend envía sus necesidades de filtrado, estado y tipo directamente en la URL. Al recibir esta URL, el backend procesa las condiciones y devuelve una única respuesta estructurada que contiene exclusivamente el array de instances que coinciden con los criterios, junto con un objeto summary cuyos números ya están pre-calculados en base a esa lista filtrada.

&nbsp;

**GET /api/instances?search=web\&type=VM\&status=running\&node=pve1**

Con respecto a los query params en la url DE EJEMPLO de arriba:

`-GET /api/instances`: Es la ruta base que indica al backend que se requiere obtener el inventario general de recursos.

`-?`: Es el símbolo universal que marca el inicio de los parámetros de consulta en una URL.&nbsp;

`-search=web`: Aplica una búsqueda textual, exigiendo que las instancias devueltas contengan "web" en su nombre o ID.

\-**`&`**: Actúa como un conector ("y") para encadenar múltiples filtros de forma simultánea.

\-**`type=VM`**: Filtra los resultados para incluir únicamente Máquinas Virtuales, descartando los contenedores LXC.

\-**`status=running`**: Restringe la lista a aquellas máquinas que se encuentran actualmente en ejecución.

\-**`node=pve1`**: Limita la respuesta a las instancias alojadas específicamente en el servidor físico denominado "pve1".

&nbsp;

```json
{
  "summary": {
    "total": 25,
    "running": 15,
    "stopped": 10
  },
  "instances": [
    {
      "id": "qemu/102",
      "name": "web-server",
      "type": "VM",
      "status": "running",
      "node": "pve1",
      "vCpu": 4,
      "ramTotalGb": 8.0,
      "storageUsedGb": 20.5,
      "storageTotalGb": 50.0,
      "storageUsagePercent": 41.0,
      "mainIp": "192.168.1.50",
      "permissions": ["START", "STOP", "REBOOT", "EDIT", "SNAPSHOT", "DELETE"],
      "availableActions": ["STOP", "REBOOT", "SNAPSHOT"], //Se separó estrictamente permissions (las acciones que el rol del usuario tiene permitidas) de availableActions (las acciones que el estado actual de la máquina permite ejecutar). Un operador puede tener permiso de iniciar (START), pero si la máquina ya está encendida, esa opción desaparece de las acciones disponibles.
      "activeTask": null //devuelve null (si la maquina está inactiva) o el estado actual de la acción
    },
    {
      "id": "lxc/105",
      "name": "db-cache",
      "type": "LXC",
      "status": "stopped",
      "node": "pve1",
      "vCpu": 2,
      "ramTotalGb": 4.0,
      "storageUsedGb": 10.0,
      "storageTotalGb": 20.0,
      "storageUsagePercent": 50.0,
      "mainIp": null,
      "permissions": ["START", "STOP", "REBOOT", "EDIT", "SNAPSHOT", "DELETE"],
      "availableActions": ["START", "EDIT", "DELETE"],
      "activeTask": "STARTING"
    }
  ]
}
```

#### 8.0 Detalle de instancia

Este es el primer endpoint que el frontend consume al entrar al detalle. Alimenta la cabecera (estado, acciones permitidas, IP, tareas activas) y la primera pestaña de información general y hardware base.&nbsp;

&nbsp;

**GET /api/instances/{id}**&nbsp;

```json
{
  "id": "qemu/102",
  "name": "web-server",
  "type": "VM",
  "status": "running",
  "node": "pve1",
  "os": "Debian 12",
  "qemuAgentActive": true,
  "autoStart": true,
  "uptimeSeconds": 345600,
  "createdAt": "2025-01-15T10:00:00Z",
  "lastBackupAt": "2026-08-27T03:00:00Z",
  "templateOrigin": "debian-12-cloudinit",
  "description": "Servidor web principal",
  "notes": "No reiniciar sin avisar a base de datos.",
  "mainIp": "192.168.1.50",
  "ipAddresses": [
    { "address": "192.168.1.50", "version": "IPv4", "isPrimary": true },
    {
      "address": "fe80::1ff:fe23:4567:890a",
      "version": "IPv6",
      "isPrimary": false
    }
  ],
  "permissions": ["START", "STOP", "REBOOT", "EDIT", "SNAPSHOT"],
  "availableActions": ["STOP", "REBOOT", "SNAPSHOT"],
  "activeOperations": [],
  "currentResources": {
    "cpuUsagePercent": 45.5,
    "vCpuAssigned": 4,
    "ram": { "usedGb": 4.5, "assignedGb": 8.0 },
    "disk": { "usedGb": 20.0, "assignedGb": 50.0, "usagePercent": 40.0 }
  },
  "recentTasks": [
    {
      "taskId": "UPID:pve1:0001:...",
      "type": "START",
      "status": "COMPLETED",
      "startAt": "2026-08-25T10:00:00Z",
      "endAt": "2026-08-25T10:00:15Z",
      "durationSeconds": 15,
      "user": "Lisandro",
      "error": null
    }
  ]
}
```

&nbsp;

**Acciones de Control de Energía**

Estas acciones (iniciar, apagar, forzar apagado, reiniciar) no necesitan que el frontend envíe un cuerpo con datos, ya que la orden es directa y el ID de la máquina viaja en la propia URL:

&nbsp;

**POST /api/instances/{id}/start**

**POST /api/instances/{id}/shutdown** (Apagado ordenado)&nbsp;

**POST /api/instances/{id}/stop** (Forzar apagado)&nbsp;

**POST /api/instances/{id}/reboot**

Sí hay una respuesta para el front, pero está agrupada más abajo para no repetirla.

Como el sistema debe encender, apagar, clonar, etc. de manera asíncrona porque toman tiempo, el backend no puede devolver un "éxito" o "falla" inmediato. Sin importar si el usuario pidió reiniciar la máquina o crear un snapshot, tu backend siempre va a devolver el mismo formato confirmando que la orden fue recibida.&nbsp;

#### 8.x Históricos y Métricas Transversales

En lugar de repetir el histórico en cada pestaña (8.0 a 8.3), centralizamos las series temporales de todos los recursos (CPU, RAM, Disco, Red) en un solo lugar gobernado por el rango temporal.&nbsp;

&nbsp;

**GET /api/instances/{id}/metrics?timeframe=1h**

```json
{
  "cpu": [{ "timestamp": "2026-08-28T16:00:00Z", "usedPercent": 40.0 }],
  "ram": [{ "timestamp": "2026-08-28T16:00:00Z", "usedPercent": 55.2 }],
  "disk": [
    {
      "timestamp": "2026-08-28T16:00:00Z",
      "usedPercent": 40.0,
      "readMb": 15.5,
      "writeMb": 5.2,
      "iops": 250
    }
  ],
  "network": [
    { "timestamp": "2026-08-28T16:00:00Z", "inMb": 10.2, "outMb": 4.1 }
  ]
}
```

#### 8.1 Detalle de instancia \- Recursos

Devuelve el detalle microscópico del hardware virtual, la disponibilidad del nodo anfitrión para saber si se puede escalar, y las reglas que el frontend debe usar para validar el formulario de edición de recursos.&nbsp;

&nbsp;

**GET /api/instances/{id}/resources**&nbsp;

```json
{
  "cpu": {
    "usedPercent": 45.5,
    "vCpuAssigned": 4,
    "sockets": 1,
    "coresPerSocket": 4,
    "model": "x86-64-v2-AES",
    "limit": null
  },
  "ram": {
    "usedGb": 4.5,
    "assignedGb": 8.0,
    "availableGb": 3.5,
    "usagePercent": 56.2,
    "ballooningEnabled": true
  },
  "nodeCapacity": {
    "cpuTotal": 32,
    "cpuUsed": 16,
    "cpuAvailable": 16,
    "ramTotalGb": 128.0,
    "ramUsedGb": 64.0,
    "ramAvailableGb": 64.0,
    "storageTotalGb": 1000.0,
    "storageUsedGb": 500.0,
    "storageAvailableGb": 500.0
  },
  "editConfig": {
    "canEdit": true,
    "cpuMin": 1,
    "cpuMax": 16,
    "ramMinGb": 1.0,
    "ramMaxGb": 64.0,
    "supportsHotplug": false,
    "requiresReboot": true
  }
}
```

&nbsp;

Para la modificación de vCPUs y memoria RAM asignada, el frontend enviará los nuevos valores requeridos.

**PUT /api/instances/{id}/resources**

Petición (Frontend ➡️ Backend):

&nbsp;

```
{
  "vCpu": 4,
  "ramMb": 4096
}
```

&nbsp;

#### 8.2 Detalle de instancia \- Disco

Resumen de almacenamiento global de la instancia y desglose exacto de cada disco conectado.

&nbsp;

**GET /api/instances/{id}/disks**&nbsp;

```json
{
  "summary": {
    "totalUsedGb": 20.0,
    "totalAvailableGb": 30.0,
    "provisionedGb": 50.0,
    "usagePercent": 40.0
  },
  "storageDetails": {
    "id": "local-lvm",
    "type": "lvmthin",
    "totalCapacityGb": 500.0,
    "usedGb": 200.0,
    "availableGb": 300.0,
    "thinProvisioning": true
  },
  "disks": [
    {
      "id": "scsi0",
      "name": "Hard Disk",
      "type": "MAIN",
      "storageId": "local-lvm",
      "format": "raw",
      "sizeGb": 50.0,
      "usedGb": 20.0,
      "usagePercent": 40.0,
      "controller": "VirtIO SCSI",
      "volumeId": "vm-102-disk-0",
      "serial": "drive-scsi0",
      "status": "OK",
      "trimDiscard": true,
      "cache": "none",
      "backupEnabled": true,
      "snapshotCount": 2,
      "lastBackupAt": "2026-08-27T03:00:00Z",
      "lastSnapshotAt": "2026-08-20T15:00:00Z"
    }
  ]
}
```

El documento detalla que se debe permitir ampliar la capacidad de un disco (pero no reducirla).

pense que se entendia que si se puede ampliar se debe poder disminuir tambien.. en caso de que sea necesario liberar recursos para otras cosas

&nbsp;

**PUT /api/instances/{id}/disks/{diskId}/resize**

Petición (Frontend ➡️ Backend):&nbsp;

&nbsp;

```
{
  "newSizeGb": 100.0
}
```

&nbsp;

#### 8.3 Detalle de instancia \- Red

Configuración completa de conectividad, direcciones e interfaces secundarias.

&nbsp;

**GET /api/instances/{id}/network**&nbsp;

```json
{
  "mainInterface": {
    "id": "net0",
    "name": "eth0",
    "model": "VirtIO",
    "bridge": "vmbr0",
    "mac": "BC:24:11:D1:C0:04",
    "status": "active",
    "vlan": null,
    "speedMbps": 1000,
    "mtu": 1500,
    "firewallEnabled": true
  },
  "ipAddresses": [
    {
      "version": "IPv4",
      "address": "192.168.1.50",
      "mask": "24",
      "gateway": "192.168.1.1",
      "isPrimary": true,
      "status": "active"
    }
  ],
  "dns": {
    "primary": "1.1.1.1",
    "secondary": "8.8.8.8",
    "searchDomain": "empresa.local"
  },
  "generalConfig": {
    "defaultGateway": "192.168.1.1",
    "defaultBridge": "vmbr0",
    "firewallPolicy": "DROP",
    "natEnabled": false,
    "proxyArpEnabled": false
  },
  "additionalInterfaces": []
}
```

&nbsp;

El documento detalla que se debe permitir agregar o eliminar interfaces de red.

**POST /api/instances/{id}/network** (Agregar interfaz)

Petición (Frontend ➡️ Backend):

&nbsp;

```
{
  "bridge": "vmbr0",
  "model": "VirtIO",
  "vlan": null,
  "ipv4": {
    "type": "static",
    "address": "192.168.1.51",
    "mask": "24",
    "gateway": "192.168.1.1"
  }
}
```

&nbsp;

#### 8.4 Detalle de instancia \- Snapshots

Como devuelve lista, se utiliza la estructura plana de array con resúmen precalculado.

&nbsp;

**GET /api/instances/{id}/snapshots**&nbsp;

```json
{
  "total": 1,
  "currentSnapshotId": "snap_v1",
  "snapshots": [
    {
      "id": "snap_v1",
      "name": "Pre-Update",
      "description": "Antes de actualizar Debian",
      "createdAt": "2026-08-20T15:00:00Z",
      "createdBy": "Lisandro",
      "status": "OK",
      "tags": ["update", "safe"]
    }
  ]
}
```

El documento exige que para crear un punto de restauración se pida obligatoriamente un nombre, permitiendo de forma opcional una descripción y etiquetas.

**POST /api/instances/{id}/snapshots** (Crear)&nbsp;

Petición (Frontend ➡️ Backend):

```json
{
  "name": "pre_actualizacion_db",
  "description": "Backup antes de correr migraciones",
  "tags": ["update", "db"]
}
```

&nbsp;

**POST /api/instances/{id}/snapshots/{snapshotId}/restore** (Revertir)

Petición (Frontend ➡️ Backend):

```
{} //no llevaría nada en el body
```

&nbsp;

**DELETE /api/instances/{id}/snapshots/{snapshotId}** (Eliminar)&nbsp;

No lleva cuerpo en la petición.

#### 8.5 Detalle de instancia \- Tareas

El frontend enviará filtros de paginación y búsqueda en la URL (ej. `?status=RUNNING&page=1`).

&nbsp;

**GET /api/instances/{id}/tasks**&nbsp;

```json
{
  "total": 45,
  "tasks": [
    {
      "taskId": "UPID:pve1:0001:...",
      "readableName": "Creación de Snapshot",
      "operationType": "SNAPSHOT",
      "description": "Creación del snapshot Pre-Update",
      "status": "COMPLETED",
      "startAt": "2026-08-20T15:00:00Z",
      "endAt": "2026-08-20T15:01:00Z",
      "durationSeconds": 60,
      "user": "Lisandro",
      "progress": 100,
      "result": "OK",
      "error": null,
      "relatedResource": "qemu/102",
      "canCancel": false
    }
  ]
}
```

&nbsp;

Si una tarea en curso admite cancelación (indicado previamente por el atributo `canCancel`), el frontend disparará esta acción.

**POST /api/tasks/{taskId}/cancel**

Petición (Frontend ➡️ Backend):

&nbsp;

```
{} //ver esto, esta accion de cancelar no pide enviar nada
```

&nbsp;

#### 8.6 Detalle de instancia \- Registros

El documento exige explícitamente que la pestaña de Registros soporte consultas filtradas por "Nivel" (INFO, WARN, ERROR, DEBUG), por "Fuente", por "Rango temporal" y con búsqueda textual. El frontend te va a pedir esos datos usando Query Parameters. Por ejemplo:

**GET /api/instances/{id}/logs?level=ERROR**

Si el usuario quisiera buscar errores del componente QEMU de hoy, la URL que el frontend armaría se vería así: `?level=ERROR&source=QEMU&timeframe=today`&nbsp;

```json
{
  "total": 120,
  "logs": [
    {
      "id": "log-999",
      "createdAt": "2026-08-28T16:25:00Z",
      "level": "ERROR",
      "source": "QEMU",
      "event": "I/O Error",
      "description": "Fallo de lectura en scsi0",
      "instanceId": "qemu/102"
    }
  ]
}
```

&nbsp;

**El Contrato de Respuesta Unificado (Backend ➡️ Frontend)**

La clave de todas estas acciones (excepto la eliminación de una interfaz de red que puede ser inmediata) es que el documento exige que se gestionen de manera **asíncrona**. Proxmox recibe la orden, devuelve un identificador de tarea (UPID) y la procesa en segundo plano.

Por lo tanto, sin importar si el frontend llamó al endpoint de `start`, al de `snapshots` o al de `resize`, tu backend siempre validará los permisos, reenviará la orden a Proxmox, capturará ese UPID y le devolverá al frontend exactamente el mismo formato de respuesta inmediata.

**Respuesta Exitosa Unificada (202 Accepted):**

```json
{
  "taskId": "task-uuid-8888",
  "status": "ACCEPTED",
  "message": "Operación iniciada en segundo plano."
}
```

#### 9.0 Crear instancia \- Paso 1: General

Aunque el documento divide el wizard en 6 pasos visuales (del 9.0 al 9.5), el frontend no envía un JSON distinto por cada paso. La arquitectura correcta para esto es que el frontend le pida al backend los datos necesarios para llenar los menús desplegables (nodos, storages, templates), y recién en el Paso 6 envíe un único gran JSON maestro con toda la configuración. Antes de que el usuario pueda crear la máquina, el front necesita consumir endpoints rápidos para saber qué opciones mostrar en los selectores y validar los límites.

&nbsp;

**GET /api/nodes**

Lista los servidores físicos y su capacidad libre para que el frontend ponga límites a los campos de RAM y CPU.

```
[
  {
    "id": "pve1",
    "name": "Servidor Principal",
    "status": "online",
    "cpuAvailable": 16,
    "ramAvailableGb": 64.0
  }
]
```

#### 9.1 Crear instancia \- Paso 2: Sistema/Recursos

Acá el front va a necesitar los datos del mismo EP usado en el paso 1 (GET /api/nodes), no hace falta llamarlo dos veces.

#### 9.2 Crear instancia \- Paso 3: Disco/Almacenamiento

Lista los discos físicos del nodo donde se puede guardar la nueva máquina.

**GET /api/nodes/{nodeId}/storages**

```json
[
  {
    "id": "local-lvm",
    "name": "Almacenamiento Local",
    "type": "lvmthin",
    "availableGb": 300.0
  }
]
```

#### 9.3 Crear instancia \- Paso 4: Red/Conectividad

Lista los bridges (puentes de red) disponibles en ese nodo.&nbsp;

**GET /api/nodes/{nodeId}/networks**&nbsp;

```json
[{ "id": "vmbr0", "status": "active" }]
```

#### 9.4 Crear instancia \- Paso 5: Sistema Operativo

El frontend envía el parámetro `?type=VM` o `?type=LXC` para recibir las ISOs o Plantillas compatibles con el tipo de instancia seleccionada.&nbsp;

**GET /api/nodes/{nodeId}/templates?type=VM**&nbsp;

```json
[
  {
    "id": "local:iso/debian-12.iso",
    "name": "Debian 12 Netinst",
    "os": "Linux",
    "isRecommended": true
  }
]
```

#### 9.5 Crear instancia \- Paso 6: Resumen/Revisar y crear

Cuando el usuario hace clic en "Crear", el frontend recolecta todo lo seleccionado en los 5 pasos anteriores y hace un: **POST /api/instances**

El front no debe mandarnos el `vmid` en el json. El backend tiene que ir a buscar el siguiente ID libre a Proxmox en el milisegundo exacto en que procesa la orden.

Como Proxmox maneja las Máquinas Virtuales (VM) y los Contenedores (LXC) de manera distinta (el LXC exige una contraseña inicial obligatoria y no usa ISOs), el backend debe estar preparado para recibir estas dos variantes estructurales:&nbsp;

&nbsp;

**Variante A: Creación de Máquina Virtual (VM)**

```json
{
  "type": "VM",
  "node": "pve1",
  "name": "web-server",
  "description": "Servidor web principal",
  "cpu": {
    "cores": 2
  },
  "ram": {
    "memoryMb": 4096
  },
  "disk": {
    "storageId": "local-lvm",
    "sizeGb": 50,
    "format": "raw",
    "thinProvisioning": true
  },
  "network": {
    "bridge": "vmbr0",
    "vlan": null,
    "firewall": true,
    "ipv4": {
      "type": "dhcp"
    }
  },
  "os": {
    "templateId": "local:iso/debian-12.iso"
  }
}
```

&nbsp;

**Variante B: Creación de Contenedor (LXC)**&nbsp;

En este caso, se reemplaza la ISO por un archivo `.tar.zst` y se incluye la contraseña que exige Proxmox para el usuario root del contenedor. Se detalla la configuración de red estática.

Los del front no nos pidieron ninguna contraseña para LXC. Pero el gurú vio el código que armaron los de infra y dice que “la API de Proxmox exige el parámetro `password` como obligatorio para aprovisionar un LXC.”

```json
{
  "type": "LXC",
  "node": "pve1",
  "name": "db-cache",
  "password": "PasswordSegura123!",
  "cpu": {
    "cores": 1
  },
  "ram": {
    "memoryMb": 1024
  },
  "disk": {
    "storageId": "local-lvm",
    "sizeGb": 10
  },
  "network": {
    "bridge": "vmbr0",
    "firewall": false,
    "ipv4": {
      "type": "static",
      "address": "192.168.1.50",
      "mask": "24",
      "gateway": "192.168.1.1"
    }
  },
  "os": {
    "templateId": "local:vztmpl/debian-12-standard.tar.zst"
  }
}
```

&nbsp;

**Respuesta de Validación y Creación**&nbsp;

&nbsp;

Al recibir el POST, el backend valida internamente que los recursos (CPU/RAM) sigan existiendo en Proxmox. Si los recursos se agotaron milisegundos antes, devuelve un `409 Conflict` o `400 Bad Request` con un error claro (ej. _"Recursos insuficientes en el nodo pve1"_). Si los recursos están disponibles y se dispara la orden, como crear una máquina toma tiempo, el backend captura el UPID de Proxmox, lo transforma en un ID interno seguro, y devuelve la **Respuesta Exitosa Unificada (202 Accepted)**:

&nbsp;

```json
{
  "taskId": "task-uuid-8888",
  "status": "ACCEPTED",
  "message": "Operación iniciada en segundo plano."
}
```

&nbsp;

Si los recursos se agotan en el proceso, el sistema debe rechazar la solicitud de quien llegue tarde, interceptando el código **HTTP 409 Conflicto**

```json
{
  "errorCode": "RESOURCE_QUOTA_EXCEEDED",
  "message": "Los recursos del nodo han sido ocupados por otra operación. No hay RAM o CPU suficiente para aprovisionar esta instancia."
}
```

&nbsp;

#### 10\. Auditoría

Para la seguridad de esto se requiere autenticación mediante JWT y validación del permiso para visualizar auditoría (exclusivo para administradores o roles con acceso autorizado).&nbsp;

Mediante query parameters:

**GET /api/audit?page=1\&limit=10\&from=2024-05-01\&to=2024-05-08\&result=SUCCESS**

- GET /api/audit: Es la ruta base del endpoint encargada de consultar y devolver los registros de auditoría almacenados en la base de datos.
- page=1: Indica el número de la página actual que se está solicitando (útil para la paginación de la tabla).
- limit=10: Define la cantidad máxima de registros que el backend debe devolver por cada página (en este caso, 10 elementos por vista).
- from=2024-05-01 y to=2024-05-08: Son los parámetros que aplican el filtro temporal, limitando la búsqueda de eventos únicamente a los ocurridos dentro de ese rango de fechas (desde el 1 de mayo hasta el 8 de mayo de 2024).
- result=SUCCESS: Filtra los registros para mostrar exclusivamente aquellas acciones cuyo resultado haya sido exitoso.

&nbsp;

**Respuesta Exitosa (200 OK):**&nbsp;

```json
{
  "statistics": {
    "totalEvents": 126,
    "successful": {
      "count": 78,
      "percentage": 61.9
    },
    "warnings": {
      "count": 8,
      "percentage": 6.3
    },
    "failed": {
      "count": 40,
      "percentage": 31.7
    }
  },
  "pagination": {
    "currentPage": 1,
    "pageSize": 10,
    "totalItems": 126,
    "totalPages": 13
  },
  "events": [
    {
      "id": "audit-uuid-001",
      "timestamp": "2024-05-08T18:04:21Z",
      "userId": "user-uuid-admin",
      "user": "AD Admin",
      "action": "Inició instancia",
      "resourceId": "101",
      "resourceName": "Ubuntu Server",
      "resourceType": "VM",
      "node": "pve01",
      "result": "SUCCESS",
      "sourceIp": "192.168.1.45",
      "details": "Iniciado correctamente desde el panel de control.",
      "errorMessage": null
    }
  ]
}
```

#### 11.1 Gestión de usuarios (Estadísticas de Usuarios)

El frontend necesita la cantidad total y desglosada por roles para mostrar en la cabecera de la pantalla.

&nbsp;

**GET /api/admin/users/stats**

&nbsp;

**Respuesta Exitosa (200 OK):**&nbsp;

&nbsp;

```json
{
  "totalUsers": 15,
  "admins": 3,
  "standard": 10,
  "readOnly": 2
}
```

#### 11.2 Gestión de usuarios (Listado y Consulta de Usuarios)

El frontend necesita traer la lista de usuarios de la organización con filtros, ordenamiento y búsqueda textual. La URL soportará _Query Parameters_ (ej: `GET /api/admin/users?role=ADMIN&status=active&search=juan`).

&nbsp;

**GET /api/admin/users**

&nbsp;

**Respuesta Exitosa (200 OK):**&nbsp;

Devuelve un array directo (Slice). Se incluye el indicador visual para saber si el registro de la lista corresponde a la persona que está viendo la pantalla.

&nbsp;

```json
[
  {
    "id": "user-uuid-1111",
    "fullName": "Juan Pérez",
    "username": "jperez",
    "email": "juan@empresa.com",
    "role": "ADMIN",
    "isActive": true,
    "lastLoginAt": "2026-08-27T10:00:00Z",
    "is2faActive": false,
    "createdAt": "2026-08-01T14:30:00Z",
    "isCurrentUser": true
  },
  {
    "id": "user-uuid-2222",
    "fullName": "María Gómez",
    "username": "mgomez",
    "email": "maria@empresa.com",
    "role": "OPERATOR",
    "isActive": false,
    "lastLoginAt": null,
    "is2faActive": false,
    "createdAt": "2026-08-20T09:15:00Z",
    "isCurrentUser": false
  }
]
```

#### 11.3 Gestión de usuarios (Obtener Roles Disponibles)

Antes de poder crear un usuario, el frontend necesita saber qué roles existen para llenar el menú desplegable del formulario.

&nbsp;

**GET /api/roles**

&nbsp;

**Respuesta Exitosa (200 OK):**&nbsp;

&nbsp;

```json
[
  {
    "id": "role-uuid-1",
    "name": "ADMIN",
    "description": "Acceso total al sistema y gestión de usuarios."
  },
  {
    "id": "role-uuid-2",
    "name": "OPERATOR",
    "description": "Acceso restringido solo a instancias asignadas."
  }
]
```

&nbsp;

NOTA SUPER IMPORTANTE: en el listado del **Requerimiento 11** incluyeron un bloque llamado "Operaciones sobre usuarios" que menciona: editar, cambiar rol, activar, desactivar, restablecer contraseña y eliminar. También mencionaron ver "permisos existentes y asignables". **Todo ese bloque de operaciones complejas es exactamente lo que compone el Requerimiento 13 (Detalle / Edición de usuario)**. En lugar de amontonarlo en el listado básico, el frontend necesita endpoints específicos para gestionar cada una de estas facetas de un usuario ya creado.&nbsp;

#### 12\. Crear usuario

El frontend envía los datos básicos ingresados en el formulario. El backend se encarga automáticamente de generar la contraseña temporal segura, asociar al usuario a la organización del administrador, marcar la contraseña como temporal y establecer el 2FA como no configurado.&nbsp;

&nbsp;

Petición (Frontend ➡️ Backend)&nbsp;

**POST /api/admin/users**

&nbsp;

```json
{
  "fullName": "Carlos López",
  "username": "clopez",
  "email": "carlos@empresa.com",
  "roleId": "role-uuid-2",
  "isActive": true
}
```

&nbsp;

**Respuesta Exitosa (201 Created):**

El documento especifica que el backend debe devolver únicamente el ID creado, el rol asignado y el estado de la cuenta.&nbsp;

&nbsp;

```json
{
  "id": "user-uuid-3333",
  "role": "OPERATOR",
  "isActive": true
}
```

(Nota: El documento dice que la contraseña temporal se enviará por correo "solo si mantienen el envío por email en el alcance". Si finalmente deciden no usar un servidor de correos (SMTP) para ahorrar tiempo, deberán agregar un campo `"tempPassword": "ClaveGenerada123!"` en este JSON de respuesta para que el administrador pueda copiarla y pasársela a su compañero por chat).

&nbsp;

**COMENTARIO MICA: me gusta la idea de que implementen la alternativa como plan A, para el MVP y dejar el servicio SMTP como plan B para cuando se implemente si es que se logra implementar. asi el back y el front no se frenen por la existencia del SMTP.**

**Lo que si analise un poco los otros endpoint y si no se levanta SMTP los flujos de recuperacion de contraseña y revinculacion de 2FA van a quedar bloqueados por lo que hay implementar algo como este plan A, considerar que en el 13.4 se gestione ahi este plan A. ANALIZAR**

&nbsp;

**Respuestas de Error (400 / 409):**

Se deben contemplar las fallas de validación detalladas en el documento (nombre de usuario o email ya existentes, formato inválido, falta de permisos).&nbsp;

&nbsp;

```json
{
  "errorCode": "USER_EMAIL_ALREADY_EXISTS",
  "message": "El correo ingresado ya se encuentra registrado en otra cuenta."
}
```

#### 13.0 Información General y Edición Básica

El frontend necesita traer el perfil completo del usuario, incluyendo qué instancias tiene asignadas, y poder modificar sus datos, su rol o su estado (activo/inactivo).

&nbsp;

**GET /api/admin/users/{userId}**&nbsp;

&nbsp;

**Respuesta Exitosa (200 OK):**

```json
{
  "id": "user-uuid-3333",
  "fullName": "Carlos López",
  "username": "clopez",
  "email": "carlos@empresa.com",
  "organizationId": "org-uuid-1",
  "role": "OPERATOR",
  "isActive": true,
  "createdAt": "2026-08-27T10:00:00Z",
  "assignedInstances": [
    {
      "id": "qemu/100",
      "name": "web-server",
      "type": "qemu",
      "node": "pve1",
      "status": "running",
      "accessLevel": "FULL_ACCESS"
    }
  ]
}
```

&nbsp;

Petición (Frontend ➡️ Backend):

**PUT /api/admin/users/{userId}** (Para actualizar datos, rol o estado)&nbsp;

&nbsp;

```json
{
  "fullName": "Carlos López Editado",
  "email": "carlos.nuevo@empresa.com",
  "role": "OPERATOR",
  "isActive": false
}
```

&nbsp;

**DELETE /api/admin/users/{userId}** (Para eliminar al usuario)&nbsp;

Devuelve un simple `204 No Content` sin cuerpo de respuesta.&nbsp;

#### 13.1 Gestión de Acceso a Instancias&nbsp;

Para cumplir con el requisito de "asignar, modificar o quitar acceso a una instancia", el frontend enviará la lista actualizada de permisos del operador.

&nbsp;

Petición (Frontend ➡️ Backend):

**PUT /api/admin/users/{userId}/instances**

&nbsp;

```json
[
  { "instanceId": "qemu/100", "accessLevel": "FULL_ACCESS" },
  { "instanceId": "lxc/101", "accessLevel": "READ_ONLY" }
]
```

&nbsp;

#### 13.2 Actividad Reciente del Usuario&nbsp;

El frontend necesita mostrar el historial de acciones exclusivas de este usuario, con filtros y ordenado por fecha. La URL soportará filtros (ej: `GET /api/admin/users/{userId}/activity?action=START`).&nbsp;

&nbsp;

**GET /api/admin/users/{userId}/activity**&nbsp;

&nbsp;

**Respuesta Exitosa (200 OK):**&nbsp;

&nbsp;

```json
[
  {
    "id": "audit-uuid-999",
    "createdAt": "2026-08-27T12:00:00Z",
    "action": "START",
    "description": "Inició la instancia web-server",
    "resourceId": "qemu/100",
    "resourceType": "qemu",
    "sourceIp": "192.168.1.50",
    "result": "EXITO"
  }
]
```

&nbsp;

#### 13.3 Gestión de Sesiones Activas&nbsp;&nbsp;

El administrador debe poder ver desde dónde está conectado el usuario y poder "patearlo" cerrando sus sesiones.

&nbsp;

**GET /api/admin/users/{userId}/sessions**&nbsp;

&nbsp;

**Respuesta Exitosa (200 OK):**&nbsp;

&nbsp;

```json
[
  {
    "id": "session-uuid-1",
    "device": "Windows",
    "browser": "Chrome",
    "ipAddress": "192.168.1.50",
    "startedAt": "2026-08-27T08:00:00Z",
    "lastActivityAt": "2026-08-27T13:45:00Z",
    "isCurrentSession": false
  }
]
```

**DELETE /api/admin/users/{userId}/sessions** (Cierra todas las sesiones del usuario).&nbsp;

**DELETE /api/admin/users/{userId}/sessions/{sessionId}** (Cierra una sesión en particular).&nbsp;

#### 13.4 Seguridad (2FA y Contraseña)&nbsp;&nbsp;

El administrador puede invalidar el 2FA si el usuario perdió el celular, o generarle una nueva clave temporal.

&nbsp;

**POST /api/admin/users/{userId}/2fa/reset**&nbsp;

Inicia el flujo de revinculación invalidando el 2FA actual.&nbsp;

```json
{
  "message": "La configuración de 2FA ha sido invalidada. El usuario deberá configurarlo en su próximo acceso."
}
```

&nbsp;

**POST /api/admin/users/{userId}/password/reset**&nbsp;

Genera una clave temporal, la asocia al usuario y devuelve el resultado.&nbsp;&nbsp;

```json
{
  "message": "Se ha forzado el restablecimiento de contraseña.",
  "tempPassword": "NuevaClaveSegura123!"
}
```

#### 14.0 Información del Perfil y Organización

El frontend necesita obtener todos los datos del usuario logueado para llenar la pantalla, incluyendo la organización y el estado de su seguridad (2FA).&nbsp;

&nbsp;

**GET /api/account/profile**&nbsp;

&nbsp;

Respuesta Exitosa (200 OK):

&nbsp;

```json
{
  "id": "user-uuid-1234",
  "fullName": "Lisandro",
  "username": "lisandro_admin",
  "email": "lisandro@tecnologiaglobal.com",
  "role": "ADMIN",
  "createdAt": "2026-08-01T14:30:00Z",
  "lastLoginAt": "2026-08-27T10:00:00Z",
  "lastLoginIp": "192.168.1.50",
  "is2faActive": true,
  "organization": {
    "id": "org-uuid-5678",
    "name": "Tecnología Global S.A."
  }
}
```

**PUT /api/account/profile** (Para actualizar datos personales)

El documento especifica que el `username`, la `organization` y el `role` no son modificables desde aquí. Solo se envía lo permitido.&nbsp;

&nbsp;

Petición (Frontend ➡️ Backend):&nbsp;

&nbsp;

```json
{
  "fullName": "Lisandro Editado",
  "email": "nuevo.email@tecnologiaglobal.com"
}
```

**PUT /api/account/organization** (Solo para Administradores)

El documento exige explícitamente que solo el administrador pueda modificar el nombre de la organización. Si un operador intenta pegarle a este endpoint, Go debe devolver un `403 Forbidden`.&nbsp;

&nbsp;

```json
{
  "name": "Nuevo Nombre de Organización S.A."
}
```

&nbsp;

#### 14.1 Seguridad (Cambio de Contraseña y 2FA)

El frontend debe enviar la clave actual para que el backend la valide antes de aplicar el cambio.

&nbsp;

**PUT /api/account/password**&nbsp;

Petición (Frontend ➡️ Backend):&nbsp;

&nbsp;

```json
{
  "currentPassword": "PasswordActual123!",
  "newPassword": "NuevaPasswordSegura456#"
}
```

&nbsp;

**Respuestas de Error (400 Bad Request):**

Si la contraseña actual no coincide o la nueva no cumple las reglas.&nbsp;

&nbsp;

```json
{
  "errorCode": "AUTH_INVALID_CURRENT_PASSWORD",
  "message": "La contraseña actual ingresada es incorrecta."
}
```

&nbsp;

**GET /api/account/2fa**&nbsp;

Devuelve el estado de seguridad detallado. No devuelve el secreto TOTP por seguridad.&nbsp;

&nbsp;

**Respuesta Exitosa (200 OK):**

```json
{
  "isActive": true,
  "activatedAt": "2026-08-15T09:00:00Z"
}
```

(Nota: Para iniciar la vinculación o revinculación del 2FA solicitada en el documento, podemos reutilizar los mismos endpoints de configuración inicial que ya definimos en el Requerimiento 3).

#### 14.2 Gestión de Sesiones Propias

El documento pide un resumen lateral y una vista detallada de "Tu sesión actual" y "Otras sesiones activas". En lugar de hacer múltiples endpoints, lo más estándar en REST es devolver la lista completa y que el frontend dibuje la vista basándose en la propiedad `isCurrent`.

&nbsp;

**GET /api/account/sessions**

Respuesta Exitosa (200 OK):&nbsp;

&nbsp;

```json
[
  {
    "id": "session-uuid-1",
    "device": "Desktop",
    "os": "Windows",
    "browser": "Chrome",
    "ipAddress": "192.168.1.50",
    "startedAt": "2026-08-27T08:00:00Z",
    "lastActivityAt": "2026-08-27T19:45:00Z",
    "isActive": true,
    "isCurrent": true
  },
  {
    "id": "session-uuid-2",
    "device": "Mobile",
    "os": "Android",
    "browser": "Chrome Mobile",
    "ipAddress": "181.45.xx.xx",
    "startedAt": "2026-08-26T15:00:00Z",
    "lastActivityAt": "2026-08-26T15:30:00Z",
    "isActive": true,
    "isCurrent": false
  }
]
```

&nbsp;

**Endpoints de acción para sesiones:**

**DELETE /api/account/sessions/current**: Cierra la sesión actual (es el equivalente al botón "Cerrar sesión" tradicional).

**DELETE /api/account/sessions/{sessionId}**: Cierra un dispositivo específico de la lista.

**DELETE /api/account/sessions**: Al no pasarle ID, el backend cierra masivamente todas las demás sesiones excepto la actual.

#### 15\. Página de acceso denegado – 403

Lo devolverá el backend en cualquier endpoint (ej. `DELETE /api/instances/101`) cuando el usuario intente hacer algo para lo cual no tiene permisos.

```json
{
  "errorCode": "ACCESS_DENIED",
  "message": "No tienes permisos suficientes para acceder a este recurso o realizar esta acción."
}
```

_(Nota: Como dice el documento, el Frontend ya conoce normalmente el usuario autenticado y su rol gracias al JWT, por lo que no es estrictamente necesario que el backend se los repita en cada error 403, manteniendo la respuesta más liviana)_.&nbsp;

#### 16\. Página no encontrada – 404

Si el usuario navega a una ruta inventada (ej. `/rutaloca`), el frontend resuelve el 404 por su cuenta usando su enrutador. Pero si el usuario intenta ver el detalle de una instancia (ej. `GET /api/instances/9999`) y esa máquina no existe en Proxmox, el backend devolvera este JSON.&nbsp;

```json
{
  "errorCode": "RESOURCE_NOT_FOUND",
  "message": "No pudimos encontrar la instancia que estás buscando. Es posible que haya sido eliminada.",
  "resource_type": "instancia",
  "resource_id": "9999"
}
```

&nbsp;
