# Estándar de Códigos HTTP y Manejo de Errores

Esta tabla define el contrato de respuestas HTTP que utilizará toda la API de El Centinela para asegurar la consistencia entre el Backend y el Frontend.

## Tabla de Referencia Rápida

| Método | Situación | Código |
| :--- | :--- | :--- |
| **GET** | Recurso(s) encontrado(s) | 200 |
| **GET** | Sin resultados (lista vacía) | 204 |
| **GET** | Datos de entrada inválidos | 400 |
| **GET** | Recurso único no existe | 404 |
| **POST** | Recurso creado | 201 |
| **POST** | Petición aceptada y ejecutándose en segundo plano | 202 |
| **POST** | Datos inválidos / faltantes | 400 |
| **POST** | Recurso referenciado no existe | 404 |
| **PUT** | Petición aceptada y ejecutándose en segundo plano | 202 |
| **PUT** | Actualización exitosa sin cuerpo | 204 |
| **PUT** | Actualización exitosa con cuerpo | 200 |
| **PUT** | Datos inválidos | 400 |
| **PUT** | Recurso a actualizar no existe | 404 |
| **PATCH** | Modificación parcial exitosa con cuerpo | 200 |
| **PATCH** | Petición aceptada y ejecutándose en segundo plano | 202 |
| **PATCH** | Datos inválidos / estado incorrecto para la operación | 400 |
| **PATCH** | Recurso a modificar no existe | 404 |
| **PATCH** | Estado del recurso impide la operación | 409 |
| **DELETE** | Petición aceptada y ejecutándose en segundo plano | 202 |
| **DELETE** | Eliminación exitosa | 204 |
| **DELETE** | Recurso a eliminar no existe | 404 |
| **POST/PUT/PATCH/DELETE** | Estado del recurso impide la operación | 409 |
| **Cualquiera** | El cliente no está autenticado | 401 |
| **Cualquiera** | El cliente está autenticado pero no tiene permisos para el recurso | 403 |
| **Cualquiera** | Error interno / BD / servicio externo | 500 |

## Estructura JSON Unificada para Errores (4xx y 5xx)

Cuando la API devuelva un error (códigos 400 a 500), el cuerpo de la respuesta **siempre** tendrá la siguiente estructura JSON:

`json
{
  "errorCode": "CODIGO_INTERNO_EN_MAYUSCULAS",
  "message": "Mensaje descriptivo legible por el usuario o desarrollador"
}
`

### Ejemplos de ErrorCode:
- AUTH_FAILED: Credenciales inválidas.
- INVALID_TOKEN: Token expirado o malformado.
- INVALID_REQUEST: Faltan campos obligatorios o formato incorrecto.
- NOT_FOUND: El recurso solicitado no existe en la base de datos.
- FORBIDDEN: El usuario no tiene permisos para realizar esta acción.
- CONFLICT: La operación no se puede realizar por el estado actual del recurso.
- INTERNAL_ERROR: Error inesperado del servidor.

## Detalle de Códigos 2xx — Éxito

### 200 OK
Operación exitosa con cuerpo de respuesta.
**Usar cuando:**
- GET retorna uno o más recursos encontrados.
- PUT retorna el recurso modificado.

**Ejemplos:**
- GET /recibos/legajo/{legajo} → 200 + lista de recibos
- GET /capacitaciones/historial/{dni} → 200 + lista de capacitaciones
- PUT /conceptos-adicionales/{id} → 200 + recurso actualizado

---

### 201 Created
Recurso creado exitosamente.
**Usar cuando:**
- POST crea un nuevo recurso en la base de datos.
- Incluir Location header con la URI del nuevo recurso cuando sea posible.

**Ejemplos:**
- POST /adicionales-empleado → 201
- POST /conceptos-adicionales → 201
*(No usar para: operaciones que no crean recursos, en esos casos usar 200 o 204).*

---

### 202 Accepted
Petición recibida y ejecutándose en segundo plano.
**Usar cuando:**
- Se envía una petición al servidor, el mismo lo acepta (pasa validaciones), pero lo va a procesar en segundo plano.
- Puede ser en POST, PUT y DELETE.

*Nota: Es obligatorio devolver algún ID que permita al front consultar el estado de la ejecución.*

**Ejemplos:**
- POST /recibos/carga-masiva → 202

---

### 204 No Content
Operación exitosa sin cuerpo de respuesta.
**Usar cuando:**
- GET no encuentra resultados para el filtro indicado (lista vacía).
- PUT o DELETE ejecutan correctamente pero no retornan cuerpo.

**Ejemplos:**
- GET /recibos/mis-recibos → 204 si el empleado no tiene recibos
- GET /adicionales-empleado/legajo/{legajo} → 204 si el empleado no tiene adicionales
- PUT /adicionales-empleado/{id} → 204 tras actualizar
- DELETE /conceptos-adicionales/{id} → 204 tras eliminar

## Detalle de Códigos 4xx — Error del cliente

### 400 Bad Request
El cliente envió datos inválidos o malformados.
**Usar cuando:**
- Parámetros obligatorios ausentes o vacíos.
- Formato inválido (ej. DNI no numérico, fecha inválida).
- Valores fuera de rango (ej. legajo ≤ 0).
- Claims del token ausentes o malformados.

**Ejemplos:**
- GET /recibos/legajo/0 → 400 (legajo inválido)
- GET /recibos/mis-recibos → 400 si el token no contiene el claim "dni"
- POST /adicionales-empleado → 400 si falta el legajo o el monto
*(No usar para: recursos no encontrados (usar 404) ni errores de permisos (usar 403)).*

---

### 403 Forbidden
El cliente está autenticado pero no tiene permisos para el recurso.
**Usar cuando:**
- El usuario existe pero su rol no permite la operación.

---

### 404 Not Found
El recurso específico no existe.
**Usar cuando:**
- Se busca un recurso por ID único y no existe en la base de datos.
- Un legajo o DNI referenciado en una operación no existe.

**Ejemplos:**
- PUT /adicionales-empleado/{id} → 404 si el id no existe
- POST /adicionales-empleado → 404 si el legajo del empleado no existe
*(No usar para: búsquedas que retornan lista vacía (usar 204)).*

**Distinción clave:**
- *Situación:* Busco recibos del legajo 123 y no tiene ninguno → **204**
- *Situación:* Busco el empleado con legajo 123 y no existe → **404**

---

### 409 Conflict
El recurso existe pero su estado actual impide la operación.
**Usar cuando:**
- Se intenta eliminar un recurso que tiene dependencias activas (ej. bloque con turnos activos).
- Se intenta operar sobre un slot sin disponibilidad.
- Se intenta crear un registro que ya existe (ej. inscripción duplicada en lista de espera).

**Ejemplos:**
- DELETE /bloques/{id} → 409 si el bloque tiene turnos activos
- POST /reservas-temporales → 409 si el slot no tiene disponibilidad
- POST /lista-espera → 409 si ya existe una inscripción activa para ese trámite

**Distinción con 400 y 404:**
- *Situación:* Datos del request inválidos o malformados → **400**
- *Situación:* El recurso referenciado no existe → **404**
- *Situación:* El recurso existe pero su estado impide la operación → **409**

---

## Detalle de Códigos 5xx — Error del servidor

### 500 Internal Server Error
Error interno no controlado.
**Usar cuando:**
- Falla de conexión a base de datos o servicio externo.
- Excepción inesperada no contemplada por la lógica de negocio.

*Regla: nunca exponer stack traces ni mensajes internos al cliente. Usar siempre un mensaje genérico.*

**Ejemplos:**
- GET /recibos/legajo/{legajo} → 500 si falla la conexión con la BD
- GET /adicionales-empleado/dni/{dni} → 500 si servicio externo no responde
