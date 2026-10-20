# 21. Desglose de tareas

Cada tarea es ejecutable por un agente de programación con este documento, los referenciados y
`AGENTS.md`. Una tarea está terminada cuando cumple sus criterios **y** la definición de terminado
global de `AGENTS.md`.

## 1. Estructura de paquetes

```
cmd/sqsolve/main.go          subcomandos: serve, init, doctor, healthcheck, admin, library, backup, restore, catalog, audit, version
internal/config              variables SQSOLVE_*, validación
internal/errs                catálogo de códigos SQS-, tipos de error
internal/logx                slog, redacción, búfer circular
internal/secret              tipo secret.String
internal/secrets             archivos de secretos, cifrado AEAD, clave maestra
internal/fsx                 escritura atómica, marcadores, statfs
internal/execx               ejecución de binarios permitidos, PGPASSFILE, cancelación de hijos propios
internal/store               SQLite, migraciones, repositorios por tabla
internal/pg                  conexión por socket, ident, DDL seguro, consultas de catálogo
internal/pgconf              postgresql.conf, pg_hba.conf, certificados, recarga
internal/jobs                cola, workers, bloqueos, pasos, registros
internal/scheduler           cron, presets, ejecuciones perdidas
internal/events              bus en memoria, persistencia de eventos
internal/audit               registro encadenado, verificación, exportación
internal/auth                usuarios, sesiones, CSRF, TOTP, tokens, política
internal/library             ciclo de vida, credenciales, sesiones, huella
internal/backup              respaldo, manifiesto, verificación, catálogo, retención, restauración
internal/notify              webhook, SMTP, ntfy, reglas, agrupación
internal/health              estado, indicadores, doctor, diagnóstico
internal/compat              huellas conocidas con lo verificado por versión
internal/drp                 exportación experimental de .drp y .drx desde la biblioteca (ADR 0019)
internal/api                 handlers JSON v1
internal/web                 handlers HTML, plantillas, i18n, recursos embebidos
internal/reconcile           reconciliación al arranque
internal/clock               reloj inyectable para pruebas
```

## 2. Convenciones para cada tarea

- Rama `sqs-NNN-nombre-corto`. Un PR por tarea salvo que la tarea diga lo contrario.
- Primero las pruebas que fijan el comportamiento descrito; luego la implementación.
- Al terminar, marcar los criterios con `[x]`, y actualizar la tabla de estado (§12) con la fecha
  en el mismo PR.

## 3. Fase 0 (0.0): investigación

### SQS-020 Compose de laboratorio
- **Depende de:** nada.
- **Entregables:** `deploy/lab/compose.yaml` con `postgres:18.6-trixie` fijado por digest,
  `log_statement=all`, `log_connections=on`, `log_disconnections=on`, `log_lock_waits=on`,
  `pg_hba` con `scram-sha-256` abierto a la LAN; `deploy/lab/README.md` con cómo levantarlo, leer
  registros y las consultas de `07` §7 y §9 para copiar en `psql`.
- **Criterios:** [ ] levanta con `docker compose up -d`; [ ] un cliente `psql` de otra máquina
  conecta con SCRAM; [ ] los registros muestran conexiones y sentencias; [ ] las variantes por
  versión (16, 17) se seleccionan con una variable.

### SQS-001 a SQS-010 Ejecutar PoC-01 a PoC-10
- **Recorte del 2026-10-19:** para la 1.0 se ejecutan PoC-01, 03, 04, 06, 08 y 09, y PoC-02 solo
  con Resolve 21.1.x y PostgreSQL 18. PoC-05, 07 y 10 y el resto de la matriz pasan a 1.1 y se
  documentan como REPORTADO. El laboratorio es mininas (sqsolve, PostgreSQL 18) y SQS-020 queda
  en un compose mínimo solo si alguna PoC lo exige. Lo que no requiere clics en Resolve
  (roles, bases, respaldos, consultas, la API de scripting de Resolve) lo corre el agente.
- **Depende de:** SQS-020.
- **Entregables:** un informe por PoC en `docs/poc/` según `02-pruebas-de-concepto.md`; cambios
  de estado en `01-investigacion.md` §14 y en `14-compatibilidad.md`; ADR nuevos o actualizados
  cuando una decisión cambie.
- **Criterios:** [ ] cada informe tiene versiones exactas, pasos, resultados literales y decisión;
  [ ] ningún informe contiene esquemas ni volcados de Resolve; [ ] la tabla "Bloquea" de 02
  refleja qué quedó desbloqueado.
- **Nota:** las ejecuta Diego en sus estaciones, o un agente le guía paso a paso.

## 4. Versión 0.1: esqueleto

### SQS-040 Esqueleto del repositorio
- **Depende de:** nada (puede empezar en paralelo con la fase 0).
- **Entregables:** `go.mod` (`go 1.26`), `cmd/sqsolve` con `version`, `Makefile` (`check`,
  `test-integration`, `image`, `vendor-check`), `LICENSE` (texto oficial AGPL-3.0),
  `CONTRIBUTING.md`, `SECURITY.md`, `CHANGELOG.md`, encabezados SPDX, flujo de CI `check`.
- **Criterios:** [ ] `make check` pasa en limpio; [ ] `sqsolve version` imprime versión, commit
  y versión de Go; [ ] CI corre en push y PR.

### SQS-041 Dockerfile e imagen multiarquitectura
- **Depende de:** SQS-040.
- **Referencias:** ADR 0016, `17` §5.
- **Criterios:** [ ] imagen basada en `postgres:18.x-trixie@digest` con `sqsolve` en
  `/usr/local/bin`; [ ] `pg_dump --version` dentro de la imagen coincide en versión mayor con el
  servidor; [ ] usuario por defecto sin privilegios; [ ] etiquetas OCI; [ ] compila para amd64 y
  arm64; [ ] dos compilaciones del mismo commit dan el mismo binario.

### SQS-100 Configuración de arranque
- **Paquete:** `internal/config`. **Referencias:** `12` §4.
- **Criterios:** [ ] todas las variables de `12` §4 con valor por defecto y validación; [ ]
  errores con código `SQS-C...` y nombre de la variable; [ ] pruebas de cada validación.

### SQS-101 Catálogo de errores
- **Paquete:** `internal/errs`. **Referencias:** `05` §8.
- **Criterios:** [ ] tipo `Error` con código, mensaje para personas (es, en), causa y estado HTTP;
  [ ] generador de `docs/manual/errores.md` desde la tabla; [ ] prueba que falla si un código no
  tiene ambos idiomas o se repite.

### SQS-102 Registros y redacción
- **Paquetes:** `internal/logx`, `internal/secret`. **Referencias:** `13` §1.
- **Criterios:** [ ] `secret.String` imprime `[redactado]` en `fmt`, `slog` y JSON; [ ] el
  manejador redacta claves sensibles; [ ] búfer circular de 2000 líneas para diagnóstico.

### SQS-103 Utilidades de archivos
- **Paquete:** `internal/fsx`.
- **Criterios:** [ ] `WriteFileAtomic` (`.partial`, `fsync`, `rename`, `fsync` de directorio);
  [ ] `CheckMarker(dir)` con `instance_id`; [ ] `Statfs(dir)`; [ ] pruebas con fallos inyectados
  en cada paso.

### SQS-104 Ejecución de procesos externos
- **Paquete:** `internal/execx`. **Referencias:** `08` T-07.
- **Criterios:** [ ] lista blanca de binarios por ruta absoluta; [ ] `PGPASSFILE` temporal 0600
  creado y borrado; [ ] proceso en grupo propio; cancelar envía SIGTERM y a los 10 s SIGKILL solo a
  ese grupo; [ ] captura de salida con límite; [ ] pruebas con un binario de prueba propio, sin
  señales a procesos ajenos.

### SQS-105 Secretos
- **Paquete:** `internal/secrets`. **Referencias:** `08` §6, ADR 0014.
- **Criterios:** [ ] leer secretos de archivos y rechazar permisos más abiertos que 0400 o 0600;
  [ ] `Seal`/`Open` con XChaCha20-Poly1305 y datos adicionales (tabla, columna, id de clave); [ ]
  rotación de clave maestra re-cifrando en una transacción; [ ] pruebas de alteración.

### SQS-106 Almacén SQLite y migraciones
- **Paquete:** `internal/store`. **Referencias:** `07` §11.
- **Criterios:** [ ] WAL, `foreign_keys`, un escritor serializado; [ ] migración inicial con todas
  las tablas de `07` §11; [ ] `VACUUM INTO` antes de migrar; [ ] suma de cada migración registrada
  y verificada; [ ] `PRAGMA integrity_check` expuesto para salud.

### SQS-107 `sqsolve init`
- **Depende de:** SQS-103, SQS-105, SQS-109. **Referencias:** `12` §5.
- **Criterios:** [ ] idempotente (correr dos veces no cambia nada); [ ] genera solo lo que falta;
  [ ] se niega a adoptar un volumen de respaldos con contenido y sin marcador; [ ] permisos y
  dueños según `12` §5, incluida la llave TLS de PostgreSQL en root:999 0640; [ ] imprime el código
  de instalación si no hay administrador; [ ] sin red.

### SQS-108 Cliente de control de PostgreSQL
- **Paquete:** `internal/pg`. **Referencias:** `07` §2, §3, §7.
- **Criterios:** [ ] conexión por socket con `sqsolve_admin` y, aparte, con superusuario solo
  desde funciones marcadas y auditadas; [ ] validadores de identificadores con fuzzing; [ ]
  constructor de DDL que solo acepta identificadores validados; [ ] consultas de catálogo:
  bases, roles, tamaños, sesiones, colación.

### SQS-109 Generación de configuración de PostgreSQL
- **Paquete:** `internal/pgconf`. **Referencias:** `07` §10.
- **Criterios:** [ ] `postgresql.conf` con los parámetros de la tabla; [ ] `pg_hba.conf` desde el
  catálogo, con rechazos administrativos primero y roles por nombre; [ ] validación con
  `pg_hba_file_rules` antes de recargar y copia `.prev`; [ ] certificado autofirmado; [ ] pruebas
  de golden files.

### SQS-110 Arranque de PostgreSQL y rol de control
- **Depende de:** SQS-108, SQS-109.
- **Criterios:** [ ] crea `sqsolve_admin` con atributos y membresías de `07` §3 una sola vez; [ ]
  intenta `GRANT EXECUTE ON FUNCTION pg_reload_conf()` y registra si quedó disponible; [ ] prueba
  de integración: `sqsolve_admin` y `postgres` rechazados por TCP.

### SQS-111 Cola de tareas
- **Paquete:** `internal/jobs`. **Referencias:** `11` §11, `05` §5.
- **Criterios:** [ ] estados y transiciones de `11` §11; [ ] prioridades; [ ] bloqueos por
  biblioteca de lectura y escritura; [ ] semáforo de tareas pesadas; [ ] pasos con diario en
  `job_steps`; [ ] cancelación; [ ] pruebas de concurrencia con `-race`.

### SQS-112 Programador
- **Paquete:** `internal/scheduler`. **Referencias:** `11` §10.
- **Criterios:** [ ] cron de 5 campos y presets; [ ] zona horaria y horario de verano; [ ]
  desplazamiento por biblioteca; [ ] ejecuciones perdidas con ventana; [ ] reloj inyectable.

### SQS-113 Eventos
- **Paquete:** `internal/events`. **Referencias:** `10` §4.
- **Criterios:** [ ] tipos de `10` §4 como constantes; [ ] persistencia con retención; [ ] bus
  para SSE y notificaciones.

### SQS-114 Auditoría
- **Paquete:** `internal/audit`. **Referencias:** `08` §9.
- **Criterios:** [ ] cadena de hashes con JSON canónico; [ ] verificación completa e incremental;
  [ ] exportación JSON Lines; [ ] prueba de alteración en cualquier posición.

### SQS-115 Salud, `doctor` y `healthcheck`
- **Paquete:** `internal/health`. **Referencias:** `13` §3, §4, §7.
- **Criterios:** [ ] `/healthz`, `/readyz`; [ ] `sqsolve healthcheck` para Docker; [ ] `doctor`
  con las comprobaciones disponibles en 0.1 (las demás se agregan con cada área).

### SQS-116 Reconciliación al arranque
- **Paquete:** `internal/reconcile`. **Referencias:** `05` §6.4, `11` §7.3, §8.
- **Criterios:** [ ] marco de reconciliación con un paso por área registrable; [ ] tareas en curso
  a `interrupted`; [ ] cada área registra su limpieza (las de respaldos y restauración se agregan
  en sus tareas).

### SQS-117 Servidor HTTP base
- **Paquete:** `internal/web` (base). **Referencias:** `08` §3 T-05, RNF-SEG-03.
- **Criterios:** [ ] TLS con certificado autofirmado propio renovable o cargado; [ ] tiempos de
  espera de lectura, escritura e inactividad; [ ] cabeceras de seguridad y CSP; [ ] identificador
  por petición; [ ] redirección opcional desde HTTP.

### SQS-030 Compose de referencia
- **Depende de:** SQS-041, SQS-107, SQS-110. **Referencias:** `12` §3.
- **Criterios:** [ ] `deploy/compose.yaml` funcional; [ ] prueba de CI que lo levanta y espera
  `readyz`; [ ] reinicio de ambos contenedores sin pérdida; [ ] pegado tal cual en "Install via
  YAML" de TrueNAS funciona (prueba manual registrada).

### SQS-031 Endurecimiento del contenedor de PostgreSQL
- **Depende de:** SQS-030.
- **Criterios:** [ ] lista mínima de capacidades que permite arrancar e inicializar el clúster;
  [ ] `no-new-privileges`; [ ] documentado en el YAML.

## 5. Versión 0.2: acceso

### SQS-150 Usuarios del panel
- **Paquete:** `internal/auth`. **Referencias:** `08` §4.
- **Criterios:** [ ] alta, edición, desactivación; [ ] Argon2id con parámetros en el hash; [ ]
  lista local de contraseñas comunes; [ ] pruebas.

### SQS-151 Sesiones, CSRF y límites
- **Criterios:** [ ] tokens de 256 bits guardados como hash; [ ] caducidad por inactividad y
  absoluta; [ ] reautenticación con marca `reauth_at`; [ ] CSRF por sincronización; [ ] límite de
  intentos por IP y cuenta con espera exponencial; [ ] evento `auth.login_failed_burst`.

### SQS-152 TOTP y códigos de recuperación
- **Criterios:** [ ] RFC 6238 con vectores; [ ] QR generado en el servidor (PNG o SVG propio, sin
  servicios externos); [ ] 10 códigos de un solo uso; [ ] opción global de TOTP obligatorio para
  administradores.

### SQS-153 Tokens de API
- **Criterios:** [ ] prefijo `sqs_`, mostrado una vez, hash SHA-256; [ ] alcances y caducidad; [ ]
  `last_used_at`; [ ] revocación.

### SQS-154 Política de autorización
- **Referencias:** `08` §5.
- **Criterios:** [ ] tabla única `Authorize(actor, action, resource)`; [ ] prueba de la matriz
  completa; [ ] usada por web, API y CLI.

### SQS-155 Asistente inicial (servicio)
- **Referencias:** `09` §3.1, RF-INS-03, RF-INS-04.
- **Criterios:** [ ] código de instalación de un solo uso; [ ] pasos persistidos (se puede
  recargar la página); [ ] se cierra al crear el administrador; [ ] opción "recuperar instalación
  existente" reservada para SQS-600.

### SQS-156 CLI de administración
- **Referencias:** `10` §7.
- **Criterios:** [ ] `admin create`, `reset-password`, `disable-totp`; [ ] auditoría con actor
  `cli`.

## 6. Versión 0.3: bibliotecas

**Bloqueadas por PoC-01, PoC-06 y PoC-08.**

### SQS-200 Modelo de biblioteca y estados
- **Paquete:** `internal/library`. **Referencias:** `07` §1, §2, §4.
- **Criterios:** [ ] máquina de estados con transiciones permitidas y prueba de las prohibidas;
  [ ] validación de slug y nombre visible; [ ] repositorio en SQLite.

### SQS-201 Creación con ventana de inicialización (E1)
- **Depende de:** SQS-200, SQS-210, SQS-211, SQS-111.
- **Referencias:** `07` §5 E1, `05` §6.1.
- **Criterios:** [ ] tarea `library.create` y `library.init_watch`; [ ] detección de base
  inicializada; [ ] retirada de `CREATEDB` al detectar, al vencer y en la reconciliación; [ ]
  ajustes de propiedad según el informe de PoC-01; [ ] reporte de bases extra creadas por el rol;
  [ ] prueba de integración con un "Resolve simulado" sintético.

### SQS-202 Creación con base precreada (E0)
- **Condicional:** solo si PoC-01 la valida.

### SQS-210 Credencial del propietario
- **Referencias:** `07` §6.
- **Criterios:** [ ] generación con el alfabeto definido; [ ] guardada cifrada; [ ] revelar con
  reautenticación y auditoría; [ ] rotar con aviso de estaciones conectadas.

### SQS-211 Reglas de `pg_hba.conf` por biblioteca
- **Depende de:** SQS-109.
- **Criterios:** [ ] cada cambio de bibliotecas o credenciales regenera y recarga; [ ] una
  biblioteca no puede conectarse a otra (prueba de integración); [ ] fallo de validación deja la
  versión anterior activa.

### SQS-220 Sesiones activas
- **Referencias:** `07` §7.
- **Criterios:** [ ] listado por biblioteca; [ ] cerrar solo sesiones de roles de esa biblioteca;
  [ ] auditoría.

### SQS-221 Mantenimiento
- **Criterios:** [ ] bloquear y desbloquear con `ALLOW_CONNECTIONS`; [ ] opción de cerrar sesiones;
  [ ] los respaldos siguen funcionando en mantenimiento.

### SQS-230 Eliminación, papelera y purga
- **Depende de:** SQS-301 (respaldo final).
- **Referencias:** RF-BIB-07.
- **Criterios:** [ ] respaldo `pre_delete` verificado antes de cualquier cambio; [ ] renombre a
  papelera y `NOLOGIN` del rol; [ ] recuperar; [ ] purga programada con segunda comprobación de
  respaldo; [ ] confirmación tecleada y reautenticación exigidas por el servicio, no solo por la
  interfaz.

### SQS-240 Huella de esquema
- **Referencias:** `07` §9. **Bloqueada por PoC-03 para la parte de "versiones conocidas".**
- **Criterios:** [ ] cálculo determinista con prueba sobre un esquema sintético; [ ] vigilancia
  cada 5 min; [ ] evento al cambiar; [ ] registro de huellas conocidas en `internal/compat`.

## 7. Versiones 0.4 y 0.5: respaldos y restauración

**Bloqueadas por PoC-04, PoC-09 y PoC-10.**

### SQS-300 Volumen de respaldos
- **Referencias:** `11` §3, RNF-FIA-04.
- **Criterios:** [ ] comprobación de marcador antes de cada escritura; [ ] cálculo de espacio
  requerido; [ ] errores `SQS-S001` y `SQS-B012`.

### SQS-301 Respaldo de una biblioteca
- **Depende de:** SQS-104, SQS-111, SQS-300.
- **Referencias:** `11` §4.
- **Criterios:** [ ] pasos 1 a 8 de `11` §4 como pasos de tarea; [ ] instantánea exportada y
  conteos en la misma transacción; [ ] `--lock-wait-timeout`; [ ] ningún archivo final sin
  manifiesto; [ ] cancelable; [ ] prueba de integración que compara conteos del manifiesto con la
  base.

### SQS-302 Manifiesto
- **Referencias:** `10` §8.1.
- **Criterios:** [ ] `api/schemas/manifest.v1.json`; [ ] escritura atómica; [ ] lectura tolerante
  a campos desconocidos.

### SQS-303 Verificación L1
- **Criterios:** [ ] SHA-256 leído del disco; [ ] `pg_restore --list`; [ ] fallo limpia y avisa.

### SQS-304 Catálogo y reconstrucción
- **Criterios:** [ ] catálogo en SQLite; [ ] `catalog rebuild` desde manifiestos; [ ] detección de
  huérfanos en ambos sentidos; [ ] tarea diaria de coherencia.

### SQS-310 Políticas y programación por biblioteca
- **Depende de:** SQS-112.
- **Criterios:** [ ] política por defecto al crear biblioteca; [ ] presets y cron; [ ] vista previa
  de próximas ejecuciones.

### SQS-311 Retención
- **Referencias:** `11` §6.
- **Criterios:** [ ] función pura con pruebas de tabla y de propiedades; [ ] vista previa; [ ]
  borrado en dos fases; [ ] fijados y tipos protegidos; [ ] `min_verified`.

### SQS-312 Descarga e importación
- **Criterios:** [ ] descarga con autorización y auditoría; [ ] importación desde `_import/` y por
  subida con límite de tamaño configurable; [ ] validación con `pg_restore --list`; [ ] manifiesto
  generado para importados; [ ] rutas validadas contra `_import/` (sin `..` ni enlaces fuera).

### SQS-313 Respaldo del plano de control
- **Criterios:** [ ] `VACUUM INTO` diario a `_system/`; [ ] exportación mensual de auditoría; [ ]
  retención de 30 copias.

### SQS-320 Verificación L2
- **Referencias:** `11` §5.
- **Criterios:** [ ] restauración en `sqsolve_verify_*`; [ ] comparación de conteos y huella; [ ]
  limpieza siempre, incluida la reconciliación; [ ] omisión con aviso si no hay espacio.

### SQS-330 Restaurar como biblioteca nueva
- **Criterios:** [ ] crea rol y base nuevos; [ ] `pg_restore --no-owner --no-privileges
  --role=<slug> --single-transaction --exit-on-error`; [ ] verificación contra el manifiesto; [ ]
  biblioteca activa con su propia política.

### SQS-331 Restaurar por intercambio
- **Referencias:** `11` §7.2.
- **Criterios:** [ ] cada estado del diagrama como paso con diario; [ ] respaldo `pre_restore`;
  [ ] espera de sesiones con tiempo máximo; [ ] el servicio exige confirmación y reautenticación;
  [ ] prueba de integración con clientes conectados.

### SQS-332 Recuperación del intercambio
- **Referencias:** `11` §7.3.
- **Criterios:** [ ] los cuatro casos de la tabla con prueba de inyección de fallos en cada punto;
  [ ] ningún camino borra `slug` ni `__prev`.

### SQS-333 Reversión y purga de estados previos
- **Referencias:** `11` §7.4.
- **Criterios:** [ ] revertir dentro del plazo; [ ] purga solo con respaldo verificado del estado
  previo.

### SQS-334 Compatibilidad del respaldo antes de restaurar
- **Referencias:** `11` §7.5.
- **Criterios:** [ ] rechazo por versión de `pg_dump`; [ ] avisos por huella distinta con el texto
  de la tabla.

### SQS-340 Tareas de mantenimiento predeterminadas
- **Referencias:** `11` §12.
- **Criterios:** [ ] todas las tareas de la tabla creadas en el primer arranque y editables; [ ]
  `VACUUM (ANALYZE)` con `vacuumdb`.

## 8. Versión 0.6: API e interfaz

Recorte del 2026-10-19: SQS-400, SQS-410 y SQS-520 pasan a 1.1. La 1.0 se queda con la API
JSON tal como está, la prueba de extremo a extremo por HTTP y las pruebas de plantilla.

### SQS-400 Contrato OpenAPI (1.1)
- **Referencias:** `10` §1, §2, §3.
- **Criterios:** [ ] `api/openapi.yaml` 3.1 completo para 1.0 antes de los handlers; [ ] errores
  RFC 9457; [ ] esquemas reutilizables.

### SQS-401 a SQS-404 Handlers de la API
- **SQS-401** bibliotecas, conexión, credenciales, sesiones; **SQS-402** respaldos, restauración,
  importación; **SQS-403** tareas, registros y SSE; **SQS-404** usuarios, tokens, ajustes,
  eventos, auditoría, mantenimiento.
- **Criterios comunes:** [ ] autorización por la política única; [ ] idempotencia y `ETag` donde
  aplica; [ ] 202 con tarea para operaciones largas; [ ] pruebas por endpoint.

### SQS-410 Pruebas de contrato (1.1)
- **Criterios:** [ ] cada respuesta de la batería de integración se valida contra OpenAPI; [ ]
  un endpoint sin especificar hace fallar CI.

### SQS-420 Webhooks y base de notificaciones
- **Paquete:** `internal/notify`. **Referencias:** `10` §5, `11` §13.
- **Criterios:** [ ] firma HMAC con marca de tiempo; [ ] reintentos; [ ] reglas y agrupación; [ ]
  botón de prueba; [ ] sin seguir redirecciones; [ ] aviso para destinos privados.

### SQS-500 Base de la interfaz
- **Referencias:** `09` §1, §5, §6, §7, §8.
- **Criterios:** [ ] fase `ui-consistency` ejecutada y `docs/ui/checklist.md` escrito; [ ] tokens
  de diseño claro y oscuro; [ ] layout, navegación y componentes de `09` §8; [ ] htmx vendorizado
  con SHA-256 registrado; [ ] i18n con prueba de claves; [ ] CSP sin excepciones.

### SQS-501 a SQS-511 Pantallas
- **SQS-501** asistente; **SQS-502** inicio de sesión, TOTP, reautenticación; **SQS-503** panel;
  **SQS-504** lista y nueva biblioteca; **SQS-505** detalle de biblioteca; **SQS-506** respaldos y
  restauración; **SQS-507** tareas con SSE; **SQS-508** eventos y auditoría; **SQS-509** salud;
  **SQS-510** ajustes; **SQS-511** ventana de actualización de Resolve (servicio y pantalla).
- **Criterios comunes:** [ ] cumple `docs/ui/checklist.md` (verificado con
  `ui-consistency:verifying`); [ ] estados vacíos, de error y de carga; [ ] operaciones largas como
  tareas con progreso; [ ] peligrosas con el patrón de `09` §5; [ ] usable a 360 px; [ ] teclado y
  lector de pantalla en flujos principales; [ ] ambos idiomas.

### SQS-520 Pruebas extremo a extremo con navegador (1.1)
- **Criterios:** [ ] `chromedp` cubre asistente, crear biblioteca (con Resolve simulado),
  respaldar, restaurar como nueva, reemplazar y revertir, eliminar y recuperar.

## 9. Versión 0.9 y 1.0

Alcance recortado el 2026-10-19 (decisión de Diego: el plan original estaba sobredimensionado
para un proyecto de una persona). Se queda lo que valida lo que el código ya asume y lo que hace
publicable la versión; lo demás pasa a 1.1 y queda anotado en cada tarea.

### SQS-600 Recuperar instalación existente
- **Referencias:** `12` §9.
- **Criterios:** [ ] desde el asistente con la clave maestra; [ ] desde `_system/` o reconstruyendo
  el catálogo; [ ] "restaurar todas"; [ ] prueba de recuperación completa en CI (AC-11 automatizado
  sin Resolve).

### SQS-601 Kit de recuperación
- **Criterios:** [ ] descarga obligatoria en el asistente; [ ] redescarga desde Ajustes (sin
  reautenticación, ADR 0021); [ ] contenido de `08` §6.

### SQS-602 Paquete de diagnóstico
- **Referencias:** `13` §6.
- **Criterios:** [ ] contenido exacto de la lista; [ ] prueba que busca secretos dentro del zip.

### SQS-603 Batería de fallos y recuperación
- **Referencias:** `16` §4.
- **Criterios (recortados):** [ ] los escenarios que ya corren en CI (`test/scenarios.py`:
  procesos que mueren, PostgreSQL caído, restaurar con sesiones abiertas) pasan en la versión
  candidata; [ ] invariantes comprobados. El trabajo nocturno con el resto de escenarios pasa a
  1.1.

### SQS-604 Revisión de seguridad
- **Criterios (recortados):** [ ] una corrida local de ZAP baseline contra el panel, sin alertas
  altas; [ ] revisión manual contra `08` con ADR 0021 ya aplicado; [ ] hallazgos corregidos o
  registrados con ADR. ZAP nocturno en CI pasa a 1.1.

### SQS-605 Documentación de usuario y runbooks
- **Referencias:** `18` §1.
- **Criterios (recortados):** [ ] el README es el manual de 1.0 y está al día; [ ] `SECURITY.md`,
  `CHANGELOG.md` y `CONTRIBUTING.md` existen; [ ] tres runbooks: restaurar y revertir, recuperar
  una instalación, actualizar sqsolve; [ ] `docs/manual/errores.md` generado a partir de los
  códigos `SQS-`. Los demás manuales y runbooks pasan a 1.1.

### SQS-606 Aceptación 1.0
- **Referencias:** `16` §8.
- **Criterios (recortados):** [ ] AC-01 a AC-11 en una sola sesión con la estación Linux de Diego
  y una macOS con Resolve Studio, con el modo según actividad en lugar de las 72 horas de
  respaldos horarios; [ ] registrados en `docs/aceptacion/1.0.md` con versiones, fechas y
  salidas de `sqsolve doctor`.

### SQS-607 Publicación 1.0
- **Referencias:** `17` §6, §8.
- **Criterios (recortados):** [ ] lista de comprobación de `17` §8 completa; [ ] imagen firmada
  con cosign sin clave (identidad del workflow de GitHub) y verificada con `cosign verify`; [ ]
  SBOM de buildx adjunto; [ ] versión en GitHub con `deploy/compose.yaml` y las notas. Réplica
  del registro, binarios sueltos y verificación por un tercero pasan a 1.1.

## 10. Versión 1.1

| ID | Tarea | Depende de | Criterio principal |
|---|---|---|---|
| SQS-700 | Credenciales por persona | PoC-01 paso 5, SQS-210 | Alta, revocación inmediata, objetos nuevos a nombre del propietario |
| SQS-701 | Reglas de red en interfaz y API | PoC-08, SQS-211 | Una IP fuera de la regla no conecta; se ve la regla en `pg_hba.conf` |
| SQS-702 | Portal de colaborador | SQS-700 | Un colaborador solo ve sus bibliotecas y su contraseña con reautenticación |
| SQS-703 | SMTP y ntfy | SQS-420 | Envío de prueba y por reglas; contraseñas cifradas |
| SQS-704 | Copias externas con restic | SQS-301 | `backup`, `forget --prune`, `check` programados; restauración desde remoto |
| SQS-705 | Hooks en contenedor aparte | SQS-420 | Contrato de `10` §6; sin secretos; bloqueantes `pre` |
| SQS-706 | Migración desde otro servidor | SQS-330 | Lista bibliotecas de un PostgreSQL externo y las trae con verificación L2 |
| SQS-707 | Plantillas locales (E2) | PoC-04 paso 3 | Crear desde plantilla sin pasar por Resolve |
| SQS-708 | Adopción de bases existentes | SQS-200 | `REASSIGN OWNED` auditado y registro de la base |
| SQS-610 | Llaves `.resolvedbkey` | PoC-05, revisión J-02 | Llave aceptada por Resolve en Windows, macOS y Linux; desactivada por defecto |
| SQS-720 | Exportación experimental: modelo y archivos `.drp`/`.drx` | ADR 0019 | 5 de 5 proyectos de `apolo` sin diferencias canónicas contra Resolve; kit documentado (`22` §6) |
| SQS-721 | Exportación experimental: interfaz, API, permisos y auditoría | SQS-720 | Pestaña Exportar; ajuste apagado por defecto; descargas auditadas; códigos `SQS-E` con prueba |
| SQS-722 | Registro de huellas verificadas | SQS-720 | `internal/compat` con la huella de 21.1.1 y procedimiento en `14` §4 |

## 11. Versiones 1.2 y 2.0

| ID | Tarea | Versión |
|---|---|---|
| SQS-800 | Métricas Prometheus | 1.2 |
| SQS-801 | Capacidad de volúmenes de medios | 1.2 |
| SQS-802 | `sqsolve-check` para estaciones (conexión, autenticación, TLS, puerto de colaboración entre dos estaciones) | 1.2 |
| SQS-803 | Mantenimiento programable en la interfaz | 1.2 |
| SQS-900 | PITR con pgBackRest | 2.0 |
| SQS-901 | OIDC | 2.0 |
| SQS-902 | WebAuthn | 2.0 |
| SQS-903 | Asistente de versión mayor de PostgreSQL (ADR 0017) | 2.0 |
| SQS-904 | Modo servidor externo | 2.0 |

## 12. Estado

| Fase | Tareas | Estado |
|---|---|---|
| 0.0 | SQS-020, SQS-001 a SQS-010 | Hechas el 2026-10-19 contra mininas con la API de scripting de Resolve: PoC-06, 08 y 09, y PoC-03 en su mitad "estable con el uso" (`docs/poc/`). Pendientes con clics en Resolve: PoC-01, 04 y la mitad de 03 (actualizar). Recorte: 05, 07, 10 y la matriz completa de 02 a 1.1 |
| 0.1 | SQS-040, 041, 100 a 117, 030, 031 | Implementado (2026-10-18) |
| 0.2 | SQS-150 a 156 | Implementado (2026-10-18) |
| 0.3 | SQS-200 a 240 | Implementado (2026-10-18) según la hipótesis del plan; sin validar con Resolve (PoC-01 a 05). E0 queda experimental |
| 0.4 y 0.5 | SQS-300 a 340 | Implementado (2026-10-18); la restauración vista desde Resolve sin validar (PoC-07, 08) |
| 0.6 | SQS-400 a 420, 500 a 520 | API, notificaciones e interfaz implementadas (2026-10-18). SQS-400, 410 y 520 pasan a 1.1 (recorte del 2026-10-19); hay una prueba de extremo a extremo por HTTP y pruebas de plantilla |
| 0.9 y 1.0 | SQS-600 a 607 | SQS-600, 601 y 602 implementadas (2026-10-18); SQS-603 cumple el criterio recortado con `test/scenarios.py`; SQS-604 hecha el 2026-10-19 (`docs/08` §10); SQS-605 hecha salvo `docs/aceptacion`; SQS-607 lista en CI (firma y release), pendiente de la primera etiqueta `v1.0.0`; SQS-606 pendiente: la sesión de aceptación con dos estaciones |
| 1.1 (parcial) | SQS-720 a 722 | Implementado (2026-10-19), ADR 0019 Aceptado como experimental en 1.0; validado contra Resolve 21.1.1 |
| Identidad | ADR 0020 | Implementada (2026-10-19): marca en `docs/identidad`, panel con el imagotipo e íconos, README |
| Seguridad proporcional | ADR 0021 | Implementada (2026-10-19): sin reautenticación, contraseñas de 8, sesiones largas, bloqueo con `admin unlock` |
| Respaldo según actividad | ADR 0022 | Implementado (2026-10-19): modo `activity`, compuerta en el programador, marca por respaldo, línea de protección |
| Exportar desde respaldo | docs/22 §7 | Implementado (2026-10-19): tarea `backup.export`, base temporal, página y API, auditoría |

Los criterios `[ ]` de cada tarea no se marcaron uno por uno en esta pasada; la tabla resume.
