# Requerimientos Funcionales

&nbsp;

**RF-01: Autenticación y control de acceso:** el sistema debe permitir el registro e inicio de sesión de usuarios mediante credenciales (usuario o mail/contraseña). Una vez validadas las credenciales, el sistema debe exigir Autenticación Doble Factor (2FA) mediante contraseñas temporales basadas en tiempo (TOTP). Si el usuario no tiene configurado el 2FA, se debe presentar la pantalla con el código QR para la vinculación inicial; si ya está vinculado, se debe solicitar el código TOTP de 6 dígitos. Se debe proveer una opción para solicitar la regeneración/revinculación del 2FA en caso de pérdida de acceso a la aplicación autenticadora.(Una opción de cargarla de forma manual). El control de acceso debe estar basado en roles con al menos dos roles: administrador y operador. Y como medida de seguridad debe generar y validar tokens JWT para manejar sesiones seguras.

&nbsp;

* **front:**&nbsp;  
  * diseñar y maquetar los formularios y registro con validaciones de campos,&nbsp;  
  * implementar el almacenamiento seguro de JWT y el gestor de estado global de la sesión  
  * configurar para poder restringir vistas según el rol del contenido en el JWT  
  * manejar las peticiones HTTP para agregar el encabezado de autorización en cada request  
  * (NUEVO) maquetar y renderizar la pantalla/modal para la vinculación inicial de 2FA mediante código QR.  
  * (NUEVO) diseñar la vista para el ingreso del código TOTP de 6 digitos con opcion para solicitar regeneración  
* **back:**  
  * crear los endpoints de autenticación y verificación de 2FA  
  * implementar seguridad para almacenamiento de contraseñas y de secretos TOTP cifrados  
  * diseñar la firma y verificación de los JWT para que incluya id, rol, estado de verificación 2FA y tiempo de expiración  
  * desarrollar validación de permisos según el rol solicitado  
  * (NUEVO) implementar la generación de secretos TOTP y renderizado del código QR  
  * (NUEVO) desarrollar la lógica de validación del código ingresado por el usuario y los mecanismos para revincular/generar claves 2FA.  
* **infra:**  
  * desplegar y mantener la persistencia de lo que el back ofrezca para usuarios y credenciales  
  * configurar las firmas JWT en el servidor  
  * garantizar conexiones HTTPS segura entre cliente y servidor para que no se roben las credenciales

&nbsp;

**RF-02: Dashboard de estado:** al iniciar el sistema debe mostrar una vista con el estado de salud y consumo de recursos del nodo físico o cluster de proxmox. Debe incluir CPU total (uso en % y cantidad de núcleos), memoria RAM (porcentaje y total) almacenamiento (uso % y capacidad total) tiempo de actividad del nodo y un resumen del número de instancias (VMs y LXC) y su estado general ( corriendo, detenido, etc).

ACLARACIÓN: este nos muestra la salud del hipervisor. Permite saber si el servidor está sobrecargado o tiene recursos disponibles.

* **front:**&nbsp;  
  * diseñar e integrar tarjetas de resúmenes y medidores para el consumo de host  
  * establecer la conexión en tiempo real así se actualizan los valores sin tener que recargar la página (polling \+ websockets, se me ocurre)  
  * implementar indicador visual del estado del hipervisor tipo verde(normal), rojo(inaccesible) etc  
* **back:**  
  * desarrollar consulta periódica para verificar estado de proxmox  
  * distribuir info de proxmox hacia el front procesando y formateando respuestas  
* **infra:**  
  * asegurar los permisos de la API de proxmox para poder leer el estado físico del servidor  
  * configurar cache que almacenan el último estado para respuestas rápidas en altas concurrencias

&nbsp;

**RF-03: Inventario de instancias (VMs y contenedores LXC):** el sistema debe presentar una lista de todas las instancias gestionadas por el servidor proxmox. Cada instancia debe mostrar: ID, nombre, tipo (VM/LXC), estado, dirección IP, y un resumen de uso de recursos (CPU, RAM). La lista debe permitir buscar y filtrar por nombre, estado o tipo.

* **front:**&nbsp;  
  * diseñar vista clara que permita visualizar y combinar máquinas virtuales (VMs)  y contenedores (LXCs)  
  * implementar buscador dinámico en tiempo real y filtros por tipo y estado  
  * diseñar las etiquetas de estado  
  * sincronizar el inventario con los eventos de cambio de estado que recibe del back  
* **back:**  
  * desarrollar el endpoint que permita ver las diferentes instancias  
  * hacer filtrado según matriz de acceso del usuario para mostrar solo las instancias permitidas por el rol  
  * homogeneizar las propiedades de VMs y LXCs en JSON  
* **infra:**  
  * verificar permisos de la API de proxmox para exponer direcciones IP de cada VM/LXC

&nbsp;

**RF-04: Control de instancias:** el sistema debe proporcionar acciones para poder gestionar el ciclo de vida de las instancias. Esto significa que debe poder iniciar, apagar de forma ordenada, forzar el apagado y reiniciar. Cada una de estas acciones debe solicitar confirmación explícita del usuario. El estado de la instancia debe actualizarse en tiempo real a medida que la operación progresa. Aca en back debe manejar el UPID que devuelve proxmox, monitorear su proceso en segundo plano y notificar al front cuando la tarea finalice. Y el front debe bloquear acciones duplicadas mientras la tarea esté en proceso.

* **front:**&nbsp;  
  * diseñar botones de control rápido con modales de confirmación  
  * cambiar la instancia al estado “operación en progreso” cuando se ejecuta una acción y bloquear botones de control sobre la misma  
  * escuchar el resultado del UPID para desproteger botones así como informar el éxito o fallo de la operación  
* **back:**  
  * crear el endpoint para enviar órdenes de energía sobre instancias  
  * capturar el UPID que retorna proxmox al ejecutar una acción  
  * implementar un monitor en segundo plano sobre el estado de la tarea en proxmox  
  * notificar la finalización de la tarea y guardar la traza en auditoría  
* **infra:**  
  * asignar los permisos de gestion de energia a la API de proxmox  
  * configurar canal (colas o bus) para que en segundo plano se escuche el proceso de UPID sin bloquear la API de proxmox

&nbsp;

**RF-05: Monitor de métricas por instancia:** el sistema debe mostrar gráficos dinámicos con las métricas de consumo de recursos de cada instancia en tiempo real, debe mostrar el consumo de CPU, RAM, red y disco de esa instancia específica, en tiempo real y con histórico reciente.

ACLARACIÓN: este RF no es el mismo que el RF-02 , ya que este nos va a permitir saber qué instancia está consumiendo más recursos o tiene picos de uso.

* **front:**&nbsp;  
  * diseñar vistas de detalle por instancia integrando librerías de gráficos de líneas&nbsp;  
  * procesar flujos de datos en tiempo real sin degradar la memoria del navegador&nbsp;  
  * ofrecer selectores de tiempo para consultar el histórico corto (ej. últimas 2 horas o 24 horas)&nbsp;  
* **back:**  
  * implementar la lectura de datos de rendimiento desde la API de Proxmox&nbsp;  
  * transmitir el flujo de métricas en vivo por instancia&nbsp;  
  * mantener un almacenamiento en caché de histórico corto para responder requerimientos de tendencias&nbsp;  
* **infra:**  
  * verificar que Proxmox mantenga activos los servicios de recolección de métricas&nbsp;  
  * monitorear el consumo de recursos de almacenamiento temporal para evitar excesos de memoria en la infraestructura

&nbsp;

**RF-06: Gestión de puntos de restauración (snapshots):** el sistema debe permitir administrar snapshots de las instancias, esto implica crear con nombre y descripción, listar los snapshots de una instancia y revertir a una snapshot seleccionado (esto es una acción destructiva).

* **front:**&nbsp;  
  * construir vista o modal de gestión de snapshots dentro del detalle de la instancia&nbsp;  
  * diseñar formulario para creación de snapshot con nombre y descripción&nbsp;  
  * mostrar tabla visual con el historial de puntos de restauración&nbsp;  
  * implementar flujo de confirmación estricto con alertas rojas para el botón de revertir&nbsp;  
* **back:**  
  * crear endpoints para listar, crear y solicitar la reversión de un snapshot&nbsp;  
  * tratar la reversión de un snapshot como una tarea asíncrona capturando su UPID y notificando la evolución&nbsp;  
  * validar permisos del usuario sobre la instancia antes de autorizar el rollback&nbsp;  
* **infra:**  
  * otorgar permisos de almacenamiento y snapshots al API de Proxmox&nbsp;  
  * garantizar que los storages configurados en Proxmox tengan habilitado el soporte nativo para snapshots&nbsp;

&nbsp;

**RF-07: Asistente de creación de instancias (Wizard):** el sistema debe proporcionar un asistente paso a paso para simplificar la creación de nuevas VMs o contenedores LXC. El wizard debe guiar al usuario en la selección de tipo de instancia (VM o LXC), recursos (vCPUs, RAM, Disco), imagen/plantilla base (ISO para VMs o template para LXC), y configuración de red (IP, puerta de enlace) la idea con esto es que se oculte la complejidad de la configuración nativa de proxmox. En el momento antes de crear, el back debe validar que proxmox tenga los recursos disponibles suficientes (CPU, RAM, disco) y rechazar en caso de que no haya. Si múltiples usuarios intentan crear instancias simultáneamente y los recursos se agotan en el proceso, el sistema debe rechazar la solicitud de quien llegue un milisegundo tarde, evitando condiciones de carrera.&nbsp;

* **front:**&nbsp;  
  * diseñar el componente wizard multi-paso con navegación progresiva&nbsp;  
  * mostrar indicadores dinámicos de cuotas disponibles en el servidor mientras se ajustan los recursos&nbsp;  
  * manejar el bloqueo del formulario durante el envío y procesar mensajes de error por cuotas insuficientes. Específicamente, debe interceptar el código de error (ej. HTTP 409 Conflicto) y mostrar una alerta amigable explicando al usuario que los recursos que había seleccionado acaban de ser ocupados por otra operación.&nbsp;  
* **back:**  
  * desarrollar el endpoint para el aprovisionamiento de instancias  
  * implementar lógica de validación de cuotas antes de llamar a Proxmox, verificando que la RAM y disco solicitados no superen lo disponible en el nodo&nbsp;  
  * rechazar con error específico (ej. HTTP 409 Conflicto de recursos) si los recursos son insuficientes; si hay recursos, enviar el comando a Proxmox, rastrear el UPID y asociar la instancia al usuario.&nbsp;  
* **infra:**  
  * otorgar permisos de creación de máquinas y asignación de almacenamiento a la API de Proxmox&nbsp;  
  * mantener disponibles las plantillas de LXC e imágenes ISO base en las librerías de almacenamiento de Proxmox&nbsp;

&nbsp;

**RF-08: Registro de auditoría:** el sistema debe registrar de forma inmutable todas las acciones que se hagan a través de la plataforma, esto debe incluir usuario, fecha y hora, acción realizada, instancia afectada (id \+ nombre) y el resultado de la acción (éxito o falla). ~~Esto debe ser consultable y filtrable por usuario, fecha, tipo de acción o instancia**.**~~ Esto debe ser consultable, filtrable y exportable.

* **front:**&nbsp;  
  * maquetar la vista de tabla de auditoría exclusiva para administradores&nbsp;  
  * implementar controles de filtrado por fechas, usuario, tipo de acción y resultado  
  * diseñar paginación eficiente para la lectura de registros&nbsp;  
  * diseñar e integrar un botón de exportación que capture los filtros aplicados actualmente en la vista para solicitar el archivo correspondiente.  
* **back:**  
  * diseñar e implementar la tabla de auditoría en la base de datos con sus correspondientes índices&nbsp;  
  * crear servicio para capturar e insertar cada operación ejecutada de forma inalterable&nbsp;  
  * exponer endpoint con soporte de filtros, paginación y ordenamiento&nbsp;  
  * desarrollar un endpoint dedicado a la exportación que consulte los registros según los filtros solicitados y construya/devuelva el archivo resultante.  
* **infra:**  
  * garantizar la persistencia segura y la estrategia de respaldos periódicos de la base de datos&nbsp;  
  * asegurar el aislamiento de la base de datos para evitar modificaciones externas directas&nbsp;

&nbsp;

**RF-09: Gestión de usuarios y permisos:** los administradores deben poder crear nuevos usuarios, eliminarlos, asignarles roles a un usuario y poder definir a qué instancias tendrá acceso un usuario operador. Al crear una cuenta, el sistema debe generar una contraseña temporal que será enviada al nuevo usuario, exigiéndole el cambio obligatorio de la misma durante su primer inicio de sesión.

* **front:**&nbsp;  
  * diseñar panel de administración de usuarios y asignación de permisos&nbsp;  
  * crear interfaz de selección para vincular usuarios operadores con sus instancias permitidas&nbsp;  
  * maquetar vistas de creación, edición de rol y eliminación de usuarios con modales de confirmación&nbsp;  
* **back:**  
  * desarrollar el CRUD completo de usuarios accesible solo por administradores&nbsp;  
  * generar una contraseña temporal segura que cumpla con las reglas de complejidad al crear la cuenta, enviándola por correo electrónico junto con las instrucciones de acceso.  
  * persistir la nueva cuenta marcando la contraseña como temporal en la base de datos, obligando al sistema a interceptar el primer inicio de sesión para requerir una clave definitiva.  
  * diseñar la tabla intermedia para la relación de usuarios e instancias&nbsp;  
  * implementar lógica de autorización por recurso para verificar si un operador tiene acceso a una instancia solicitada&nbsp;  
  * configurar y garantizar la disponibilidad del servicio de mensajería (SMTP) para la entrega confiable de los correos electrónicos con las credenciales iniciales.  
* **infra:**  
  * asegurar el aislamiento de la lógica de roles dentro de la plataforma sin alterar los usuarios del sistema operativo ni de Proxmox&nbsp;

&nbsp;

**RF-10: Edición de recursos de instancias:** debe permitir a los usuarios (con permisos) modificar la cantidad de vCPUs y RAM asignadas a una instancia (en el momento o después de un reinicio) aca el back debe validar la disponibilidad de recursos antes de aplicar el cambio.&nbsp;

* **front:**&nbsp;  
  * diseñar modal o formulario de edición de recursos dentro del detalle de la instancia&nbsp;  
  * proveer controles de ajuste numérico pre-cargados con los valores actuales&nbsp;  
  * mostrar advertencias visuales si la modificación requiere un reinicio de la instancia&nbsp;  
* **back:**  
  * desarrollar endpoint para reconfigurar RAM y vCPUs de una instancia&nbsp;  
  * calcular la diferencia de recursos solicitados y verificar que el nodo físico posea suficiente RAM libre para cubrir el incremento&nbsp;  
  * rechazar la modificación si no hay recursos o enviar la orden de actualización a Proxmox si es aprobada  
* **infra:**  
  * garantizar que el API Token tenga otorgados los permisos de configuración de CPU y memoria en Proxmox&nbsp;  
  * asegurar que las máquinas virtuales tengan habilitado el soporte de conexión en caliente (hot-plug) si se requiere ajustar recursos sin apagar&nbsp;

&nbsp;

**RF-11: Notificaciones:** el sistema debe emitir notificaciones en la interfaz para notificar al usuario sobre cambios críticos de estado o eventos en el servidor, sin la necesidad de recargar la pantalla, y adicionalmente debe enviar alertas por correo electrónico ante situaciones importantes de alta severidad. Los eventos de los cuales se notifica son cambios de estados de las instancias, creación de nuevas instancias, saturación de recursos y finalización de tareas (notificación de éxito o fallo). Cada notificación debe estar relacionada con una instancia (VM o LXC), la cual debe incluir un botón/enlace que direccione para visualizar el estado de la instancia con botones de acción correspondientes.

* **front:**  
  * implementar un contenedor de notificación flotantes (buscar componente toast) persistente con enrutamiento dinámico  
  * asociar id de la instancia afectada al componente para habilitar navegación  
  * escuchar alertas entrantes desde el back y filtrarlas instancias,&nbsp;  
  * implementar códigos de colores para identificar diferentes eventos  
* **back:**  
  * emitir mensaje notificando un cambio de estado relacionando con la instancia afectada  
  * evaluar las métricas relacionadas al estado de salud y consumo de recursos del nodo físico o cluster de proxmox, para emitir mensaje sobre saturación  
  * emitir alerta sobre resultados de acciones solicitadas por el front para cada instancia  
  * configurar e integrar un servicio de mensajería (SMTP o proveedor externo) garantizando alta disponibilidad para el envío de los correos electrónicos.  
* **infra:**  
  * asegurar el bus de datos internos (Kafka) transmita los eventos de alerta sin demoras ni pérdidas.  
    &nbsp;

**RF-12: Gestión de organizaciones:** el sistema debe soportar múltiples organizaciones operando de forma aislada.

* **front:**  
  * diseñar y maquetar los formularios para la creación de organizaciones y la edición de sus detalles, como el nombre.  
  * implementar la interfaz de gestión de miembros, permitiendo al Administrador crear usuarios en el sistema o enviarles una invitación.  
  * maquetar las vistas de perfil y configuración para asegurar que el dato de la organización asignada se muestre como no modificable para los usuarios estándar.  
* **back:**  
  * procesar la creación de una nueva organización y asignar automáticamente el rol de Administrador inicial al usuario creador.  
  * asociar automáticamente a los nuevos usuarios a la organización del administrador que los creó o invitó.  
  * bloquear y rechazar cualquier petición de usuarios estándar que intente modificar la organización a la que pertenecen.  
  * vincular estrictamente en la base de datos todas las instancias, roles y registros de auditoría a una organización específica.  
  * validar en todos los endpoints de consulta y asignación que un administrador solo pueda visualizar y gestionar los recursos que correspondan a su propia organización.  
  * autorizar la edición de los detalles de la organización únicamente a los usuarios que posean el rol de Administrador.  
* **infra:**  
  * no tienen nada que hacer aca, chau

**RF-13: Recuperación de contraseña:** el sistema debe permitir a los usuarios restablecer su contraseña en caso de olvido, mediante la generación y envío de un código temporal de verificación a su correo electrónico.

* **front:**  
  * diseñar y maquetar el formulario para el ingreso del email y la solicitud de recuperación.  
  * implementar la vista para el ingreso del código de verificación temporal de 6 dígitos.  
  * manejar el flujo visual procesando el resultado de la solicitud y los errores devueltos por el backend.  
* **back:**  
  * recibir el email ingresado, validar su formato y buscar la cuenta asociada a dicho email.  
  * generar una solicitud de recuperación de contraseña y un código temporal de verificación de 6 dígitos.  
  * asociar el código a la cuenta y a la solicitud, definiendo una fecha/hora de expiración.  
  * enviar el código de verificación al email de la cuenta.  
  * invalidar los códigos anteriores cuando se genere uno nuevo y evitar que un código vencido pueda utilizarse.  
  * devolver el resultado de la solicitud y los errores necesarios para que el frontend pueda manejar el flujo.  
* **infra:**  
  * configurar y asegurar el servicio de mensajería (SMTP o servicio de terceros) para garantizar la entrega confiable de los correos electrónicos.  
  * garantizar la persistencia temporal de los códigos y las solicitudes de recuperación en la base de datos o caché.

&nbsp;

# Requerimientos No Funcionales

**RNF-01: Seguridad:**&nbsp;

**Mínimo privilegio:** el back no debe usar las credenciales de administrador (root) de proxmox. Se debe comunicar mediante api tokens de proxmox con permisos limitados a las operaciones necesarias. El token debe tener la separación de privilegios activada.

**Credenciales:** las contraseña se deben almacenar de manera segura (función hash), las claves secretas de 2FA y los tokens de la API de proxmox deben almacenarse de manera segura evitando el texto plano.

* **front:**&nbsp;  
  * manipular únicamente el JWT de sesión emitido por la plataforma y nunca almacenar contraseñas, secretos TOTP ni tokens maestros en texto plano&nbsp;  
  * limpiar el estado de sesión y redirigir al login automáticamente al caducar la sesión&nbsp;  
* **back:**  
  * implementar cifrado simétrico para almacenar la claves secretas de 2FA y la cave del API Token de Proxmox en variables de entorno&nbsp;  
  * utilizar hashing para el almacenamiento de contraseñas de usuarios&nbsp;  
  * garantizar que ningún endpoint exponga secretos TOTP sin cifrar ni tokens o llaves maestras en las respuestas JSON hacia el cliente&nbsp;  
* **infra:**  
  * crear un usuario exclusivo de servicio en Proxmox sin permisos de administración host&nbsp;  
  * generar un API Token asociado con la casilla de separación de privilegios activada&nbsp;  
  * asignar permisos y ACLs específicas al token únicamente sobre los recursos que operará el sistema&nbsp;  
  * garantizar conexiones cifradas HTTPS/TLS para proteger la transmisión de credenciales y codigos TOTP en transito  
    &nbsp;

**RNF-02: Rendimeinto:**&nbsp;

**Concurrencia:** el sistema debe ser capaz de manejar múltiples solicitudes concurrentes (muchos usuarios y muchas operaciones simultáneas) sin degradación del rendimiento.

**Tiempo de respuesta:** el sistema debe estar optimizado para una experiencia de usuario fluida (tiempo de respuestas de máximo 5s en condiciones normales).

* **front:**&nbsp;  
  * optimizar renderizado y memorización de componentes para evitar re-renderizados ante la llegada masiva de eventos&nbsp;  
  * implementar paginación y carga diferida en tablas con alto volumen de datos&nbsp;  
* **back:**  
  * utilizar un marco de trabajo asíncrono y concurrente con bucle de eventos&nbsp;  
  * separar la arquitectura para que la recolección de métricas no frene las peticiones REST&nbsp;  
* **infra:**  
  * asegurar recursos suficientes de memoria y ancho de banda en los servicios de caché y colas&nbsp;  
  * configurar el servidor web o proxy de entrada para gestionar eficientemente múltiples conexiones concurrentes persistentes

&nbsp;

**RNF-03: Usabilidad:**&nbsp;

**Simplicidad y claridad:** la interfaz debe ser limpia y moderna e intuitiva, con el objetivo de reducir la carga de entender que está viendo, pensada especialmente para desarrolladores. Los flujos del wizard deben ser guiados y todas las acciones deben tener confirmación explícita y advertencias claras para prevenir errores humanos.

* **front:**&nbsp;  
  * construir una interfaz homogénea utilizando una librería de componentes de diseño moderna&nbsp;  
  * ocultar parámetros avanzados del kernel o configuraciones complejas de red en los formularios&nbsp;  
  * diseñar modales de confirmación con mensajes de advertencia visuales claros antes de procesar acciones críticas o destructivas&nbsp;  
* **back:**  
  * estandarizar los mensajes de error devueltos traduciendo excepciones técnicas complejas de Proxmox a textos claros&nbsp;  
* **infra:**  
  * desplegar el sistema en un dominio o dirección accesible internamente de forma sencilla para el equipo

&nbsp;

**RNF-04:** **Confiabilidad:** &nbsp;

**Manejo de errores y gestión de operaciones:** manejar todos los posibles errores de la API de proxmox (timeouts, tokens inválidos, recursos no encontrados, operaciones fallidas) para dar al usuario mensajes de error claros para que el usuario entienda qué fue lo que pasó. Todas las operaciones que resultan en una tarea de Proxmox (UPID) deben ser gestionadas de manera asíncrona. El backend debe iniciar la tarea, retornar inmediatamente un ID de tarea al Frontend, monitorear el estado de la tarea en segundo plano, y notificar al front cuando la tarea esté completa o falle. El front debe reflejar este estado de "en progreso" en la UI.

* **front:**&nbsp;  
  * bloquear botones de acciones duplicadas sobre la misma instancia y mostrar indicadores visuales de carga durante tareas en progreso&nbsp;  
  * mostrar notificaciones flotantes adaptativas para informar el resultado final de la operación&nbsp;  
* **back:**  
  * capturar excepciones en las llamadas hacia la API de Proxmox y retornar respuestas estructuradas&nbsp;  
  * diseñar el motor de tareas en segundo plano para sondear UPIDs manejando reintentos y tiempos de espera&nbsp;  
  * asegurar que el sistema mantenga un estado degradado controlado ante caídas temporales de la API de Proxmox sin colapsar&nbsp;  
* **infra:**  
  * configurar políticas de reinicio automático sobre los servicios de base de datos, caché y backend en caso de caídas&nbsp;

&nbsp;

&nbsp;

**RNF-05: Mantenibilidad:** el código debe estar documentado, principalmente la API del back para facilitar el mantenimiento y la incorporación de nuevas funcionalidades en el futuro.

* **front:**&nbsp;  
  * estructurar el código mediante componentes reutilizables y mantener centralizados los servicios de comunicación API y websockets&nbsp;  
* **back:**  
  * implementar arquitectura fácil de entender  
  * configurar la generación automática de documentación interactiva de la API  
* **infra:**  
  * documentar los procedimientos de despliegue, scripts de base de datos y archivos de orquestación de servicios&nbsp;

&nbsp;

&nbsp;

**RNF-06: Compatibilidad:** la interfaz debe ser compatible con navegadores más comunes en las versiones más recientes, navegadores como Google Chrome, Mozilla Firefox, Brave, Microsoft Edge y Safari.

* **front:**&nbsp;  
  * probar y validar el correcto funcionamiento de gráficos, estilos y websockets de manera uniforme en múltiples navegadores modernos&nbsp;  
* **back:**  
  * configurar encabezados de intercambio de recursos de origen cruzado permitiendo peticiones desde orígenes autorizados&nbsp;  
* **infra:**  
  * asegurar que el proxy inverso gestione la negociación de protocolos HTTP y los encabezados necesarios para la actualización a websockets&nbsp;

&nbsp;

&nbsp;

**RNF-07: Escalabilidad:** El sistema debe estar diseñado para soportar la gestión de múltiples nodos Proxmox y escalar horizontalmente si es necesario, sin modificar la lógica central.&nbsp;

* **front:**&nbsp;  
  * diseñar vistas preparadas para soportar la selección o agrupamiento por nodo sin rehacer componentes&nbsp;  
* **back:**  
  * desacoplar la identificación del nodo en la lógica de servicios para permitir enrutamiento dinámico a múltiples endpoints de Proxmox&nbsp;  
* **infra:**  
  * disponer la arquitectura de contenedores y servicios de backend para permitir su réplica o despliegue en múltiples instancias si aumenta la carga&nbsp;

&nbsp;

&nbsp;

# Funcionalidades Excluidas

**Consola remota web:** se elimina esta funcionalidad para mitigar los riesgos de configuración errónea a nivel de líneas de comandos. Los accesos por consola directa quedan reservados para administradores de infraestructura a través de las herramientas de proxmox, esto es para evitar la ejecución de comandos arbitrarios y mitigar riesgos de configuración a nivel de sistema operativo.

&nbsp;

&nbsp;

&nbsp;