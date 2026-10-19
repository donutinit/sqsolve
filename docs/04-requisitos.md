# 04. Requisitos

Prioridad: **M** obligatorio para 1.0, **S** deseable en 1.x, **C** posterior. Cada requisito
se traza a tareas en `21-tareas.md` y a pruebas en `16-pruebas.md`.

## 1. Requisitos funcionales

### 1.1 Instalación y configuración inicial (RF-INS)

| ID | Requisito | Prioridad |
|---|---|---|
| RF-INS-01 | Instalación con un único archivo Docker Compose que levanta PostgreSQL y sqsolve | M |
| RF-INS-02 | En el primer arranque se generan todos los secretos (superusuario, rol de control, clave maestra, certificados TLS) sin intervención | M |
| RF-INS-03 | Asistente web inicial: idioma, zona horaria, cuenta de administrador, dirección anunciada a las estaciones, política de respaldo por defecto, descarga del kit de recuperación | M |
| RF-INS-04 | El asistente solo es accesible mientras no exista un administrador y requiere el código de instalación que se imprime en el registro del contenedor | M |
| RF-INS-05 | Si un volumen obligatorio no está montado o no tiene permisos, sqsolve se niega a arrancar y explica qué falta | M |

### 1.2 Bibliotecas (RF-BIB)

| ID | Requisito | Prioridad |
|---|---|---|
| RF-BIB-01 | Crear una biblioteca con nombre visible, identificador (slug) y notas | M |
| RF-BIB-02 | Crear una biblioteca a partir de una plantilla local (copia de una biblioteca recién inicializada por Resolve) | S |
| RF-BIB-03 | Listar bibliotecas con estado, tamaño, conexiones activas, último respaldo y versión detectada | M |
| RF-BIB-04 | Ver el detalle de una biblioteca: conexión, respaldos, programación, accesos, actividad, ajustes | M |
| RF-BIB-05 | Editar nombre visible, notas, límite de conexiones y versión declarada de Resolve | M |
| RF-BIB-06 | Modo mantenimiento: bloquear conexiones nuevas y, opcionalmente, cerrar las activas | M |
| RF-BIB-07 | Eliminar: respaldo final verificado, bloqueo, renombre a papelera, purga tras N días (30 por defecto); restaurable desde la papelera mientras exista | M |
| RF-BIB-08 | Ver sesiones activas por biblioteca (usuario, IP, aplicación, inicio) y cerrar una sesión | M |
| RF-BIB-09 | Calcular y vigilar la huella de esquema; registrar un evento cuando cambie | M |
| RF-BIB-10 | Adoptar una base de datos de Resolve existente en el servidor (creada fuera de sqsolve) | S |
| RF-BIB-11 | Solo lectura (archivada) si PoC-09 lo valida | C |
| RF-BIB-12 | Mantenimiento programado: `VACUUM (ANALYZE)` semanal por biblioteca | S |

### 1.3 Credenciales y acceso (RF-ACC)

| ID | Requisito | Prioridad |
|---|---|---|
| RF-ACC-01 | Cada biblioteca tiene un rol propietario con contraseña aleatoria fuerte | M |
| RF-ACC-02 | Mostrar la contraseña requiere reautenticación reciente y queda en la auditoría | M |
| RF-ACC-03 | Rotar la contraseña de una biblioteca, con aviso de que las estaciones deben actualizarla | M |
| RF-ACC-04 | Credenciales por persona (roles miembros) con alta, baja y revocación inmediata | S |
| RF-ACC-05 | Reglas de red por biblioteca (lista de CIDR) aplicadas en `pg_hba.conf` | S |
| RF-ACC-06 | Instrucciones de conexión por sistema operativo con valores listos para copiar | M |
| RF-ACC-07 | Llave `.resolvedbkey` descargable (experimental, desactivada por defecto) | C, depende de PoC-05 |
| RF-ACC-08 | Portal de conexión para colaboradores con acceso solo a sus bibliotecas | S |

### 1.4 Respaldos (RF-RES)

| ID | Requisito | Prioridad |
|---|---|---|
| RF-RES-01 | Respaldo manual de una biblioteca | M |
| RF-RES-02 | Programación independiente por biblioteca (presets y expresión cron, zona horaria) | M |
| RF-RES-03 | Retención configurable: últimos N, diarios, semanales, mensuales, mínimo verificado | M |
| RF-RES-04 | Catálogo con fecha, tamaño, duración, estado, tipo, verificación, versión de PostgreSQL, huella | M |
| RF-RES-05 | Verificación nivel 1 (suma SHA-256 e índice legible) tras cada respaldo | M |
| RF-RES-06 | Verificación nivel 2 (restauración de prueba con conteos) programada, semanal por defecto | M |
| RF-RES-07 | Restaurar como biblioteca nueva | M |
| RF-RES-08 | Restaurar reemplazando, con respaldo previo, intercambio y reversión durante N días | M |
| RF-RES-09 | Descargar un respaldo | M |
| RF-RES-10 | Importar respaldos externos (subida o carpeta de importación) con validación | M |
| RF-RES-11 | Fijar un respaldo para que la retención nunca lo borre | M |
| RF-RES-12 | Previsualizar lo que la retención va a borrar | M |
| RF-RES-13 | Copia externa cifrada con restic (ruta local, SFTP, servidor REST, S3 compatible) | S |
| RF-RES-14 | Respaldo del plano de control (SQLite) diario en el volumen de respaldos | M |
| RF-RES-15 | Reconstruir el catálogo desde los manifiestos del volumen de respaldos | M |
| RF-RES-16 | Recuperación a un punto en el tiempo con pgBackRest | C |

### 1.5 Tareas y automatización (RF-TAR)

| ID | Requisito | Prioridad |
|---|---|---|
| RF-TAR-01 | Toda operación larga es una tarea persistente con estado, progreso y registro | M |
| RF-TAR-02 | Vista de tareas: en cola, en curso, completadas, fallidas, interrumpidas, canceladas | M |
| RF-TAR-03 | Cancelar una tarea en curso cuando su tipo lo permite | M |
| RF-TAR-04 | Tras un reinicio, las tareas en curso pasan a "interrumpida" y se limpian sus restos | M |
| RF-TAR-05 | Política de ejecuciones perdidas: ejecutar una vez al arrancar si la última se perdió dentro de una ventana | M |
| RF-TAR-06 | Tareas de mantenimiento predeterminadas (`11-respaldos-y-automatizacion.md` §12) | M |
| RF-TAR-07 | Webhooks de eventos (antes y después de operaciones) firmados con HMAC | M |
| RF-TAR-08 | Hooks ejecutables en un contenedor aislado | S |

### 1.6 Notificaciones (RF-NOT)

| ID | Requisito | Prioridad |
|---|---|---|
| RF-NOT-01 | Canales: webhook genérico | M |
| RF-NOT-02 | Canales: correo SMTP y ntfy | S |
| RF-NOT-03 | Reglas por tipo de evento y severidad; botón de prueba | M |
| RF-NOT-04 | Agrupación de avisos repetidos | S |

### 1.7 Usuarios del panel (RF-USR)

| ID | Requisito | Prioridad |
|---|---|---|
| RF-USR-01 | Cuentas locales con roles Administrador, Operador y Lector | M |
| RF-USR-02 | Segundo factor TOTP opcional por cuenta, obligatorio configurable para administradores | M |
| RF-USR-03 | Tokens de API con alcance y caducidad | M |
| RF-USR-04 | Recuperación de acceso por CLI dentro del contenedor | M |
| RF-USR-05 | Rol Colaborador (portal de conexión) | S |
| RF-USR-06 | Inicio de sesión con OIDC (proveedor autohospedado) | C |
| RF-USR-07 | Llaves de acceso WebAuthn | C |

### 1.8 Observación y auditoría (RF-OBS)

| ID | Requisito | Prioridad |
|---|---|---|
| RF-OBS-01 | Panel de salud: PostgreSQL, espacio de cada volumen, certificados, últimas tareas | M |
| RF-OBS-02 | Registro de eventos consultable y filtrable | M |
| RF-OBS-03 | Auditoría encadenada por hash de toda acción administrativa | M |
| RF-OBS-04 | Exportar auditoría y eventos en JSON Lines | M |
| RF-OBS-05 | Paquete de diagnóstico sin secretos | M |
| RF-OBS-06 | Métricas en formato Prometheus (opcional, con token) | S |

### 1.9 Actualizaciones y compatibilidad (RF-ACT)

| ID | Requisito | Prioridad |
|---|---|---|
| RF-ACT-01 | Migraciones automáticas del esquema de SQLite con copia previa | M |
| RF-ACT-02 | Al arrancar, comprobar versión de PostgreSQL y del directorio de datos y negarse a continuar si no coinciden | M |
| RF-ACT-03 | Ventana de actualización de Resolve: respaldos verificados de todas las bibliotecas y lista de pasos | M |
| RF-ACT-04 | Mostrar la matriz de compatibilidad incluida en la versión instalada | M |
| RF-ACT-05 | Asistente de migración lógica a una versión mayor nueva de PostgreSQL | C |
| RF-ACT-06 | Migración de bibliotecas desde otro servidor PostgreSQL | S |

## 2. Requisitos no funcionales

### 2.1 Fiabilidad e integridad (RNF-FIA)

| ID | Requisito |
|---|---|
| RNF-FIA-01 | Ninguna operación de sqsolve escribe sobre la base activa de una biblioteca, salvo `GRANT`, `ALTER DATABASE` de conexión y los renombres del intercambio |
| RNF-FIA-02 | Un corte de energía en cualquier momento deja el sistema en un estado del que se recupera solo al arrancar, sin pérdida de bibliotecas ni de respaldos completos |
| RNF-FIA-03 | Un respaldo solo cuenta como válido si su archivo, su manifiesto y su suma coinciden |
| RNF-FIA-04 | El volumen de respaldos se identifica con un archivo marcador; si falta, no se escribe nada |
| RNF-FIA-05 | La retención nunca deja una biblioteca activa sin al menos un respaldo verificado |
| RNF-FIA-06 | El plano de control funciona con PostgreSQL caído (para diagnosticar y restaurar) |

### 2.2 Seguridad (RNF-SEG)

| ID | Requisito |
|---|---|
| RNF-SEG-01 | Ningún rol administrativo de PostgreSQL acepta conexiones por red; solo por socket local |
| RNF-SEG-02 | Contraseñas de PostgreSQL con `scram-sha-256` exclusivamente |
| RNF-SEG-03 | Panel solo por HTTPS; HTTP redirige |
| RNF-SEG-04 | Contraseñas del panel con Argon2id; sesiones con cookie `Secure`, `HttpOnly`, `SameSite=Strict` y token CSRF |
| RNF-SEG-05 | Contenedor de sqsolve sin root, con sistema de archivos de solo lectura, sin capacidades y sin socket de Docker |
| RNF-SEG-06 | Cero ejecuciones de shell; procesos externos con lista blanca de binarios |
| RNF-SEG-07 | Secretos recuperables cifrados con XChaCha20-Poly1305 y clave maestra en archivo aparte |
| RNF-SEG-08 | Política de seguridad de contenido estricta sin scripts en línea |
| RNF-SEG-09 | Operaciones peligrosas con confirmación tecleada, reautenticación reciente y auditoría |

### 2.3 Operación (RNF-OPE)

| ID | Requisito |
|---|---|
| RNF-OPE-01 | Imágenes para `linux/amd64` y `linux/arm64` |
| RNF-OPE-02 | Arranque del contenedor de sqsolve en menos de 5 s con PostgreSQL ya disponible |
| RNF-OPE-03 | Consumo en reposo de sqsolve menor a 100 MB de RAM |
| RNF-OPE-04 | Sin acceso a Internet en ninguna función esencial; las únicas salidas son las que configura el administrador (notificaciones, copias externas) |
| RNF-OPE-05 | Toda la configuración de arranque por variables de entorno documentadas; el resto en la interfaz |
| RNF-OPE-06 | Actualizar es cambiar la etiqueta de imagen y recrear contenedores |

### 2.4 Usabilidad (RNF-USA)

| ID | Requisito |
|---|---|
| RNF-USA-01 | Interfaz en español e inglés desde 1.0 |
| RNF-USA-02 | Diseño adaptable desde 360 px de ancho |
| RNF-USA-03 | WCAG 2.2 nivel AA |
| RNF-USA-04 | Cada error muestra qué pasó, por qué y qué hacer, con un código `SQS-` buscable en la documentación |
| RNF-USA-05 | p95 de respuesta de páginas menor a 300 ms con 50 bibliotecas y 5000 respaldos en el catálogo |

### 2.5 Mantenibilidad (RNF-MAN)

| ID | Requisito |
|---|---|
| RNF-MAN-01 | Cobertura de pruebas de `internal/` mayor o igual a 80 % en líneas, 100 % en validadores de identificadores, retención e intercambio |
| RNF-MAN-02 | Dependencias de Go directas acotadas a las de `06-stack.md` |
| RNF-MAN-03 | Compilación reproducible: dos compilaciones del mismo commit producen el mismo binario |
| RNF-MAN-04 | Compilable sin Internet con `go mod vendor` |
| RNF-MAN-05 | API descrita en OpenAPI 3.1 y validada en pruebas |

### 2.6 Compatibilidad (RNF-COM)

| ID | Requisito |
|---|---|
| RNF-COM-01 | Resolve 21.1.x soportado; 20.3.2 soportado si PoC-02 pasa |
| RNF-COM-02 | PostgreSQL 18 empaquetado; modo servidor externo con 16 o superior (2.0) |
| RNF-COM-03 | Navegadores: dos últimas versiones mayores de Firefox, Chromium y Safari |
| RNF-COM-04 | Docker Engine 24 o superior con Compose v2 |
