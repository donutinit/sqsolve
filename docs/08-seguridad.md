# 08. Seguridad

## 1. Activos

| Activo | Por qué importa |
|---|---|
| Bibliotecas (metadatos de proyectos) | Meses de trabajo de edición y color; difíciles de reconstruir |
| Respaldos | Copia de todo lo anterior; un atacante que los lea tiene los proyectos |
| Credenciales de bibliotecas | Dan lectura y escritura total a una biblioteca |
| Cuentas del panel y tokens | Controlan todo lo demás |
| Clave maestra y secretos | Descifran credenciales guardadas |
| Disponibilidad | Un servidor caído detiene a todo el estudio |

## 2. Actores y fronteras

| Actor | Confianza |
|---|---|
| Administrador con acceso al host | Total (puede leer volúmenes). sqsolve no intenta protegerse de él |
| Administrador u operador del panel | Según su rol |
| Estación de trabajo con credencial | Total sobre su biblioteca; Resolve guarda la contraseña en la estación |
| Otro equipo de la LAN | Ninguna; puede intentar conectarse a 5432 y 9432 |
| Atacante en Internet | Ninguna; no debería alcanzar los puertos (VPN) |
| Código de un hook | Mínima: no ve secretos ni PostgreSQL |
| Dependencia comprometida | Riesgo de cadena de suministro; se limita con pocas dependencias y aislamiento |

Fronteras: navegador y panel (HTTPS), estaciones y PostgreSQL (TCP 5432), sqsolve y PostgreSQL
(socket Unix), sqsolve y procesos externos (`execx`), sqsolve y servicios de aviso (salida
opcional), contenedores y host (volúmenes).

## 3. Amenazas y controles

| ID | Amenaza | Controles |
|---|---|---|
| T-01 | Fuerza bruta contra el panel | Argon2id; límite por IP y por cuenta (5 intentos, luego espera exponencial hasta 15 min); a las 10 contraseñas incorrectas seguidas la cuenta se bloquea y solo `sqsolve admin unlock` en la consola del contenedor la levanta (ADR 0021); TOTP opcional; eventos `auth.login_failed_burst` y `auth.locked` |
| T-02 | Fuerza bruta contra PostgreSQL | Contraseñas de 140 bits; SCRAM; reglas de red; `log_connections` y evento de fallos repetidos leyendo los registros de PostgreSQL (1.1) |
| T-03 | Robo de sesión | Cookies `Secure`, `HttpOnly`, `SameSite=Strict`; token de sesión de 256 bits guardado como hash; caducidad por inactividad (8 h) y absoluta (7 días); cierre de sesión en todos los dispositivos |
| T-04 | CSRF | `SameSite=Strict` y token de sincronización en cada formulario y en `hx-headers` |
| T-05 | XSS | `html/template` con escape contextual; CSP `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'; form-action 'self'`; htmx con `allowEval = false` e `includeIndicatorStyles = false` |
| T-06 | Inyección SQL | Parámetros siempre; DDL solo con identificadores validados y saneados (`07` §2); pruebas de fuzzing; `gosec` |
| T-07 | Inyección de comandos | Sin shell; `execx` con lista blanca (`pg_dump`, `pg_restore`, `vacuumdb`, `restic`); argumentos en arreglo; rutas validadas contra el directorio permitido |
| T-08 | Escalada de una inyección SQL a ejecución de comandos | Rol de control sin superusuario (sin `COPY ... PROGRAM`); el superusuario solo se usa en rutas de código listadas y auditadas |
| T-09 | Conexión administrativa desde la red | `pg_hba.conf` rechaza `postgres` y `sqsolve_admin` por red; sqsolve usa socket local |
| T-10 | Escucha en la LAN | TLS en PostgreSQL: cifrado oportunista por defecto (Resolve 21.1.1 negocia TLS 1.3 solo, comprobado el 2026-10-19) y modo "Solo TLS" opcional que pasa `pg_hba.conf` a `hostssl`, con advertencia de que no se sabe cómo lo implementa cada versión de Resolve; la vista de sesiones muestra si cada conexión va cifrada (`pg_stat_ssl`); panel solo HTTPS; recomendación de VPN fuera de la LAN |
| T-11 | Toma del asistente inicial | Código de instalación de un solo uso impreso en el registro del contenedor; el asistente se cierra al crear el primer administrador |
| T-12 | Operación destructiva por error o por sesión robada | Confirmación tecleada con el slug; respaldo previo automático; papelera; auditoría; aviso. Sin reautenticación desde ADR 0021 |
| T-13 | Lectura de respaldos | Volumen con permisos 0700 del UID de sqsolve; cifrado con age para copias externas; restic cifra siempre |
| T-14 | Fuga de secretos en registros | Tipo `secret.String` que se imprime como `[redactado]`; manejador de `slog` que redacta claves conocidas; prueba que busca secretos de prueba en toda la salida |
| T-15 | Hook malicioso o defectuoso | Sin hooks en 1.0; en 1.1, contenedor aparte, sin secretos, sin socket, con tiempo máximo y solo lectura sobre respaldos |
| T-16 | Webhook hacia la red interna (SSRF) | Solo URLs configuradas por administradores; aviso si apunta a direcciones privadas; sin redirecciones automáticas |
| T-17 | Dependencia comprometida | Lista cerrada de módulos; `govulncheck`; espera de 7 días para versiones nuevas; SBOM; compilación reproducible; imágenes fijadas por digest |
| T-18 | Escape del contenedor | Sin root, `read_only: true`, `cap_drop: [ALL]`, `no-new-privileges`, sin socket de Docker, `tmpfs` para `/tmp` |
| T-19 | Denegación de servicio por disco lleno | Comprobación de espacio antes de cada respaldo; alertas a 80 % y 90 %; retención; reserva recomendada en el volumen de datos |
| T-20 | Manipulación de la auditoría | Cadena de hashes; verificación diaria y al exportar; exportación periódica al volumen de respaldos |

## 4. Autenticación del panel

- Usuarios locales. Nombre de usuario, contraseña de 8 caracteres mínimo comprobada contra una
  lista local de contraseñas comunes (archivo embebido, sin consultas externas).
- Argon2id con `m=64 MiB, t=3, p=2` y sal de 16 bytes; parámetros guardados en el hash para
  poder subirlos después.
- TOTP RFC 6238 (SHA-1, 6 dígitos, 30 s, tolerancia de un paso), con 10 códigos de recuperación
  de un solo uso guardados como hash. Administradores: TOTP obligatorio si se activa la opción
  global.
- Tokens de API: prefijo `sqs_`, 32 bytes aleatorios, se muestran una sola vez, se guardan como
  SHA-256. Alcances: `read`, `backup`, `restore`, `export`, `admin`. Caducidad máxima 365 días.
- Recuperación: `docker compose exec sqsolve sqsolve admin reset-password <usuario>` o
  `sqsolve admin create --role admin`. Quien llega a la consola del contenedor ya controla el
  host.

## 5. Autorización

Roles del panel:

| Acción | Lector | Operador | Administrador | Colaborador (1.1) |
|---|---|---|---|---|
| Ver estado, bibliotecas, respaldos, tareas, eventos | Sí | Sí | Sí | Solo sus bibliotecas |
| Ver datos de conexión (sin contraseña) | Sí | Sí | Sí | Solo sus bibliotecas |
| Ver contraseña de una biblioteca | No | No | Sí (auditado) | Solo la suya (auditado) |
| Respaldar ahora, verificar | No | Sí | Sí | No |
| Restaurar como copia, descargar respaldo | No | Sí | Sí | No |
| Restaurar reemplazando, revertir | No | No | Sí | No |
| Exportar proyectos y grados (experimental, si está activo) | No | Sí | Sí | No |
| Crear, editar, mantenimiento, eliminar bibliotecas | No | No | Sí | No |
| Credenciales, reglas de red | No | No | Sí | No |
| Programaciones, retención, notificaciones | No | No | Sí | No |
| Usuarios, tokens de otros, ajustes del sistema | No | No | Sí | No |
| Auditoría | No | Sí (lectura) | Sí | No |

La matriz vive en `internal/auth/policy.go` como tabla y se prueba exhaustivamente: cada
combinación de rol, acción y recurso tiene un caso.

Autorización en Resolve: solo existe a nivel de conexión. Lo que se puede aplicar de verdad:

| Mecanismo | Aplicable | Nota |
|---|---|---|
| Qué credencial entra a qué biblioteca | Sí | Roles y `pg_hba.conf` |
| Desde qué redes | Sí | `pg_hba.conf` (si PoC-08 confirma que se ve la IP real) |
| Cuántas conexiones | Sí | `CONNECTION LIMIT` |
| Bloquear temporalmente | Sí | `ALLOW_CONNECTIONS` |
| Solo lectura | INVESTIGAR | PoC-09; Resolve probablemente necesita escribir aunque solo mire |
| Permisos por proyecto, bin o timeline | DESCARTADO | Resolve no lo aplica: cualquier rol que entra puede todo dentro de la biblioteca |
| Identidad del colaborador en el chat | DESCARTADO | La define cada estación; no es verificable |

La interfaz no ofrece nada de lo descartado. Para separar clientes o producciones se usan
bibliotecas distintas.

## 6. Secretos

| Secreto | Dónde | Quién lo lee | Rotación |
|---|---|---|---|
| Contraseña de `postgres` | `db/sqsolve/superuser_password` (0440, grupo de PostgreSQL) | PostgreSQL (al iniciar el clúster por `POSTGRES_PASSWORD_FILE`), sqsolve | `sqsolve admin rotate-superuser` |
| Contraseña de `sqsolve_admin` | `app/secrets/pg_admin_password` (0400) | sqsolve | Automática cada 90 días o bajo demanda |
| Clave maestra (32 bytes) | `app/secrets/master.key` (0400) | sqsolve | `sqsolve admin rotate-master-key` vuelve a cifrar todo en una transacción |
| Clave de firma de webhooks | Cifrada en SQLite, una por canal | sqsolve | Desde la interfaz |
| Contraseñas de bibliotecas | Cifradas en SQLite | sqsolve | Rotación por biblioteca |
| Contraseña de restic (1.1) | Cifrada en SQLite y en el kit de recuperación | sqsolve | No rotable sin rehacer el repositorio; se documenta |
| Certificados TLS | `db/sqsolve/tls` y `app/tls` | PostgreSQL, sqsolve | Renovación automática a 30 días de caducar (autofirmados de 2 años) |

Cifrado de secretos recuperables: XChaCha20-Poly1305 con nonce aleatorio de 24 bytes; el
identificador de la clave y el nombre de la columna van como datos adicionales autenticados para
que un valor no pueda moverse a otra columna.

Kit de recuperación: archivo `sqsolve-recuperacion-<instalación>.txt` que el asistente obliga a
descargar, con la clave maestra en base64, la huella del certificado, la versión y los pasos de
recuperación. Se puede volver a descargar desde Ajustes. Sin él, las bibliotecas siguen a
salvo pero hay que rotar sus contraseñas.

## 7. Red

- PostgreSQL solo en la LAN o la VPN. Nunca publicar 5432 en Internet (ADR 0015).
- Acceso remoto recomendado: WireGuard (por ejemplo, la app wg-easy que ofrecen muchos NAS) o
  Headscale con clientes de Tailscale. La documentación cubre ambos; sqsolve no incluye VPN.
- El panel puede publicarse detrás de un proxy inverso del usuario; sqsolve respeta
  `X-Forwarded-For` solo de las IP declaradas en `SQSOLVE_TRUSTED_PROXIES`.
- En hosts con varias interfaces, el Compose de referencia permite publicar 5432 solo en la IP de
  la LAN (`192.168.1.10:5432:5432`), como en Synology.

## 8. Endurecimiento del contenedor de sqsolve

```yaml
user: "${SQSOLVE_UID:-1000}:${SQSOLVE_GID:-1000}"
read_only: true
cap_drop: [ALL]
security_opt: ["no-new-privileges:true"]
tmpfs: ["/tmp:size=64m,mode=1777"]
```

`pg_dump` escribe directo en el volumen de respaldos, nunca en `/tmp`. El contenedor de
PostgreSQL usa la imagen oficial con su usuario por defecto; se le aplica `no-new-privileges` y
`cap_drop` con la lista mínima que necesita su entrada (`CHOWN`, `DAC_OVERRIDE`, `FOWNER`,
`SETGID`, `SETUID`), validada en SQS-031.

## 9. Auditoría

Se registra toda acción que cambia estado o expone secretos: inicios y fallos de sesión,
cambios de usuarios, creación y cambios de bibliotecas, ver contraseñas, rotaciones, reglas de
red, respaldos manuales, restauraciones, reversiones, eliminaciones, cambios de programación y
retención, cambios de ajustes, uso del superusuario, ejecución de comandos de la CLI.

Formato de cada entrada: `seq`, fecha, actor (tipo, id, IP), acción, objetivo, resultado,
detalles sin secretos, `prev_hash` y `hash = SHA-256(prev_hash || JSON canónico de la entrada)`.
La verificación recorre la cadena; una ruptura genera el evento crítico `audit.chain_broken`.

## 10. Respuesta a incidentes y divulgación

- `SECURITY.md` con correo de contacto (Por confirmar) y política de divulgación coordinada a 90
  días.
- Avisos de seguridad publicados como GitHub Security Advisories y en el registro de cambios.
- Guía "credencial filtrada": rotar la biblioteca afectada, revisar sesiones y auditoría,
  revisar reglas de red.
- Guía "servidor comprometido": aislar, conservar volúmenes, reinstalar en limpio, restaurar
  desde respaldos verificados anteriores al incidente, rotar todas las credenciales y la clave
  maestra.
