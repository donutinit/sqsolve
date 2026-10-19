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

| Volumen | Ruta en contenedor | Contenido | Recomendación de almacenamiento |
|---|---|---|---|
| `pgdata` | `/var/lib/postgresql` (en `postgres`) | Clúster de PostgreSQL (`18/docker` dentro) | SSD; sistema de archivos con instantáneas si hay |
| `pgconf` | `/etc/sqsolve/pg` | `postgresql.conf`, `pg_hba.conf`, `tls/` | Mismo disco que `pgdata` |
| `pgsocket` | `/var/run/postgresql` | Socket Unix compartido | Volumen con nombre (no hace falta ruta del host) |
| `state` | `/var/lib/sqsolve` | SQLite, certificados del panel, plantillas, copias previas a migraciones | SSD |
| `secrets` | `/run/sqsolve/secrets` | Contraseñas y clave maestra | Mismo disco que `state`; fuera de respaldos compartidos |
| `backups` | `/backups` | Respaldos, manifiestos, plano de control, auditoría | **Otro disco o pool**; puede ser HDD o una carpeta NFS/SMB montada en el host |
| `hooks` (1.1) | `/hooks` (en `sqsolve-hooks`) | Ejecutables del administrador | Ruta del host editable solo por el administrador |

Por defecto todos son volúmenes con nombre de Docker, salvo `backups`, que se recomienda como
ruta del host para que viva en otro disco. En un NAS conviene mapear también `pgdata`, `state` y
`secrets` a carpetas visibles para que entren en sus instantáneas y réplicas.

Sugerencia para ZFS (no obligatoria): `pgdata` con `recordsize=16K` y `compression=lz4`;
`backups` con `recordsize=1M`. Reserva (`reservation`) en el dataset de `pgdata` para que un disco
lleno por otra cosa no detenga PostgreSQL.

## 3. Compose de referencia

Borrador normativo; la versión definitiva se mantiene en `deploy/compose.yaml` (SQS-030) y se
prueba en CI.

```yaml
name: sqsolve

x-sqsolve-image: &sqsolve-image "ghcr.io/POR-CONFIRMAR/sqsolve:1.0.0"
x-postgres-image: &postgres-image "postgres:18.6-trixie@sha256:74935e72241653ca55e0414067e6d8763aceb8a810eb51b452253ec3dcfc4336"

x-common-env: &common-env
  TZ: "America/Mexico_City"
  SQSOLVE_UID: "1000"
  SQSOLVE_GID: "1000"

services:
  sqsolve-init:
    image: *sqsolve-image
    command: ["sqsolve", "init"]
    user: "0:0"
    network_mode: none
    environment: *common-env
    volumes:
      - pgconf:/etc/sqsolve/pg
      - pgsocket:/var/run/postgresql
      - state:/var/lib/sqsolve
      - secrets:/run/sqsolve/secrets
      - /ruta/a/respaldos:/backups
    restart: "no"

  postgres:
    image: *postgres-image
    command: ["postgres", "-c", "config_file=/etc/sqsolve/pg/postgresql.conf"]
    environment:
      TZ: "America/Mexico_City"
      POSTGRES_PASSWORD_FILE: /run/sqsolve/secrets/pg_superuser_password
      POSTGRES_INITDB_ARGS: "--encoding=UTF8 --locale-provider=builtin --builtin-locale=C.UTF-8"
    volumes:
      - pgdata:/var/lib/postgresql
      - pgconf:/etc/sqsolve/pg:ro
      - pgsocket:/var/run/postgresql
      - secrets:/run/sqsolve/secrets:ro
    ports:
      - "5432:5432"            # en Synology: "192.168.1.10:5432:5432"
    shm_size: 256mb
    depends_on:
      sqsolve-init:
        condition: service_completed_successfully
    healthcheck:
      test: ["CMD", "pg_isready", "-h", "/var/run/postgresql"]
      interval: 10s
      timeout: 5s
      retries: 6
    security_opt: ["no-new-privileges:true"]
    stop_grace_period: 60s
    restart: unless-stopped

  sqsolve:
    image: *sqsolve-image
    command: ["sqsolve", "serve"]
    user: "1000:1000"
    environment:
      <<: *common-env
      SQSOLVE_LISTEN: ":9432"
    volumes:
      - pgconf:/etc/sqsolve/pg
      - pgsocket:/var/run/postgresql
      - state:/var/lib/sqsolve
      - secrets:/run/sqsolve/secrets:ro
      - /ruta/a/respaldos:/backups
      - pgdata:/srv/pgdata:ro      # solo para medir espacio con statfs
    ports:
      - "9432:9432"
    depends_on:
      sqsolve-init:
        condition: service_completed_successfully
      postgres:
        condition: service_started
    read_only: true
    tmpfs:
      - /tmp:size=64m,mode=1777
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    healthcheck:
      test: ["CMD", "sqsolve", "healthcheck"]
      interval: 30s
      timeout: 5s
      retries: 3
    stop_grace_period: 120s
    restart: unless-stopped

volumes:
  pgdata:
  pgconf:
  pgsocket:
  state:
  secrets:
```

Notas:

- `sqsolve` arranca aunque PostgreSQL no esté listo (`service_started`, no `service_healthy`):
  el panel debe estar disponible para diagnosticar un PostgreSQL que no levanta.
- `stop_grace_period` de 120 s da tiempo a terminar un paso de tarea o dejarla en estado
  recuperable. Al recibir SIGTERM, sqsolve deja de aceptar tareas nuevas, cancela las cancelables
  y espera a que el intercambio en curso (si hay) llegue a un estado del diario.
- En 1.1 se agrega el servicio opcional `sqsolve-hooks` bajo el perfil `hooks`
  (`docker compose --profile hooks up -d`).

## 4. Variables de entorno

| Variable | Por defecto | Uso |
|---|---|---|
| `TZ` | `UTC` | Zona horaria del contenedor y valor inicial de las políticas |
| `SQSOLVE_UID`, `SQSOLVE_GID` | `1000` | Dueño de los volúmenes de sqsolve (lo aplica `sqsolve-init`); debe coincidir con `user:` |
| `SQSOLVE_LISTEN` | `:9432` | Dirección del panel HTTPS |
| `SQSOLVE_HTTP_REDIRECT` | vacío | Si se define (`:9080`), escucha HTTP solo para redirigir a HTTPS |
| `SQSOLVE_ADVERTISED_HOST` | vacío | Dirección que verán las estaciones; si está vacío, se define en el asistente |
| `SQSOLVE_DATA_DIR` | `/var/lib/sqsolve` | Estado |
| `SQSOLVE_BACKUP_DIR` | `/backups` | Respaldos |
| `SQSOLVE_SECRETS_DIR` | `/run/sqsolve/secrets` | Secretos |
| `SQSOLVE_PGCONF_DIR` | `/etc/sqsolve/pg` | Configuración de PostgreSQL |
| `SQSOLVE_PG_SOCKET_DIR` | `/var/run/postgresql` | Socket |
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
   equivocada); genera secretos que falten con `crypto/rand`; genera `postgresql.conf` y un
   `pg_hba.conf` inicial; genera el certificado TLS de PostgreSQL (autofirmado, ECDSA P-256,
   10 años, con dueño root y grupo 999 en modo 0640 como exige PostgreSQL); ajusta dueños y
   permisos; imprime el **código de instalación** si aún no hay administrador; termina.
2. `postgres` inicializa el clúster la primera vez con la contraseña del superusuario.
3. `sqsolve` crea `sqsolve_admin` con el superusuario por socket (una sola vez, auditado), abre
   el asistente y espera.

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
