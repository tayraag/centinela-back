# Actores

usuario: persona que interactúa a través de la interfaz web

api proxmox: sistema externo que interactúa a través de una api y rest/json

# Casos de uso

### CU-01 autenticarse e iniciar sesión

requerimiento relacionado: RF-01

actor principal: usuario (administrador / operador).

descripción: el usuario ingresa credenciales básicas y, posteriormente, valida su identidad mediante un código de autenticación de doble factor (“FA) para acceder a la plataforma.

transacciones:

1. el usuario solicita la pantalla de ingreso y el sistema la renderiza.  
2. el usuario envía sus credenciales (usuario/contraseña) al backend.  
3. el backend valida las credenciales, y verifica en la base de datos si el usuario tiene el 2FA vinculado.  
4. CONDICIONAL:  
   1. sin vinculación: el backend genera la clave secreta y la URL del código QR, notificando al frontend  
   2. sin vinculación: el frontend renderiza la pantalla con el código QR para que el usuario vincule su aplicacion autenticadora  
   3. con vinculación: el frontend renderiza la pantalla para solicitar el código TOTP de 6 dígitos  
5. el usuario ingresa el código del 2FA y lo envía al backend  
6. el backend valida el código 2FA ingresado, firma el token JWT con la sesion completamente autenticada y responde exitosamente  
7. el frontend almacena el JWT y redirige al dashboard

&nbsp;

### CU-02: visualizar dashboard general del servidor

requerimiento asociado: RF-02.

actor principal: usuario (administrador / operador).

descripción: el usuario consulta el estado de salud e hipervisor (cpu, ram, disco, instancias).

transacciones:

1. el usuario navega a la vista principal/dashboard.  
   2. el frontend solicita el estado de salud al backend.  
   3. el backend consulta la api de proxmox para leer recursos del host.  
   4. proxmox responde con la telemetría del servidor.  
   5. el backend formatea la información y la envía al frontend.  
   6. el frontend actualiza los componentes gráficos del panel.

&nbsp;

### CU-03: filtrar e inventariar instancias (vms y lxc)

requerimiento asociado: RF-03.

actor principal: usuario (administrador / operador).

descripción: muestra la lista filtrable de instancias asociadas al rol del usuario.

transacciones:

1. el usuario solicita la lista de instancias.  
   2. el backend intercepta la solicitud y valida los permisos del rol.  
   3. el backend solicita el inventario a la api de proxmox.  
   4. el backend unifica las propiedades json (vms y lxc).  
   5. el frontend renderiza la tabla de inventario.  
   6. el usuario aplica filtros (tipo, estado o búsqueda por nombre).  
   7. el frontend procesa y filtra la lista.

&nbsp;

### CU-04: ejecutar acción de control de energía (manejo de UPID)

requerimiento asociado: RF-04.

actor principal: usuario (administrador / operador).

descripción: iniciar, apagar, forzar apagado o reiniciar una máquina o contenedor de forma asíncrona.

transacciones:

1. el usuario presiona una acción de energía (ej. apagar).  
   2. la interfaz abre un modal solicitando confirmación explícita.  
   3. el usuario confirma la acción.  
   4. el frontend envía la orden al backend.  
   5. el backend valida los permisos e invoca la orden a proxmox.  
   6. proxmox retorna un identificador de tarea asíncrona (UPID).  
   7. el frontend bloquea los controles de esa instancia y muestra el estado "en progreso".  
   8. el backend monitorea en segundo plano la evolución del upid en proxmox.  
   9. el backend notifica la finalización de la tarea al frontend.  
   10. el frontend desbloquea los botones e informa el resultado.  
   11. el backend registra la acción en la tabla de auditoría.

&nbsp;

### CU-05: consultar telemetría y métricas por instancia

requerimiento asociado: RF-05.

actor principal: usuario (administrador / operador).

descripción: visualización de gráficos de consumo (cpu, ram, disco, red) en tiempo real e histórico corto.

transacciones:

1. el usuario ingresa a la vista de detalle de una instancia.  
   2. el frontend establece suscripción de métricas.  
   3. el backend consulta métricas en vivo e histórico a proxmox/caché.  
   4. el backend emite el flujo de métricas hacia el frontend.  
   5. el frontend renderiza los gráficos de líneas en vivo.  
   6. el usuario selecciona un rango histórico diferente (ej. últimas 2 horas).

&nbsp;

### CU-06: administrar snapshots (puntos de restauración)

requerimiento asociado: RF-06.

actor principal: usuario (administrador / operador).

descripción: crear, listar y revertir un punto de restauración de una instancia.

transacciones:

1. el usuario abre la sección de snapshots de una instancia.  
   2. el backend obtiene la lista de snapshots desde proxmox y la envía al frontend.  
   3. el usuario completa el formulario (nombre y descripción) para crear uno nuevo.  
   4. el backend envía la orden de creación de snapshot a proxmox.  
   5. el usuario solicita la reversión a un snapshot existente.  
   6. el sistema muestra una alerta de confirmación destructiva.  
   7. el backend captura el upid del rollback y notifica la finalización.

&nbsp;

### CU-07: aprovisionar nueva instancia (wizard)

requerimiento asociado: RF-07.

actor principal: usuario (administrador / operador).

descripción: asistente paso a paso para crear vms/lxc ocultando la complejidad con validación previa de cuotas.

transacciones:

1. el usuario inicia el asistente de creación (wizard).  
   2. el usuario selecciona el tipo de instancia (vm o lxc).  
   3. el usuario define la cuota de recursos (vcpus, ram, disco).  
   4. el usuario selecciona la plantilla/iso e ingresa configuración de red.  
   5. el usuario confirma el resumen de creación.  
   6. el backend consulta a proxmox la capacidad libre real del nodo físico.  
   7. el backend valida las cuotas y aprueba/rechaza la solicitud.  
   8. el backend manda la orden a proxmox, rastrea el upid y asocia la vm al usuario.

&nbsp;

### CU-08: consultar registros de auditoría

requerimiento asociado: RF-08.

actor principal: usuario administrador.

descripción: visualización e inspección inalterable de los logs de acciones de la plataforma.

transacciones:

1. el administrador accede a la vista de auditoría.  
   2. el frontend consulta los registros paginados.  
   3. el backend ejecuta la consulta indexada sobre la base de datos.  
   4. el administrador aplica un filtro (por usuario, fecha o acción).  
   5. el backend devuelve la lista ordenada y filtrada.

&nbsp;

### CU-09: gestionar usuarios, roles y permisos de instancia

requerimiento asociado: RF-09.

actor principal: usuario administrador.

descripción: crear o eliminar usuarios, cambiar roles y vincular qué operador puede controlar qué instancia.

transacciones:

1. el administrador accede al panel de usuarios.  
   2. el administrador crea un nuevo usuario y le asigna un rol.  
   3. el backend guarda las credenciales en la base de datos aplicando un hash seguro.  
   4. el administrador selecciona un operador y le asigna un listado de instancias permitidas.  
   5. el backend actualiza la tabla intermedia de permisos en la base de datos.

&nbsp;

### CU-10: modificar recursos de instancia

requerimiento asociado: RF-10.

actor principal: usuario (administrador / operador con permisos).

descripción: reconfiguración de vcpus y ram asignadas a una instancia con validación de disponibilidad.

transacciones:

1. el usuario abre el formulario de modificación de recursos de la máquina.  
   2. el usuario ajusta los valores de vcpus y memoria ram.  
   3. el backend calcula el diferencial de recursos solicitados.  
   4. el backend verifica la ram libre en el servidor físico.  
   5. el backend aprueba la actualización y envía la orden de reconfiguración a proxmox.

### CU-11: notificaciones

requerimiento asociado: RF-11.

actor principal: usuario (administrador / operador con permisos)

descripción:el sistema monitorea el estado de la infraestructura y emite alertas emergentes en tiempo real en la interfaz. Al hacer clic en la notificación relacionada con una instancia, la interfaz redirige automáticamente al usuario a la pantalla de detalle de dicha instancia, donde se presenta las métricas en vivo y el panel de control para ejecutar acciones correctivas como apagar, reiniciar o ajustar recursos.

transacciones:

1. El usuario está navegando en cualquier sección del panel web.  
2. Una máquina virtual se satura o se apaga inesperadamente en Proxmox o un proceso termina  
3. backend detecta evento en proxmox  
4. backend emite evento con tipo de alerta, severidad e instancia afectada  
5. frontend renderiza componente mostrando mensaje y enlace de redireccionamiento  
6. el usuario al hacer clic en ver detalle  
7. el frontend captura el evento de navegación y redirige a la vista específica de la instancia  
8. frontend renderiza métricas en vivo y habilita botones de control  
9. el backend registra  emisión de alerta