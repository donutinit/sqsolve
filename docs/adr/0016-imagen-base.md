# ADR 0016. Imagen base de sqsolve

**Estado:** Aceptado (2026-10-18).

**Contexto.** `pg_dump` debe ser de la misma versión mayor que el servidor o más nueva; el socket
compartido necesita los mismos permisos que crea la imagen de PostgreSQL.

**Decisión.** La imagen de sqsolve parte de la misma `postgres:18.x-trixie@digest` que el
servidor y agrega el binario `sqsolve`. Se usan sus `pg_dump`, `pg_restore` y `vacuumdb`.

**Alternativas.** Imagen mínima con clientes de PostgreSQL instalados por paquetes (otra fuente
de versiones que sincronizar); ejecutar `pg_dump` dentro del contenedor de PostgreSQL (exigiría el
socket de Docker).

**Consecuencias.** Imagen más grande que una mínima, pero comparte capas con el servidor, así que
en disco casi no suma. Cada versión de sqsolve fija ambos digest juntos.
