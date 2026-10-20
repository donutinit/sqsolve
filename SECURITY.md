# Seguridad

sqsolve está pensado para una red local de estudio o una VPN; el panel (9432) y PostgreSQL
(5432) nunca deben exponerse a Internet (ADR 0015). Con ese modelo, el panel pide usuario y
contraseña normales, sin reautenticación ni fricción extra (ADR 0021); lo que protege los
proyectos es la red, los respaldos verificados y la auditoría.

## Reportar una vulnerabilidad

Usa el reporte privado de GitHub: en el repositorio, pestaña **Security**, "Report a
vulnerability". No abras un issue público con los detalles.

Qué incluir: versión de sqsolve (pie del panel o `sqsolve version`), cómo reproducirlo y qué
impacto tiene. Si puedes, adjunta el paquete de diagnóstico de Ajustes, Mantenimiento, que no
contiene secretos.

Compromiso: respuesta en 7 días; arreglo en una versión de parche de la serie vigente y aviso en
las notas de la versión. Divulgación coordinada a 90 días, o antes si el arreglo ya está
publicado.

## Alcance

Dentro: el binario `sqsolve`, la imagen `ghcr.io/donutinit/sqsolve`, `deploy/compose.yaml`, el
panel, la API y la consola.

Fuera: DaVinci Resolve, PostgreSQL y Docker en sí (repórtalos a sus proyectos), y
configuraciones que exponen los puertos a Internet contra la documentación.

## Qué hace sqsolve por su cuenta

- Contraseñas con Argon2id; límite de intentos por IP y por cuenta; bloqueo tras diez
  contraseñas incorrectas seguidas que solo levanta `sqsolve admin unlock` desde la consola.
- TOTP opcional; tokens de API con alcances; CSRF; cabeceras estrictas (CSP sin recursos
  externos).
- Panel siempre por HTTPS. PostgreSQL ofrece TLS a Resolve; el modo "Solo TLS" lo exige.
- Contraseñas de biblioteca cifradas con la clave maestra; secretos nunca en registros.
- Auditoría encadenada por hashes de cada acción, exportable.
- Contenedor sin privilegios, sistema de archivos de solo lectura, sin capacidades de Linux.

El modelo de amenazas completo está en [`docs/08-seguridad.md`](docs/08-seguridad.md).
