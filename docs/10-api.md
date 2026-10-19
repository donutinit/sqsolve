# 10. API y contratos

## 1. Convenciones de la API REST

- Base: `https://<servidor>:9432/api/v1`. JSON UTF-8. Contrato en `api/openapi.yaml`
  (OpenAPI 3.1), que se escribe antes que los handlers y se valida en pruebas (SQS-410).
- Autenticación: `Authorization: Bearer sqs_...` (tokens con alcance) o cookie de sesión más
  cabecera `X-CSRF-Token` (uso desde el navegador).
- Errores: RFC 9457 `application/problem+json`:

```json
{
  "type": "https://sqsolve.example/errores/SQS-B012",
  "title": "Espacio insuficiente en el volumen de respaldos",
  "status": 507,
  "detail": "Se necesitan 3.1 GB y hay 1.2 GB libres en /backups.",
  "code": "SQS-B012",
  "instance": "/api/v1/libraries/01JB.../backups"
}
```

  El dominio de `type` es Por confirmar; mientras tanto se usa la ruta relativa
  `/docs/errores/SQS-B012`.
- Operaciones largas: `202 Accepted` con `Location: /api/v1/jobs/{id}` y el objeto tarea.
- Idempotencia: los `POST` que crean o lanzan aceptan `Idempotency-Key`; la misma clave en 24 h
  devuelve la misma respuesta.
- Concurrencia optimista: recursos editables devuelven `ETag`; `PATCH` exige `If-Match`.
- Paginación por cursor: `?limit=50&cursor=...`, respuesta con `next_cursor`.
- Fechas RFC 3339 en UTC. Tamaños en bytes. Identificadores ULID.
- Operaciones peligrosas exigen en el cuerpo `"confirm": "<slug>"` y, con sesión, reautenticación
  de los últimos 5 min (`SQS-A007` si falta); con token, el alcance `admin` o `restore`.

## 2. Recursos

| Método y ruta | Alcance mínimo | Descripción |
|---|---|---|
| `GET /system/status` | `read` | Estado resumido (lo que muestra el panel) |
| `GET /system/health` | sin autenticación | `200` o `503` con detalle mínimo (para monitores) |
| `GET /system/version` | `read` | Versiones de sqsolve, PostgreSQL, matriz de compatibilidad |
| `GET /libraries` | `read` | Lista con filtros `state` |
| `POST /libraries` | `admin` | Crear; devuelve tarea |
| `GET /libraries/{id}` | `read` | Detalle |
| `PATCH /libraries/{id}` | `admin` | Nombre visible, notas, límite, versión declarada |
| `POST /libraries/{id}/init-window` | `admin` | Reabrir ventana de inicialización |
| `POST /libraries/{id}/maintenance` | `admin` | `{ "enabled": true, "terminate_sessions": false }` |
| `DELETE /libraries/{id}` | `admin` + confirmación | A la papelera; devuelve tarea |
| `POST /libraries/{id}/restore-from-trash` | `admin` | Recuperar de la papelera |
| `GET /libraries/{id}/connection` | `read` | Ubicación, base, usuario; sin contraseña |
| `POST /libraries/{id}/connection/reveal` | `admin` + reautenticación | Contraseña; auditado |
| `GET /libraries/{id}/resolvedbkey` | `admin` | Archivo de llave (si el módulo está activo) |
| `GET /libraries/{id}/credentials` | `admin` | Lista sin contraseñas |
| `POST /libraries/{id}/credentials` | `admin` | Crear credencial de persona (1.1) |
| `POST /libraries/{id}/credentials/{cid}/rotate` | `admin` | Rotar |
| `POST /libraries/{id}/credentials/{cid}/revoke` | `admin` | Revocar (1.1) |
| `GET/PUT /libraries/{id}/network-rules` | `admin` | Reglas CIDR (1.1) |
| `GET /libraries/{id}/sessions` | `read` | Sesiones activas |
| `DELETE /libraries/{id}/sessions/{pid}` | `admin` | Cerrar sesión |
| `GET/PUT /libraries/{id}/backup-policy` | `read` / `admin` | Programación y retención |
| `GET /libraries/{id}/backup-policy/preview` | `read` | Próximas ejecuciones y efecto de la retención |
| `GET /libraries/{id}/backups` | `read` | Catálogo de la biblioteca |
| `POST /libraries/{id}/backups` | `backup` | Respaldar ahora; devuelve tarea |
| `GET /backups` | `read` | Catálogo global |
| `GET /backups/{id}` | `read` | Detalle con manifiesto |
| `GET /backups/{id}/download` | `restore` | Descarga del archivo (`Content-Disposition`) |
| `POST /backups/{id}/verify` | `backup` | `{ "level": 2 }`; devuelve tarea |
| `POST /backups/{id}/pin` / `unpin` | `admin` | Fijar o soltar |
| `POST /backups/{id}/restore` | `restore` o `admin` | `{ "mode": "new", "slug": "...", "display_name": "..." }` o `{ "mode": "replace", "confirm": "<slug>", "terminate_sessions": false }` |
| `POST /backups/import` | `admin` | Subida `multipart/form-data` o `{ "path": "_import/archivo.backup", "library_slug": "..." }` |
| `POST /libraries/{id}/revert` | `admin` + confirmación | Revertir el último intercambio dentro del plazo |
| `GET /jobs` | `read` | Filtros `state`, `type`, `library_id` |
| `GET /jobs/{id}` | `read` | Detalle con pasos |
| `GET /jobs/{id}/logs` | `read` | Registro paginado |
| `GET /jobs/{id}/events` | `read` | SSE de progreso |
| `POST /jobs/{id}/cancel` | según tipo | Cancelar |
| `GET /events` | `read` | Eventos con filtros |
| `GET /audit` | `admin` | Auditoría; `?format=jsonl` para exportar |
| `GET /audit/verify` | `admin` | Verificar la cadena |
| `GET/POST/PATCH/DELETE /users[...]` | `admin` | Usuarios del panel |
| `GET/POST/DELETE /tokens[...]` | propio o `admin` | Tokens |
| `GET/POST/PATCH/DELETE /notifications/channels[...]` | `admin` | Canales |
| `POST /notifications/channels/{id}/test` | `admin` | Envío de prueba |
| `GET/PUT /notifications/rules` | `admin` | Reglas |
| `GET/PATCH /settings` | `admin` | Ajustes de ejecución |
| `POST /maintenance/upgrade-window` | `admin` | Abrir ventana de actualización de Resolve |
| `POST /maintenance/catalog/rebuild` | `admin` | Reconstruir catálogo desde manifiestos |
| `POST /maintenance/diagnostics` | `admin` | Generar paquete de diagnóstico; devuelve tarea |
| `GET /metrics` | token de métricas | Formato de exposición de Prometheus (1.2) |

## 3. Objeto tarea

```json
{
  "id": "01JB7Y6Z3K9M2Q8R4T5V6W7X8Y",
  "type": "backup.create",
  "state": "running",
  "library_id": "01JB7Y...",
  "progress": 40,
  "current_step": "dump",
  "steps": [
    { "name": "preflight", "state": "succeeded", "started_at": "...", "finished_at": "..." },
    { "name": "dump", "state": "running", "started_at": "..." }
  ],
  "created_by": { "type": "schedule", "id": "01JB..." },
  "created_at": "2026-10-18T07:00:00Z",
  "error": null
}
```

Tipos de tarea: `library.create`, `library.init_watch`, `library.delete`, `library.purge`,
`library.maintenance`, `library.fingerprint`, `backup.create`, `backup.verify`,
`backup.retention`, `backup.import`, `backup.offsite_sync` (1.1), `restore.new`,
`restore.replace`, `restore.revert`, `system.vacuum`, `system.control_backup`,
`system.catalog_check`, `system.diagnostics`, `system.upgrade_window`.

## 4. Catálogo de eventos

| Tipo | Severidad | Cuándo |
|---|---|---|
| `library.created`, `library.activated`, `library.trashed`, `library.purged`, `library.restored_from_trash` | info | Ciclo de vida |
| `library.init_expired` | warning | Venció la ventana |
| `library.schema_changed` | warning | Cambió la huella |
| `library.maintenance_on`, `library.maintenance_off` | info | Bloqueos |
| `library.credential_rotated`, `library.credential_revealed` | info | Credenciales |
| `backup.started`, `backup.succeeded` | info | Respaldos |
| `backup.failed`, `backup.verify_failed`, `backup.missed` | error | Problemas de respaldo |
| `backup.stale` | warning | Biblioteca sin respaldo verificado en más del umbral (48 h) |
| `backup.expired` | info | La retención borró un respaldo |
| `restore.started`, `restore.succeeded`, `restore.reverted` | info | Restauración |
| `restore.failed` | error | Restauración fallida (la biblioteca activa no se tocó o se revirtió) |
| `storage.low` | warning a 80 %, error a 90 % | Espacio |
| `storage.volume_missing` | critical | Marcador de volumen ausente |
| `postgres.down`, `postgres.up` | critical, info | Disponibilidad |
| `auth.login_failed_burst` | warning | Fuerza bruta probable |
| `audit.chain_broken` | critical | Integridad de la auditoría |
| `tls.expiring` | warning | Certificado a menos de 30 días |
| `job.interrupted` | warning | Tarea cortada por reinicio |

## 5. Webhooks salientes

- `POST` a la URL del canal con `Content-Type: application/json`.
- Cabeceras: `X-Sqsolve-Event` (tipo), `X-Sqsolve-Delivery` (ULID), `X-Sqsolve-Timestamp`
  (segundos Unix), `X-Sqsolve-Signature: sha256=<hex>` con HMAC-SHA256 sobre
  `timestamp + "." + cuerpo` y la clave del canal.
- Cuerpo:

```json
{
  "id": "01JB...",
  "type": "backup.failed",
  "severity": "error",
  "occurred_at": "2026-10-18T07:00:12Z",
  "instance": "estudio-principal",
  "library": { "id": "01JB...", "slug": "cliente_x", "display_name": "Cliente X" },
  "job": { "id": "01JB...", "type": "backup.create" },
  "message": "Espacio insuficiente en el volumen de respaldos",
  "code": "SQS-B012",
  "links": { "job": "https://nas.local:9432/tareas/01JB..." }
}
```

- Reintentos a los 10 s, 1 min, 10 min y 1 h; luego se marca el canal con error. Tiempo máximo
  por envío 10 s. Sin seguir redirecciones.
- Los eventos "antes de" (`backup.started`, `restore.started`) se envían pero no bloquean la
  operación. El bloqueo previo es de los hooks (1.1).

## 6. Contrato de hooks (1.1)

- El administrador coloca ejecutables en el volumen `hooks`: `hooks/<evento>.d/<orden>-<nombre>`
  (por ejemplo `hooks/backup.succeeded.d/10-subir`).
- sqsolve deja una solicitud JSON en el volumen compartido `hookspool` y el contenedor
  `sqsolve-hooks` la ejecuta: el JSON del evento llega por la entrada estándar; variables de
  entorno solo `SQSOLVE_EVENT`, `SQSOLVE_DELIVERY` y `SQSOLVE_BACKUP_PATH` (ruta de solo lectura
  dentro del contenedor de hooks) cuando aplica.
- Tiempo máximo 300 s configurable; salida estándar y de error guardadas en el registro de la
  tarea (truncadas a 64 KiB).
- Hooks `pre` (`backup.pre.d`, `restore.pre.d`) pueden marcarse como bloqueantes: un código de
  salida distinto de 0 cancela la operación antes de tocar nada.

## 7. CLI

El mismo binario, para recuperación y automatización local:

```
sqsolve serve
sqsolve init                         # usado por sqsolve-init
sqsolve doctor [--json]              # comprueba volúmenes, permisos, PostgreSQL, catálogo
sqsolve admin create --username U --role admin
sqsolve admin reset-password U
sqsolve admin disable-totp U
sqsolve admin rotate-master-key
sqsolve admin rotate-superuser
sqsolve library list [--json]
sqsolve backup create <slug>
sqsolve backup list <slug> [--json]
sqsolve backup verify <id> [--level 2]
sqsolve restore <id> --new <slug-nuevo>
sqsolve restore <id> --replace --confirm <slug>
sqsolve catalog rebuild
sqsolve audit verify
sqsolve version
```

Las acciones de la CLI pasan por los mismos servicios, quedan en la auditoría con actor `cli` y
respetan los bloqueos de tareas.

## 8. Formatos internos

### 8.1 Manifiesto de respaldo

Archivo `<nombre>.manifest.json` junto a cada respaldo. Esquema JSON en
`api/schemas/manifest.v1.json`.

```json
{
  "manifest_version": 1,
  "backup_id": "01JB...",
  "library": { "id": "01JB...", "slug": "cliente_x", "display_name": "Cliente X" },
  "kind": "scheduled",
  "file": "cliente_x_20261018T070000Z_01JB.backup",
  "size_bytes": 184467221,
  "sha256": "9f2c...",
  "format": { "tool": "pg_dump", "tool_version": "18.6", "format": "custom", "compression": "gzip:6", "archive_version": "<leída del encabezado>" },
  "server": { "postgres_version": "18.6", "encoding": "UTF8", "locale_provider": "builtin", "locale": "C.UTF-8" },
  "schema": { "fingerprint": "sha256:51ab...", "tables": 214 },
  "resolve_version_declared": "21.1.1",
  "row_counts": { "tabla_a": 120, "tabla_b": 98431 },
  "started_at": "2026-10-18T07:00:00Z",
  "finished_at": "2026-10-18T07:00:41Z",
  "sqsolve_version": "1.0.0",
  "instance_id": "01JA..."
}
```

`archive_version` se lee del encabezado del archivo con `pg_restore --list`. Los nombres de
tablas en `row_counts` son metadatos de catálogo; no contienen datos de proyectos.

### 8.2 Diario de pasos de restauración

Cada fila de `job_steps` de `restore.replace` guarda en `data` el estado necesario para decidir
al arrancar: nombres de base involucrados, si el renombre se emitió y si se confirmó. Ver
`11-respaldos-y-automatizacion.md` §7.3.

## 9. Versionado

- La API `v1` solo cambia de forma compatible: campos nuevos opcionales, endpoints nuevos.
  Un cambio incompatible crea `v2` y `v1` se mantiene al menos dos versiones menores de sqsolve.
- El manifiesto lleva `manifest_version`; sqsolve lee todas las versiones anteriores.
- Los eventos y webhooks agregan campos sin quitar ni renombrar.
