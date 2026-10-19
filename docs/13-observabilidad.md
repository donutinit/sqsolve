# 13. Observabilidad, registros y diagnóstico

## 1. Registros de proceso

- `log/slog` a la salida estándar (la recoge Docker y la muestra cualquier panel de NAS).
- JSON por defecto; texto legible con `SQSOLVE_LOG_FORMAT=text`.
- Campos fijos: `time`, `level`, `msg`, `component`, y cuando aplica `request_id`, `job_id`,
  `library`, `user`, `code`.
- Redacción: los valores de tipo `secret.String` y las claves `password`, `token`, `secret`,
  `key`, `authorization`, `cookie` salen como `[redactado]`. Prueba automática: se ejecuta la
  batería de integración con secretos marcados y se busca cada uno en toda la salida.
- Peticiones HTTP: una línea por petición con método, ruta (sin consulta), estado, duración y
  usuario; nivel `debug` para recursos estáticos.

## 2. Eventos y auditoría

- **Eventos** (`events` en SQLite): hechos de negocio para personas; tipos en `10-api.md` §4;
  retención 180 días.
- **Auditoría** (`audit_log`): acciones de actores, encadenada por hash (`08-seguridad.md` §9);
  retención indefinida en SQLite con exportación mensual a `backups/_system/`.
- **Registros de tareas** (`job_logs`): lo que vería alguien mirando la terminal; retención 90
  días.

Los tres se consultan y filtran en la interfaz y se exportan en JSON Lines.

## 3. Salud

| Endpoint | Autenticación | Qué responde |
|---|---|---|
| `GET /healthz` | No | `200` si el proceso atiende (para el `healthcheck` de Docker vía `sqsolve healthcheck`) |
| `GET /readyz` | No | `200` si SQLite está abierto y los volúmenes tienen marcador; `503` con lista de fallos sin detalles sensibles |
| `GET /api/v1/system/health` | No | Igual que `readyz` más estado de PostgreSQL |

PostgreSQL caído no hace fallar `healthz`: reiniciar sqsolve no arregla PostgreSQL y quitaría el
panel.

## 4. Indicadores del panel

| Indicador | Fuente | Aviso | Error |
|---|---|---|---|
| PostgreSQL disponible | `SELECT 1` cada 15 s | Latencia mayor a 500 ms | Sin respuesta 3 veces seguidas |
| Conexiones totales | `pg_stat_activity` | 80 % de `max_connections` | 95 % |
| Espacio en `pgdata`, `state`, `backups` | `statfs` de cada volumen (`pgdata` montado en solo lectura en `/srv/pgdata`, solo para medir) | 80 % | 90 % |
| Último respaldo verificado por biblioteca | Catálogo | Más de 26 h | Más de 48 h |
| Última verificación L2 | Catálogo | Más de 8 días | Fallida |
| Tareas fallidas en 24 h | `jobs` | 1 | 3 |
| Certificados | Archivos | Menos de 30 días | Vencido |
| Integridad de auditoría | Verificación diaria | | Rota |
| Versión de colación | `pg_database.datcollversion` frente a la real | Distinta | |
| Sesiones inactivas en transacción | `pg_stat_activity.state = 'idle in transaction'` por más de 1 h | Sí | |

## 5. Métricas (1.2)

`GET /metrics` en formato de exposición de Prometheus, protegido con token propio. Escrito a mano
(pocas series, sin dependencia):

```
sqsolve_build_info{version,commit,go_version} 1
sqsolve_postgres_up 1
sqsolve_library_size_bytes{library}
sqsolve_library_connections{library}
sqsolve_backup_last_success_timestamp_seconds{library}
sqsolve_backup_last_verified_timestamp_seconds{library}
sqsolve_backup_duration_seconds{library}           (último)
sqsolve_backup_size_bytes{library}                 (último)
sqsolve_jobs{state}
sqsolve_volume_free_bytes{volume}
sqsolve_volume_size_bytes{volume}
sqsolve_http_requests_total{code_class}
```

## 6. Paquete de diagnóstico

Tarea `system.diagnostics`, genera `sqsolve-diag-<marca>.zip` descargable una vez:

- versiones (sqsolve, Go, PostgreSQL, imagen), variables `SQSOLVE_*` sin secretos;
- salida de `sqsolve doctor --json`;
- `postgresql.conf` y `pg_hba.conf` generados (sin secretos por diseño);
- `pg_settings` relevantes, lista de bases con tamaño, dueño y estado de conexión, roles con
  atributos (sin contraseñas);
- últimos 1000 eventos y registros de las últimas 20 tareas fallidas;
- últimas 2000 líneas del registro de proceso disponibles en memoria (búfer circular);
- catálogo de respaldos sin rutas absolutas del host.

Nunca incluye: secretos, contraseñas cifradas, contenido de bibliotecas, la auditoría completa ni
nombres de proyectos. La interfaz lista el contenido antes de descargar.

## 7. `sqsolve doctor`

Comprobaciones con resultado `ok`, `warn` o `fail` y una sugerencia por fallo:

1. Volúmenes montados, marcadores, permisos y espacio.
2. Secretos presentes y con permisos 0400.
3. Conexión por socket como `sqsolve_admin` y como superusuario.
4. `pg_hba.conf` vigente coincide con el que generaría el catálogo.
5. Roles y bases coinciden con el catálogo (bases huérfanas, roles huérfanos, bibliotecas sin
   base).
6. Catálogo coincide con archivos del volumen de respaldos.
7. Cadena de auditoría íntegra.
8. Certificados vigentes.
9. Tareas interrumpidas pendientes de limpieza.
