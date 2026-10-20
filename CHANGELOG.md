# Cambios

Formato: una sección por versión, la más nueva arriba. Las notas de cada versión en GitHub se
toman de aquí (`.github/workflows/release.yml`).

## [Sin publicar]

## [1.0.0] 2026-10-19

- Primera versión estable. Alcance y evidencia en `docs/aceptacion/1.0.md` y `docs/poc`.
- La estrategia "Base vacía" (E0) queda marcada como no compatible con Resolve 21.1: al
  conectar, Resolve la declara incompatible en vez de inicializarla (PoC-01).
- El README dice qué está probado y qué no.
- Una biblioteca en "Sin base en el servidor" (tras recuperar una instalación) ya se puede
  eliminar y purgar; antes solo se podía recrear (`SQS-L041`).

Actualizar: igual que la 0.1.x, con la etiqueta `1.0` en `x-sqsolve` (`docker compose pull` y
`up -d`, o Update en TrueNAS). Sin migraciones respecto a la 0.1.6. Volver atrás: imagen `0.1.6`.

## [0.1.6] 2026-10-19

- Firma de la imagen con cosign sin clave y publicación de cada versión en GitHub con el compose
  y estas notas.
- `SECURITY.md`, `CONTRIBUTING.md`, runbooks y `docs/manual/errores.md`.
- Cabeceras `Strict-Transport-Security` (con HTTPS) y `Cross-Origin-Resource-Policy` en el panel,
  tras la revisión con ZAP.
- Informes de PoC-03, 06, 08 y 09 en `docs/poc`; ADR 0019 aceptado como experimental.

Actualizar: igual que la 0.1.5. Sin migraciones.

## [0.1.5] 2026-10-19

- Seguridad proporcional a LAN y VPN (ADR 0021): sin reautenticación, contraseñas de 8
  caracteres, sesiones de 30 días, HTTPS obligatorio, bloqueo tras diez contraseñas incorrectas
  que se levanta con `sqsolve admin unlock`.
- Respaldo según actividad (ADR 0022): revisión cada 15 minutos y respaldo solo si la biblioteca
  cambió; un respaldo vigente no cuenta como atrasado. Modo por defecto en instalaciones nuevas.
- Exportar `.drp` y `.drx` desde cualquier respaldo, sin restaurar la biblioteca.
- Modo "Solo TLS" para las conexiones de Resolve, con la columna "Cifrado" en las sesiones.
- Migraciones 0002 a 0004 del catálogo; se aplican solas al arrancar.

Actualizar: con la etiqueta `0.1`, `docker compose pull` y `docker compose up -d`, o el botón
Update en TrueNAS. Volver atrás: la imagen `0.1.4` arranca con el catálogo migrado (las columnas
nuevas se ignoran).

## [0.1.4] 2026-10-19

- Identidad visual "Nodo" (ADR 0020): imagotipo e íconos en el panel, favicon, README nuevo.

## [0.1.3] 2026-10-19

- Arreglo: la pestaña Exportar fallaba con `SQS-X998` en bibliotecas con stills.

## [0.1.2] 2026-10-19

- Exportación experimental de proyectos (`.drp`) y grados (`.drx`) (ADR 0019), apagada por
  defecto.

## [0.1.1] 2026-10-19

- PostgreSQL corre como el usuario 999 y `sqsolve-init` le prepara la carpeta de datos; detección
  de volúmenes con ACL NFSv4 (`SQS-C054`).
- El compose de referencia usa la etiqueta `0.1`.

## [0.1.0] 2026-10-18

- Primera versión: bibliotecas, accesos, respaldos verificados, restauración, panel, API,
  notificaciones, kit de recuperación.
