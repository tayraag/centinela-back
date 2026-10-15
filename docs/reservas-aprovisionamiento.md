# Seguridad de las reservas de creación

La creación usa un bloqueo compartido en Redis para validar y reservar capacidad
antes de enviar la orden a Proxmox. El respaldo en memoria sigue disponible para
otras funciones, pero crear devuelve HTTP 503
`PROVISIONING_COORDINATION_UNAVAILABLE` sin enviar órdenes a Proxmox.

Un error al enviar la creación no demuestra que haya sido rechazada: la tarea
remota puede existir. Las reservas no vencen automáticamente y se conservan ante
errores de transporte, tareas fallidas o tiempos de seguimiento agotados. Una
finalización exitosa confirmada libera la reserva e invalida las cachés. Las
fechas de vencimiento antiguas no demuestran una cancelación si el registro aún
existe; las reservas ya vencidas antes de arrancar esta versión no se recuperan.

La reserva de RAM y CPU se asocia al VMID devuelto. Solo se agrega la parte que
todavía no aparece en el inventario. El disco se cuenta de forma conservadora
hasta finalizar correctamente: ver la VM no demuestra que la asignación del
volumen ya esté reflejada en la capacidad del almacenamiento.

## Límites pendientes

Esta corrección no incorpora reconciliación automática durable de una orden con
resultado desconocido. Antes de liberar una reserva retenida hay que investigar
su estado en Proxmox; el timeout del solicitante no alcanza como prueba. No se
debe borrar la lista completa de reservas para recuperar una sola operación.

La pérdida de datos de Redis y el vencimiento del bloqueo durante una pausa del
proceso siguen sin protección completa. Aumentar un TTL no resuelve esos casos.
El bloqueo global actual vence a los 60 segundos, sin renovación. Hace falta
resolver estos límites antes de afirmar seguridad ante caídas con varios procesos.
Las pruebas locales no demuestran el comportamiento de Proxmox en producción.

## Pruebas locales

Las regresiones de respuesta perdida y doble conteo fallan sobre `a363c9a` y pasan
con esta corrección. La prueba concurrente usa dos clientes del adaptador Redis
contra miniredis, no dos procesos ni un servidor Redis real. Lanza 24 solicitudes:
acepta seis y rechaza dieciocho sin enviar sus órdenes a Proxmox. La prueba HTTP
usa el cliente real contra un servidor local que acepta la orden y corta la
conexión antes de devolver el resultado.
