# 02. Pruebas de concepto

Las PoC convierten los puntos INVESTIGAR de `01-investigacion.md` en hechos antes de escribir el
código que depende de ellos. Cada PoC produce un informe en `docs/poc/PoC-NN-<nombre>.md` con
fecha, versiones exactas, pasos, resultados y la decisión tomada.

## Reglas para todas las PoC

- Se ejecutan con software obtenido legalmente por Diego (Resolve gratuito y Studio) en sus
  estaciones. Resolve no corre en CI ni en lizeth (sin GPU).
- Solo se observa el comportamiento de Resolve desde fuera: registros de PostgreSQL, catálogo del
  sistema, tráfico de red y archivos que el propio usuario genera. Sin descompilar ni modificar
  binarios.
- Los registros con sentencias SQL de Resolve se guardan fuera del repo (`/mnt/...` de Diego) y
  no se publican. En el informe solo van conclusiones (por ejemplo, "ejecuta `CREATE DATABASE`
  conectado a la base `postgres`"), no el esquema.
- Entorno base: el `compose.yaml` de laboratorio en `deploy/lab/` (SQS-020), con PostgreSQL 18 y
  `log_statement = 'all'`, `log_connections = on`, `log_disconnections = on`.

## Resumen y bloqueos

| PoC | Tema | Prioridad | Bloquea |
|---|---|---|---|
| PoC-01 | Ciclo de vida de una biblioteca y privilegios mínimos | Crítica | SQS-200 a SQS-230 (bibliotecas) |
| PoC-02 | Matriz de versiones Resolve × PostgreSQL | Crítica | ADR 0006, `14-compatibilidad.md` |
| PoC-03 | Huella de esquema y detección de actualizaciones | Alta | SQS-240 |
| PoC-04 | Formato de respaldo y restauración cruzada | Crítica | SQS-300 a SQS-340 (respaldos) |
| PoC-05 | Llaves `.resolvedbkey` | Media | SQS-610 (módulo experimental) |
| PoC-06 | TLS y SCRAM desde Resolve | Alta | Valores por defecto de `pg_hba.conf` |
| PoC-07 | Colaboración y tráfico de red | Alta | Documentación para editores |
| PoC-08 | Red: IP de origen en Docker, puerto, VPN | Alta | Reglas de red por biblioteca, guía de acceso remoto |
| PoC-09 | Estados restringidos y desconexión forzada | Crítica | Restauración por intercambio, modo mantenimiento |
| PoC-10 | Bloqueos y duración de `pg_dump` con uso real | Media | Recomendaciones de programación |

Orden sugerido: PoC-01, PoC-02 y PoC-06 en una misma sesión; luego PoC-04 y PoC-09; después el
resto.

## PoC-01. Ciclo de vida y privilegios mínimos

**Pregunta.** ¿Qué hace Resolve al crear y conectar una biblioteca, y con qué privilegios mínimos
funciona?

**Procedimiento.**

1. Crear un rol `lab_a` con `LOGIN NOSUPERUSER NOCREATEROLE CREATEDB` y contraseña SCRAM.
   En Resolve 21.1: "Create" de una biblioteca `lab_a`. Registrar a qué base se conecta primero
   (`log_connections`), qué sentencias de nivel de base ejecuta (`CREATE DATABASE` con qué
   opciones de plantilla, codificación y colación) y si crea extensiones o funciones.
2. Repetir con un rol `NOCREATEDB` y una base vacía `lab_b` creada antes con dueño `lab_b`:
   probar "Create" y "Connect". Anotar mensajes de error literales.
3. Base `lab_c` cuyo dueño es `sqsolve_admin`, con el esquema `public` propiedad de `lab_c` y
   `GRANT CONNECT, TEMP ON DATABASE lab_c TO lab_c`. Probar "Create" y "Connect" con `lab_c`.
4. Con una biblioteca ya inicializada, quitar `CREATEDB` al rol y verificar que crear, abrir,
   guardar, borrar proyectos e importar medios sigue funcionando.
5. Rol miembro: `CREATE ROLE lab_a_ana LOGIN IN ROLE lab_a`, `ALTER ROLE lab_a_ana SET role =
   'lab_a'`. Conectar Resolve como `lab_a_ana`, crear un proyecto, y comprobar en el catálogo que
   los objetos nuevos quedan a nombre de `lab_a`.
6. Contar conexiones simultáneas por estación (`pg_stat_activity`) en reposo, con un proyecto
   abierto y durante un render. Anotar `application_name` y `client_addr`.
7. Con la base creada sobre un clúster con `--locale-provider=builtin --builtin-locale=C.UTF-8`,
   comprobar que el orden de proyectos y bins en Resolve es aceptable con nombres acentuados.
8. Repetir 1 y 4 con Resolve 20.3.2.

**Éxito.** Se conoce el conjunto mínimo de privilegios para cada fase (inicialización y uso
diario) y la estrategia de creación aplicable (E0, E1 o E2 de
`07-bibliotecas-y-postgresql.md`).

**Decisión que habilita.** Estrategia de creación, modelo de propiedad (dueño de la base:
`sqsolve_admin` o el rol de la biblioteca), credenciales por persona y `max_connections`.

**Plan B.** Si Resolve exige ser dueño de la base, el dueño será el rol de la biblioteca y los
renombres del intercambio se harán con el superusuario por socket local, con auditoría.

## PoC-02. Matriz de versiones

**Pregunta.** ¿Qué versiones de Resolve funcionan con qué versiones de PostgreSQL?

**Procedimiento.** Para cada combinación, crear biblioteca, crear y abrir proyecto, importar
medios, editar una timeline, cerrar y reabrir, respaldar desde Resolve:

| Resolve \ PostgreSQL | 16 | 17 | 18 |
|---|---|---|---|
| 21.1.x | prueba | prueba | prueba |
| 20.3.2 | prueba | prueba | prueba |
| 19.x (opcional) | | | prueba |

**Éxito.** Tabla con "funciona", "funciona con avisos" o "falla" y el mensaje literal.

**Decisión que habilita.** Se confirma PostgreSQL 18 como única versión por defecto, o se
agrega un perfil alternativo (17) solo para equipos atados a una versión de Resolve.

## PoC-03. Huella de esquema

**Pregunta.** ¿La huella definida en `07-bibliotecas-y-postgresql.md` §9 cambia cuando Resolve
actualiza una biblioteca y se mantiene estable con el uso normal?

**Procedimiento.** Crear una biblioteca con 20.3.2, calcular la huella, usarla una hora
(proyectos, bins, timelines), recalcular; abrirla con 21.1 y aceptar la actualización si
Resolve la ofrece; recalcular; abrir un proyecto en 21.1; recalcular.

**Éxito.** La huella no cambia con el uso normal y cambia cuando cambia el esquema. Si
actualizar no cambia el esquema (solo datos de proyecto), se documenta que la huella no detecta
ese caso y la ventana de actualización se vuelve obligatoria en la guía.

## PoC-04. Formato de respaldo y restauración cruzada

**Preguntas.** ¿El archivo de respaldo de Resolve es un archivo de `pg_dump` en formato custom?
¿Resolve restaura un respaldo hecho por sqsolve? ¿Dos bibliotecas restauradas del mismo
respaldo pueden convivir?

**Procedimiento.**

1. Respaldar desde el Project Manager de Resolve 21.1 y 20.3.2. Inspeccionar con `file` y
   `pg_restore --list`. Anotar la versión del formato de archivo.
2. Respaldar con `pg_dump --format=custom --quote-all-identifiers` de PostgreSQL 18. Restaurar
   desde la interfaz de Resolve 21.1 y 20.3.2.
3. Restaurar con `pg_restore --no-owner --no-privileges --role=<rol>` en una base nueva con otro
   nombre y otro rol. Conectar ambas bibliotecas (original y copia) a la misma estación; abrir,
   modificar y cerrar proyectos en las dos; comprobar que no se mezclan.
4. Probar compresión por defecto (gzip) y `--compress=zstd`. Anotar cuál acepta Resolve.

**Éxito.** Se sabe si los respaldos de sqsolve son restaurables desde Resolve, con qué opciones,
y si la restauración como copia es segura.

**Plan B.** Si Resolve no acepta los archivos de sqsolve, la interfaz lo dice claramente y la
restauración se hace solo desde sqsolve. Nada del diseño de respaldos depende de esa
compatibilidad.

## PoC-05. Llaves `.resolvedbkey`

**Pregunta.** ¿Puede sqsolve generar llaves que Resolve acepte?

**Procedimiento.**

1. En una estación con el Project Server oficial (Windows o macOS), agregar una biblioteca
   alojada en sqsolve, habilitar el uso compartido si lo exige y exportar una llave.
2. Documentar la estructura del XML (elementos, atributos, codificación, si la contraseña va en
   claro u ofuscada). No copiar el archivo al repo: solo la descripción de campos.
3. Generar una llave equivalente a mano con otros valores. Importarla en Resolve 21.1 y 20.3.2 en
   Windows, macOS y Linux. Probar nombre de host en vez de IP, contraseñas con símbolos y un
   usuario distinto de `postgres`.

**Éxito.** Resolve importa la llave generada en las tres plataformas.

**Plan B.** Si el formato está cifrado, firmado o es inestable entre versiones, el módulo queda
DESCARTADO y la interfaz ofrece solo instrucciones de conexión manual.

## PoC-06. TLS y SCRAM

**Pregunta.** ¿Resolve se conecta con `scram-sha-256` y por TLS?

**Procedimiento.** `password_encryption = scram-sha-256` y `pg_hba` solo con `scram-sha-256`.
Con `ssl = on`, revisar `pg_stat_ssl` para cada conexión de Resolve. Luego cambiar las líneas a
`hostssl` y probar que Resolve sigue conectando. Probar `ssl_min_protocol_version = TLSv1.3`.

**Éxito.** Se conoce si TLS puede ser obligatorio por defecto. Si Resolve usa TLS en modo
"prefer" sin verificar certificado, se documenta que el cifrado protege contra escucha pasiva
pero no contra suplantación, y la recomendación de VPN para accesos fuera de la LAN se mantiene.

**Resultado parcial (2026-10-19).** Resolve Studio 21.1.1 en Linux contra sqsolve 0.1.4
(PostgreSQL 18.6, certificado autofirmado de `sqsolve-init`, `ssl_min_protocol_version =
TLSv1.2`, `pg_hba` con líneas `host` y `scram-sha-256`): la sesión de Resolve sale en
`pg_stat_ssl` con `ssl = t`, TLSv1.3 y `TLS_AES_256_GCM_SHA384`, sin configurar nada en la
estación. Confirma el modo "prefer" sin verificación de certificado. Pendiente: `hostssl`
obligatorio (ajuste `RequireTLS`) y `TLSv1.3` mínimo, que exigen una conexión nueva desde
Resolve.

## PoC-07. Colaboración y tráfico

**Preguntas.** ¿Qué tráfico genera la colaboración? ¿Funciona con la versión gratuita de 21?

**Procedimiento.** Dos estaciones con Studio en la misma biblioteca, colaboración activada en el
proyecto. Captura con `tcpdump` o Wireshark en ambas estaciones y en el servidor. Probar chat,
bloqueo de bins y timelines, actualización de cambios de colaboradores. Bloquear 50059-50314 en
el firewall de una estación y observar el síntoma. Repetir con una estación con Resolve gratuito.

**Éxito.** Lista de puertos y sentidos del tráfico; síntomas visibles de cada fallo para la guía
de diagnóstico; respuesta sobre la versión gratuita.

## PoC-08. Red

**Preguntas.** ¿PostgreSQL ve la IP real del cliente con el puerto publicado por Docker? ¿Resolve
acepta otro puerto? ¿Funciona la biblioteca y la colaboración por VPN?

**Procedimiento.**

1. En el NAS de Diego, con el Compose de referencia y red bridge, conectar desde una estación y
   leer `client_addr` en `pg_stat_activity`. Repetir con `network_mode: host` como comparación.
2. Probar en el campo de ubicación de Resolve `IP:puerto` y nombre de host.
3. Con WireGuard (o Headscale), conectar una estación remota a la biblioteca y medir la
   experiencia al abrir y guardar proyectos. Probar colaboración entre una estación local y
   una remota.

**Éxito.** Se decide si las reglas de red por biblioteca son confiables con bridge o si se
documenta `host` como requisito para usarlas; se documenta el acceso remoto probado.

## PoC-09. Estados restringidos y desconexión forzada

**Preguntas.** ¿Cómo reacciona Resolve cuando sqsolve corta sus conexiones, bloquea la base o la
reemplaza por una restaurada con el mismo nombre?

**Procedimiento.** Con un proyecto abierto en una estación:

1. `ALTER DATABASE x ALLOW_CONNECTIONS false` y `pg_terminate_backend` de sus sesiones. Anotar
   el mensaje de Resolve y si reintenta.
2. Intercambio: restaurar una copia en `x__restore_1`, renombrar `x` a `x__prev_1` y
   `x__restore_1` a `x`, reabrir conexiones. ¿Resolve se reconecta y ve el estado restaurado?
   ¿Hay cachés locales que muestren datos viejos?
3. `ALTER DATABASE x SET default_transaction_read_only = on`. ¿Resolve abre proyectos en modo
   lectura o falla?
4. `ALTER ROLE x NOLOGIN` con sesiones abiertas.

**Éxito.** Se conoce el comportamiento visible para escribir los avisos de la interfaz y la guía
"antes de restaurar"; se decide si el estado "archivada (solo lectura)" es viable.

## PoC-10. Bloqueos y duración de `pg_dump`

**Pregunta.** ¿El respaldo en caliente afecta a los editores?

**Procedimiento.** Con una biblioteca de prueba grande (varios proyectos con timelines largas;
tamaño anotado con `pg_database_size`), ejecutar `pg_dump` mientras una estación edita y otra
guarda con frecuencia. Medir duración, esperas de bloqueo (`pg_locks`, `log_lock_waits = on`) y
la experiencia en Resolve. Repetir la restauración de prueba en una base auxiliar.

**Éxito.** Datos de duración por tamaño para la guía; confirmación de que el respaldo en caliente
no bloquea el trabajo normal o, si lo hace, en qué operaciones.
