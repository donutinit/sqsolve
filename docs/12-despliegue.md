# 12. Despliegue, persistencia y configuración

## 1. Modelo

Un archivo `compose.yaml` es la única forma oficial de instalar. Sirve igual en:

- cualquier Linux con Docker Engine 24+ y Compose v2 (`docker compose up -d`);
- NAS o paneles que aceptan Compose: TrueNAS ("Apps > Discover > Custom App > Install via
  YAML"), Portainer (Stacks), Unraid (plugin Compose Manager), Synology Container Manager
  (Proyectos), y similares.

No hay plantillas ni integraciones específicas de ningún NAS. Las diferencias entre plataformas
(rutas de volúmenes, IP donde publicar 5432) se resuelven editando dos o tres líneas del YAML.

## 2. Volúmenes

Tres rutas visibles (ADR 0018):

| Volumen | Ruta en contenedores | Contenido | Recomendación |
|---|---|---|---|
| db | `/var/lib/postgresql` | Clúster de PostgreSQL (`18/docker`) y la configuración que genera sqsolve (`sqsolve/`) | SSD; sistema de archivos con instantáneas si hay |
| app | `/var/lib/sqsolve` | SQLite, secretos de sqsolve, certificado del panel | SSD; no se copia junto con los respaldos externos |
| respaldos | `/backups` | Respaldos, manifiestos, `_system`, `_import` | **Otro disco o pool** |

Más un volumen con nombre interno para el socket de PostgreSQL, sin datos.

Sugerencia para ZFS (no obligatoria): db con `recordsize=16K` y `compression=lz4`; respaldos con
`recordsize=1M`. Una reserva en el dataset de db evita que un disco lleno por otra cosa detenga
PostgreSQL.

## 3. Compose de referencia

El archivo vigente es [`deploy/compose.yaml`](../deploy/compose.yaml) y CI lo levanta en cada
cambio (trabajo `smoke`). Servicios:

- `sqsolve-init`: como root y sin red, prepara volúmenes, secretos, `postgresql.conf`,
  `pg_hba.conf` inicial y certificado TLS de PostgreSQL. Corre en cada `up` y es idempotente.
- `postgres`: imagen oficial fijada por digest, arrancada con
  `postgres -c config_file=/var/lib/postgresql/sqsolve/postgresql.conf` y con el usuario 999
  de la imagen (`user: "999:999"`); nunca corre como root, igual que en las apps de TrueNAS.
- `sqsolve`: usuario sin privilegios, sistema de archivos de solo lectura, sin capacidades;
  publica 9432. Arranca aunque PostgreSQL no esté listo, para poder diagnosticarlo.

Para instalar se cambian las tres rutas, `TZ` y, si hace falta, la IP donde se publica 5432.

## 4. Variables de entorno

| Variable | Por defecto | Uso |
|---|---|---|
| `TZ` | `UTC` | Zona horaria del contenedor y valor inicial de las políticas |
| `SQSOLVE_UID`, `SQSOLVE_GID` | `1000` | Dueño de los volúmenes de sqsolve (lo aplica `sqsolve-init`); debe coincidir con `user:` |
| `SQSOLVE_LISTEN` | `:9432` | Dirección del panel HTTPS |
| `SQSOLVE_HTTP_REDIRECT` | vacío | Si se define (`:9080`), escucha HTTP solo para redirigir a HTTPS |
| `SQSOLVE_ADVERTISED_HOST` | vacío | Dirección que verán las estaciones; si está vacío, se define en el asistente |
| `SQSOLVE_APP_DIR` | `/var/lib/sqsolve` | Volumen app |
| `SQSOLVE_BACKUP_DIR` | `/backups` | Volumen de respaldos |
| `SQSOLVE_PG_DIR` | `/var/lib/postgresql` | Volumen db; la configuración va en `sqsolve/` dentro |
| `SQSOLVE_PG_DATA_DIR` | `/var/lib/postgresql/18/docker` | PGDATA de la imagen oficial 18 |
| `SQSOLVE_PG_SOCKET_DIR` | `/var/run/postgresql` | Socket |
| `SQSOLVE_PG_PORT` | `5432` | Puerto de PostgreSQL |
| `SQSOLVE_PG_LISTEN` | `*` | `listen_addresses` de PostgreSQL |
| `SQSOLVE_PG_UID`, `SQSOLVE_PG_GID` | `999` | Usuario de PostgreSQL en la imagen oficial |
| `SQSOLVE_BACKUPS_ADOPT` | vacío | `1` permite marcar un volumen de respaldos que ya tiene archivos |
| `SQSOLVE_FIX_PERMS` | vacío | `1` corrige dueños de forma recursiva en `init` |
| `SQSOLVE_INSECURE_HTTP` | vacío | Solo desarrollo y pruebas: panel por HTTP |
| `SQSOLVE_PG_MEMORY_MB` | `2048` | Memoria que se asume para PostgreSQL al calcular `shared_buffers` |
| `SQSOLVE_PG_MAX_CONNECTIONS` | `200` | Se escribe en `postgresql.conf` (requiere reiniciar PostgreSQL) |
| `SQSOLVE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `SQSOLVE_LOG_FORMAT` | `json` | `json` o `text` |
| `SQSOLVE_TRUSTED_PROXIES` | vacío | CIDR de proxies inversos de confianza |
| `SQSOLVE_WORKERS` | `2` | Workers de tareas |
| `SQSOLVE_HEAVY_JOBS` | `1` | Tareas pesadas simultáneas |

## 5. Primer arranque

1. `sqsolve-init` (como root, sin red): crea el marcador de `backups` si no existe y el volumen
   está vacío (si hay contenido sin marcador, se detiene con error para no adoptar una carpeta
   equivocada; si el volumen ya trae el marcador de otra instalación y app está vacío, adopta su
   identidad para una recuperación); genera secretos que falten con `crypto/rand`; genera
   `postgresql.conf` y un `pg_hba.conf` inicial; genera el certificado TLS de PostgreSQL
   (autofirmado, ECDSA P-256, 10 años, con dueño root y grupo 999 en modo 0640 como exige
   PostgreSQL); crea la carpeta de datos de PostgreSQL con dueño 999 y modo 0700, y se detiene
   con `SQS-C054` si el volumen no conserva ese modo (ACL de tipo NFSv4); ajusta dueños y
   permisos; termina.
2. `postgres` inicializa el clúster la primera vez con la contraseña del superusuario.
3. `sqsolve` crea o repara `sqsolve_admin` con el superusuario por socket, imprime el **código
   de instalación** en su registro si aún no hay administrador y abre el asistente.

El certificado de PostgreSQL dura 10 años porque Resolve no lo verifica (PoC-06): su función es
cifrar, no identificar. El del panel dura 2 años y sqsolve lo renueva solo; el administrador
puede cargar uno propio.

## 6. Plataformas

| Plataforma | Notas |
|---|---|
| Linux genérico | `docker compose up -d` en la carpeta del `compose.yaml` |
| TrueNAS | Install via YAML con el mismo archivo; cambiar `/ruta/a/respaldos` por la ruta de un dataset (`/mnt/<pool>/...`) y, si se desea, mapear los demás volúmenes a datasets. TrueNAS usa los puertos 80 y 443 para su interfaz, no 9432 ni 5432 |
| Portainer | Stack con el YAML |
| Unraid | Plugin Compose Manager con el YAML |
| Synology | Publicar 5432 en la IP de la LAN porque DSM ocupa 5432 en localhost [01 §13] |
| Podman | Compatible con `podman compose` en principio; documentado como "comunidad" hasta probarlo |
| ARM64 | Mismas imágenes multiarquitectura |

## 7. Red y nombres

- El servidor necesita una IP fija (reserva DHCP o estática). La dirección anunciada puede ser
  un nombre DNS local; PoC-08 confirma si Resolve acepta nombres en la ubicación.
- Firewall del host: permitir 5432 y 9432 desde la LAN o la VPN; nada desde Internet.
- Estaciones: permitir TCP 50059 a 50314 entrante y saliente entre ellas para colaborar (PoC-07).

## 8. Actualizaciones

### 8.1 sqsolve

1. Cambiar la etiqueta de la imagen (o `docker compose pull` si se usa una etiqueta de versión
   menor como `1.0`).
2. `docker compose up -d`.
3. Al arrancar: copia `VACUUM INTO` de SQLite, migraciones, reconciliación, evento
   `system.upgraded` con versión anterior y nueva.

Volver atrás: detener, restaurar la copia de SQLite de `state/backups/` correspondiente y usar la
imagen anterior. Las migraciones de SQLite nunca borran columnas en una versión menor, así que
volver a la versión menor anterior funciona sin restaurar en la mayoría de los casos; las notas
de versión lo indican.

### 8.2 PostgreSQL versión menor

Cada versión de sqsolve fija el digest de una versión menor reciente de PostgreSQL 18. Actualizar
sqsolve actualiza el YAML de referencia; el administrador aplica el cambio de imagen. Antes de
reiniciar PostgreSQL, la interfaz ofrece "respaldar todo ahora".

### 8.3 PostgreSQL versión mayor (2.0)

Por migración lógica a un clúster nuevo (ADR 0017): volumen `pgdata` nuevo, contenedor temporal
con la versión nueva, respaldo y restauración de cada biblioteca con verificación L2, cambio de
servicios en el YAML, y el volumen anterior intacto hasta que el administrador lo borre. Nunca
`pg_upgrade` sobre el directorio de datos en uso.

## 9. Respaldo de la instalación y recuperación ante desastre

Qué guardar fuera del servidor: el volumen `backups` (o su copia restic) y el kit de recuperación.

Recuperar en un servidor nuevo:

1. Instalar con el YAML de referencia apuntando `backups` a la copia de los respaldos.
2. En el asistente elegir "Recuperar instalación existente" y pegar la clave maestra del kit.
3. sqsolve restaura el plano de control desde `_system/` (usuarios, políticas, credenciales
   cifradas) o, si no hay, reconstruye el catálogo desde los manifiestos.
4. "Restaurar todas" crea cada biblioteca desde su respaldo verificado más reciente con su mismo
   slug y las contraseñas originales (si la clave maestra está disponible; si no, genera nuevas).
5. Las estaciones se reconectan con la nueva IP si cambió.

## 10. Desinstalar

`docker compose down` conserva todos los volúmenes. Borrar datos es un paso explícito y separado
(`docker volume rm ...` o borrar carpetas), documentado con advertencias.
