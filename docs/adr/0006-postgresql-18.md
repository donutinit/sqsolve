# ADR 0006. Versión y configuración de PostgreSQL

**Estado:** Aceptado, pendiente de PoC-01 (colación) y PoC-02 (versiones de Resolve).

**Contexto.** Resolve 18 a 20 se usaron con PostgreSQL 13, ya sin soporte. Resolve 21.1 incluye
PostgreSQL 18 en Project Server y declara compatibilidad con bibliotecas en 13 o superior
[01 §4]. PostgreSQL 18 tiene soporte hasta 2030-11-14.

**Decisión.** Imagen oficial `postgres:18.x-trixie` fijada por digest. Clúster con
`--encoding=UTF8 --locale-provider=builtin --builtin-locale=C.UTF-8`, sumas de verificación
(por defecto en 18), SCRAM exclusivo.

**Alternativas.**
- PostgreSQL 13: compatible con todo lo anterior, pero sin parches de seguridad.
- PostgreSQL 17: válido; se reserva como perfil alternativo si PoC-02 lo exige para 20.3.x.
- Colación de la glibc (`en_US.UTF-8`): orden alfabético más "natural", pero una actualización de
  la imagen base puede invalidar índices de texto.

**Consecuencias.** Si PoC-01 muestra que Resolve fija colación al crear la base o que el orden
con `C.UTF-8` molesta a los editores, se evalúa ICU (`--locale-provider=icu`) mediante un ADR
nuevo, antes de 1.0, porque la colación no se puede cambiar después sin rehacer el clúster. ICU
da un orden más natural, pero su versión también viene con la imagen; habría que vigilar
`datcollversion` y reindexar cuando cambie.
