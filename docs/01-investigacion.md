# 01. Investigación

Fecha de corte: 2026-10-18. Las referencias numeradas [n] están al final.

## 1. Método

- Fuentes primarias: notas de versión oficiales de DaVinci Resolve 21.x en el sitio de soporte
  de Blackmagic Design [1][2][3][4], el manual de referencia de Resolve (capítulos de bases de
  datos y colaboración) [5], la documentación de PostgreSQL [12][13][14][15][16] y la de la
  imagen oficial de PostgreSQL en Docker Hub [17].
- Fuentes secundarias: respuestas del soporte de Blackmagic en su foro [6][7][8], proyectos
  libres que ya empaquetan PostgreSQL para Resolve [9][10][11] y la documentación pública de
  Studio Server [18][19][20][21].
- El foro de Blackmagic bloquea la descarga automática; sus citas provienen de los fragmentos
  indexados y se marcan como REPORTADO hasta reproducirlas.
- No se usó ni se usará código, binarios ni esquemas de Blackmagic Design o de WireBear.

## 2. Qué es una biblioteca de proyectos

Resolve guarda proyectos en bibliotecas. Hay tres tipos:

| Tipo | Dónde vive | Relevancia para sqsolve |
|---|---|---|
| Local (antes "Disk database") | Carpeta en la estación de trabajo | Ninguna: no se comparte en red [5] |
| En red | Una base de datos de PostgreSQL en un servidor | Es el objeto que sqsolve administra |
| Blackmagic Cloud | Servicio hospedado por Blackmagic | Fuera de alcance: servicio propietario [22] |

Desde Resolve 18 la interfaz y el Project Server llaman "project libraries" a lo que antes eran
"databases"; cada biblioteca en red es una base de datos de PostgreSQL independiente [11].

## 3. El Project Server oficial de Blackmagic

Según el manual [5], "DaVinci Resolve Project Server" es una aplicación independiente que se
instala junto con Resolve en macOS y Windows y permite crear, administrar, respaldar,
restaurar y compartir bibliotecas sin abrir Resolve. Sus funciones documentadas:

- **Enable Sharing / Disable Sharing**: configura el PostgreSQL local para aceptar conexiones de
  la red y "autoriza" esa configuración.
- **New Database**: crea bibliotecas sin abrir Resolve.
- **Optimize**: solo para PostgreSQL; hace "vacuum" y reindexa.
- **Backup / Restore**: exporta e importa archivos `.resolve.backup`.
- **Create Access Key**: exporta un archivo `.resolvedbkey` para conectar otra estación.

Requisitos del servidor según el mismo manual: red local, conexiones rápidas, el servidor no
necesita GPU rápida y "no necesita llave ni número de serie, ni ser la versión Studio" [5].

Para Linux, el manual describe una configuración manual de `pg_hba.conf` (para los sistemas
CentOS de Blackmagic) con una línea `host all all <subred> md5` [5]. WireBear afirma que
Blackmagic no ofrece una versión de Project Server para Linux, y por eso existe Studio Server
[18] (REPORTADO).

## 4. Versiones de PostgreSQL por versión de Resolve

| Resolve | PostgreSQL | Estado | Fuente |
|---|---|---|---|
| hasta 12.5.3 (macOS) | 8.4 | VERIFICADO | Manual [5] |
| 12.5.4 a 17 | 9.5 | VERIFICADO | Manual [5]; comunidad [11] |
| 17 y anteriores con PostgreSQL 12 o superior | No funcionan | REPORTADO | [9] dice que usan una función eliminada en PostgreSQL 12; la eliminación de `WITH OIDS` en 12 es la explicación probable [16] |
| 18, 19, 20 | 13 (el instalador de Project Server 20 instala 13.4) | REPORTADO | Soporte de Blackmagic en el foro [6] |
| 21.1 | Project Server incluye PostgreSQL 18 | VERIFICADO | Notas de 21.1 [1] |
| 21.1 | "Resolve y Project Server pueden seguir trabajando con bibliotecas en red basadas en PostgreSQL 13 y superior" | VERIFICADO | Notas de 21.1 [1] |

Otros hechos relevantes:

- Los instaladores de Project Server no actualizan ni modifican un PostgreSQL ya instalado;
  solo actualizan la aplicación (REPORTADO, soporte de Blackmagic [6]). Es decir, hay
  servidores en producción con PostgreSQL 13 que nunca se actualizaron.
- PostgreSQL 13 dejó de tener soporte el 2025-11-13 y PostgreSQL 14 lo pierde el 2026-11-12.
  PostgreSQL 18 tiene soporte hasta el 2030-11-14 [12] (VERIFICADO).

## 5. Creación y conexión de bibliotecas desde Resolve

- El Project Manager de Resolve ofrece "Create" (crea una biblioteca nueva en el servidor) y
  "Connect" (se conecta a una existente). Proyectos de la comunidad indican que la primera
  estación usa "Create" para que Resolve cree la estructura, y las demás usan "Connect" [10]
  (REPORTADO).
- Credenciales por defecto de Blackmagic: usuario `postgres`, contraseña `DaVinci` [9][10][11]
  (REPORTADO, convención muy difundida). Es decir, el modelo oficial usa el superusuario.
- Resolve crea la base de datos y su esquema. El esquema no está documentado públicamente.
- Si se intenta "Create" sobre un nombre que ya existe, Resolve responde que la base ya existe
  [23] (REPORTADO). No hay evidencia pública de si "Connect" sobre una base vacía inicializa el
  esquema: **INVESTIGAR (PoC-01)**.
- En el campo de ubicación se escribe solo la IP del servidor, sin puerto [10] (REPORTADO).
  WireBear indica que el puerto 5432 no puede cambiarse sin que los clientes fallen [18]
  (REPORTADO). **INVESTIGAR (PoC-08)** si Resolve acepta otro puerto.
- No se encontró documentación sobre los privilegios mínimos que Resolve necesita (superusuario,
  `CREATEDB`, propiedad de la base). **INVESTIGAR (PoC-01)**.

## 6. Actualización de bibliotecas entre versiones de Resolve

- Las notas de Resolve 21, 21.0.3, 21.1 y 21.1.1 dicen, con la misma redacción, que se
  mantuvo la compatibilidad de las bibliotecas con Resolve 20.3.2, pero que "los proyectos
  individuales creados o abiertos" en 21.x "ya no serán accesibles" en 20.3.2, y recomiendan
  "un respaldo completo de la biblioteca y respaldos de proyectos individuales" antes de abrir
  proyectos en 21 [1][2][3][4] (VERIFICADO).
- Consecuencia: una sola estación con una versión nueva puede dejar proyectos inaccesibles para
  las demás. El cambio puede ser a nivel de proyecto (datos) y no necesariamente del esquema.
- La comunidad documenta que, al restaurar un respaldo de una versión anterior, se conecta la
  biblioteca y Resolve ofrece actualizarla [11] (REPORTADO).

## 7. Respaldos nativos

- El manual describe archivos `.resolve.backup` creados por Project Server y por el Project
  Manager [5] (VERIFICADO).
- Herramientas libres generan respaldos con `pg_dump --format=custom --blobs` y extensión
  `.backup`, y afirman que se restauran desde la interfaz de Resolve [9][11] (REPORTADO). El
  formato interno exacto del archivo oficial no está documentado: **INVESTIGAR (PoC-04)**.
- El procedimiento de la comunidad para restaurar en un servidor nuevo usa `pg_restore
  --single-transaction --clean --if-exists` sobre una base en blanco [11] (REPORTADO).
- "Optimize" equivale a `vacuumdb` y `reindexdb` [5][11].

## 8. Llaves de acceso `.resolvedbkey`

- VERIFICADO [5]: Project Server exporta un archivo `.resolvedbkey`; al arrastrarlo al Project
  Manager, la biblioteca compartida aparece en la barra lateral. Si el servidor cambia de red, hay
  que desactivar y reactivar el uso compartido antes de generar llaves nuevas.
- REPORTADO: el manual de Resolve 18 (copia en línea, hoy caída) describe la llave como "un
  archivo .xml con la extensión .resolvedbkey". Un usuario del foro afirma que guarda la
  contraseña en texto plano.
- INVESTIGAR (PoC-05): los nombres de los elementos XML, si incluye usuario y contraseña, si
  admite puerto o nombre de host, y si Resolve en Windows, macOS y Linux acepta una llave
  generada por otro programa. No existe documentación pública ni implementaciones libres
  (búsqueda en GitHub sin resultados el 2026-10-18).

## 9. Colaboración

| Afirmación | Estado | Fuente |
|---|---|---|
| Todos los participantes de la colaboración deben usar DaVinci Resolve Studio | VERIFICADO para Resolve 15 [5]; por confirmar para 21 (PoC-07) | Manual [5] |
| La colaboración usa bloqueo de bins, de timelines y de clips, y un chat integrado ("Collaboration Chat") | VERIFICADO [5] | Manual |
| El nombre de cada colaborador lo define cada estación ("Customizing Your Collaborator Identification"); no es una identidad autenticada | VERIFICADO [5] | Manual |
| Desde Resolve 15.2 el enlace entre clientes usa un puerto entre 50059 y 50314 (incrementa si 50059 está ocupado); antes de 15.2 era 56789 | REPORTADO | Soporte de Blackmagic en el foro [7] |
| La conexión entre colaboradores es de cliente a cliente; un login correcto a la biblioteca no prueba que el enlace directo funcione | REPORTADO | [7][8] y una guía de terceros para 21.1 [24] |
| Los clientes deben estar en la misma subred | REPORTADO por soporte en el foro [7]; el manual de 15 dice que también pueden estar en subredes distintas [5] | Contradicción: PoC-08 |
| Resolve 21.1.1 "corrigió problemas de conectividad de la colaboración multiusuario" | VERIFICADO | Notas [2] |
| La biblioteca debe estar en un servidor que nunca se apague ni se suspenda | VERIFICADO [5] | Manual |

Conclusión de arquitectura: el servidor solo necesita ofrecer PostgreSQL en 5432. El tráfico de
colaboración en 50059-50314 viaja entre estaciones. Studio Server publica 50059 en su contenedor
y lo describe como "chat de colaboración" [18]; con la evidencia actual eso no tiene efecto, porque
el chat no pasa por el servidor. Se confirma con captura de tráfico en PoC-07.

## 10. Studio Server de WireBear

### 10.1 Versión 1 (documentada)

Fuentes: página del producto [18], página del cliente CLI [19], Docker Hub [20].

- Un solo contenedor Docker (`allebb/studio-server`) que incluye PostgreSQL, un panel web,
  tareas cron y hooks; usa por dentro su herramienta CLI "Studio Server Client".
- Puertos: 5432 (PostgreSQL), 50059 ("chat de colaboración"), 8543 (panel). Solo el 8543 puede
  remapearse.
- Volúmenes: `database`, `backups`, `jobs` (montado en `/etc/cron.d/`) y `hooks` (scripts BASH
  antes y después del respaldo).
- Funciones: panel web; crear una o varias bibliotecas; crea usuarios y roles de base de datos
  (el usuario se llama igual que la base); respaldos programados por base (por defecto a las
  00:00) con expresiones cron; purga de respaldos viejos (30 días por defecto); respaldo y
  restauración manuales; restablecer contraseñas; hooks para subir respaldos a la nube.
- Advertencia propia: antes de restaurar, todos los usuarios deben salir de Resolve o cambiar
  de biblioteca.
- La etiqueta `latest` en Docker Hub es del 2023-07-24 y solo para amd64 [20].

### 10.2 Versión 3 (anunciada, sin publicar al 2026-10-18)

Anuncio del 2026-09-06 [21], que hablaba de septiembre; en el foro, un mensaje del 2026-10-08
agradece al autor una actualización y dice esperar la publicación a finales de octubre de 2026
(fecha sin confirmar por WireBear en una fuente pública):

- Reescritura completa con menos consumo de recursos.
- Tres modos de instalación: sin contenedores, Docker Compose con base de datos separada y
  Compose con servidor de base de datos externo.
- Réplica opcional de respaldos a S3, webhooks, imágenes ARM64, vista de usuarios y equipos
  conectados por biblioteca.
- Edición "Professional" de pago (licencia vitalicia) con auditoría de usuarios y bibliotecas,
  control de acceso por usuario y biblioteca, revocación centralizada y un portal restringido
  donde cada usuario descarga los archivos de conexión de sus bibliotecas.

### 10.3 Licencia

No encontramos repositorio público ni licencia para Studio Server ni para su CLI. La página
indica "Copyright © 2023 WireBear - All rights reserved" [18][19]. Una imagen pública en Docker
Hub no equivale a código abierto. sqsolve se diseña solo a partir de estas descripciones
funcionales públicas.

### 10.4 Lo que sqsolve toma como referencia y lo que mejora

| Necesidad | Studio Server | sqsolve |
|---|---|---|
| Separación de componentes | v1 todo en un contenedor; v3 anuncia Compose | Dos contenedores desde el inicio: PostgreSQL oficial y sqsolve |
| Privilegios en PostgreSQL | No documentado | Rol de control sin superusuario; rol propio por biblioteca; reglas de red por biblioteca |
| Restauración | Sobre la base activa, con aviso de salir de Resolve | Siempre en base nueva, verificación y luego intercambio; el estado previo se conserva para revertir |
| Verificación de respaldos | No documentada | Suma SHA-256, lectura del índice y restauración de prueba periódica con conteos |
| Hooks | Scripts BASH dentro del contenedor del servidor | Webhooks en 1.0; hooks en un contenedor aparte sin secretos en 1.1 |
| Auditoría y acceso por persona | Edición de pago (v3) | Incluidos y libres |
| Puerto 50059 | Publicado | No se publica (la colaboración va entre estaciones) |
| Licencia | Cerrada | AGPL-3.0-or-later |

## 11. Otros proyectos libres

| Proyecto | Licencia | Qué hace | Qué aprendemos |
|---|---|---|---|
| elliotmatson/Docker-Davinci-Resolve-Project-Server [9] | MIT | Compose con `postgres:13`, respaldos con `prodrigestivill/postgres-backup-local` (`--blobs --format=custom --quote-all-identifiers`, sufijo `.backup`), pgAdmin | La compatibilidad de formato con la restauración de Resolve es una función valorada; Resolve es poco tolerante a versiones distintas de PostgreSQL |
| Luprintech/davinci-resolve-server [10] | MIT | Compose con PostgreSQL 13 y respaldos; recomienda VPN y nunca exponer 5432 | Flujo "Create la primera vez, Connect las demás"; en Synology el 5432 choca con el PostgreSQL del sistema |
| sethgoldin/davinci-resolve-postgresql-workflow-tools [11] | Sin licencia declarada según la API de GitHub | Scripts de respaldo y "optimize" con launchd y systemd | Procedimiento de restauración en servidor nuevo; equivalencia de "Optimize" |

Ninguno ofrece roles por biblioteca, restauración segura, verificación de respaldos ni
administración web completa.

## 12. Hechos de PostgreSQL que condicionan el diseño

| Hecho | Estado | Fuente |
|---|---|---|
| Fin de soporte: 13 el 2025-11-13, 14 el 2026-11-12, 15 el 2027-11-11, 16 el 2028-11-09, 17 el 2029-11-08, 18 el 2030-11-14 | VERIFICADO | [12] |
| Desde PostgreSQL 16, un rol con `CREATEROLE` solo modifica roles sobre los que tiene `ADMIN OPTION`, y solo concede `CREATEDB` si lo tiene | VERIFICADO | [13] |
| PostgreSQL 18 activa las sumas de verificación de páginas (data checksums) por defecto en `initdb` y declara obsoletas las contraseñas MD5 | VERIFICADO | [14] |
| El proveedor de colación `builtin` (PostgreSQL 17+) admite `C`, `C.UTF-8` y `PG_UNICODE_FAST` y no depende de la glibc | VERIFICADO | [15] |
| `WITH OIDS` se eliminó en PostgreSQL 12 | VERIFICADO | [16] |
| Rol predefinido `pg_read_all_data` (14+) para leer todas las tablas | VERIFICADO | [25] |
| En la imagen oficial `postgres:18`, `PGDATA` es `/var/lib/postgresql/18/docker` y el volumen se monta en `/var/lib/postgresql` | VERIFICADO | [17] |

## 13. Docker y NAS

- Cualquier plataforma que acepte un archivo de Docker Compose puede instalar sqsolve. TrueNAS
  ejecuta apps con Docker desde 24.10 y permite pegar un Compose con "Install via YAML" [26];
  Portainer y Unraid aceptan Compose de forma similar. No se diseña nada específico para un NAS.
- El catálogo de apps de TrueNAS usa la imagen `postgres` oficial (18.6 al 2026-10-18) [27],
  lo que confirma que la imagen oficial es la opción estándar.
- En Synology, el PostgreSQL del sistema ocupa 5432 en localhost; la solución de la comunidad es
  publicar el puerto del contenedor solo en la IP de la LAN [10].

## 14. Matriz de hallazgos

| ID | Hallazgo | Estado | Afecta |
|---|---|---|---|
| H-01 | Una biblioteca en red es una base de PostgreSQL independiente | VERIFICADO | Modelo de datos |
| H-02 | Resolve 21.1 trabaja con bibliotecas en PostgreSQL 13 o superior y Project Server trae 18 | VERIFICADO | ADR 0006 |
| H-03 | Resolve 18 a 20 se usaron con PostgreSQL 13; su compatibilidad con 18 no está documentada | INVESTIGAR (PoC-02) | Matriz de compatibilidad |
| H-04 | Resolve 17 y anteriores no funcionan con PostgreSQL 12+ | REPORTADO | Fuera de soporte |
| H-05 | Resolve crea el esquema con "Create"; privilegios mínimos desconocidos | INVESTIGAR (PoC-01) | Creación de bibliotecas, roles |
| H-06 | Formato `.resolve.backup` compatible con `pg_dump -Fc` | REPORTADO, INVESTIGAR (PoC-04) | Formato de respaldo |
| H-07 | `.resolvedbkey` es XML con datos de conexión | REPORTADO, INVESTIGAR (PoC-05) | Módulo de llaves |
| H-08 | Colaboración entre clientes en 50059-50314 | REPORTADO, INVESTIGAR (PoC-07) | Puertos, documentación |
| H-09 | Colaboración requiere Studio | VERIFICADO en 15, por confirmar en 21 | Documentación |
| H-10 | Proyectos abiertos en 21.x no abren en 20.3.2 | VERIFICADO | Ventana de actualización |
| H-11 | La identidad del colaborador en Resolve no está autenticada | VERIFICADO | Autorización: solo por conexión |
| H-12 | Resolve no permite elegir el puerto | REPORTADO, INVESTIGAR (PoC-08) | Despliegue |
| H-13 | Studio Server no es software libre | VERIFICADO (sin fuente ni licencia pública) | Implementación independiente |

## 15. Implicaciones para la arquitectura

1. El motor por defecto es PostgreSQL 18 (soporte hasta 2030 y versión que Blackmagic ya
   incluye). La compatibilidad de Resolve 20.3.x con 18 se demuestra en PoC-02 antes de
   declararla.
2. sqsolve trata las bibliotecas como cajas negras: no depende del esquema de Resolve, que no
   está documentado y cambia entre versiones.
3. La creación de bibliotecas debe funcionar aunque Resolve exija crear la base él mismo
   (estrategia de "ventana de inicialización", `07-bibliotecas-y-postgresql.md`).
4. La autorización real solo existe a nivel de conexión a PostgreSQL (qué credencial entra a
   qué biblioteca y desde qué red). No se ofrecen permisos dentro de un proyecto porque Resolve
   no los aplica.
5. Las llaves `.resolvedbkey` son un módulo experimental que depende de PoC-05.
6. El servidor no publica 50059; la documentación para editores explica los puertos de las
   estaciones.
7. Antes de cualquier actualización de Resolve, sqsolve ofrece respaldos verificados de todas
   las bibliotecas, porque el cambio puede ser irreversible para versiones anteriores.

## Referencias

1. Blackmagic Design, "About DaVinci Resolve 21" (21.1), notas de versión.
   https://www.blackmagicdesign.com/support/content/readme/59dd4eef1f4941c29fb8dc48b33f5c87
2. Blackmagic Design, notas de versión 21.1.1.
   https://www.blackmagicdesign.com/support/content/readme/62817b75bdf744718e93909ca6d58f0d
3. Blackmagic Design, notas de versión 21.
   https://www.blackmagicdesign.com/support/content/readme/6aefbffe870f4ba686e61ecb83b19ec0
4. Blackmagic Design, notas de versión 21.0.3.
   https://www.blackmagicdesign.com/support/content/readme/f549d6da20df4cc98f57d4777d8c8a03
5. Blackmagic Design, DaVinci Resolve 15 Reference Manual, capítulos 147 "Managing Databases and
   Project Servers" y 148 "Collaborative Workflow" (copia pública).
   https://ltbits.github.io/davinci-resolve-manuals/DR15/DR15-RM-13-ProjectDatabasesCollaborativeAndRemoteWorkflows.pdf
6. Foro de Blackmagic, "Resolve Project Server v20 is out".
   https://forum.blackmagicdesign.com/viewtopic.php?f=21&t=222297
7. Foro de Blackmagic, respuestas de soporte sobre el puerto de colaboración.
   https://forum.blackmagicdesign.com/viewtopic.php?p=547147 y
   https://forum.blackmagicdesign.com/viewtopic.php?p=747507
8. Foro de Blackmagic, "Trying to setup collaboration and failing".
   https://forum.blackmagicdesign.com/viewtopic.php?p=747246
9. elliotmatson/Docker-Davinci-Resolve-Project-Server.
   https://github.com/elliotmatson/Docker-Davinci-Resolve-Project-Server
10. Luprintech/davinci-resolve-server. https://github.com/Luprintech/davinci-resolve-server
11. sethgoldin/davinci-resolve-postgresql-workflow-tools.
    https://github.com/sethgoldin/davinci-resolve-postgresql-workflow-tools
12. PostgreSQL, Versioning Policy. https://www.postgresql.org/support/versioning/
13. PostgreSQL 16.0 Release Notes. https://www.postgresql.org/docs/release/16.0/
14. PostgreSQL 18.0 Release Notes. https://www.postgresql.org/docs/release/18.0/
15. PostgreSQL 18, Locale Support. https://www.postgresql.org/docs/18/locale.html
16. PostgreSQL 12.0 Release Notes. https://www.postgresql.org/docs/release/12.0/
17. Imagen oficial `postgres` en Docker Hub. https://hub.docker.com/_/postgres
18. WireBear, Studio Server. https://wirebear.co.uk/software/studio-server
19. WireBear, Studio Server Client. https://wirebear.co.uk/software/studio-server-client
20. Docker Hub, `allebb/studio-server`. https://hub.docker.com/r/allebb/studio-server
21. Foro de Studio Server, "Studio Server v3 is coming soon" (2026-09-06).
    https://forums.studioserver.net/d/2-studio-server-v3-is-coming-soon
22. Blackmagic Cloud, "Accessing Blackmagic Cloud from DaVinci Resolve".
    https://help.cloud.blackmagicdesign.com/getting-started/bmcloud-and-davinci-resolve/
23. Foro de Blackmagic, "Reconnect to database that already exists".
    https://forum.blackmagicdesign.com/viewtopic.php?p=743090
24. Guía de terceros para 21.1 sobre comunicación entre colaboradores (fuente no oficial).
    https://davinciresolve21.com/blog/davinci-resolve-21-1-cannot-communicate-with-collaborators
25. PostgreSQL 18, Predefined Roles. https://www.postgresql.org/docs/18/predefined-roles.html
26. TrueNAS, Install Custom App Screens (26). https://www.truenas.com/docs/scale/26/apps/installcustomappscreens/
27. Catálogo de apps de TrueNAS, app `postgres`. https://github.com/truenas/apps/tree/master/ix-dev/community/postgres
