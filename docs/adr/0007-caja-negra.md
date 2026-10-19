# ADR 0007. Las bibliotecas de Resolve son una caja negra

**Estado:** Aceptado (2026-10-18).

**Contexto.** El esquema de Resolve no está documentado, cambia entre versiones y es obra de
Blackmagic. Funciones como "listar proyectos de una biblioteca" o "liberar bloqueos" exigirían
leerlo o modificarlo.

**Decisión.** sqsolve no lee ni escribe tablas de Resolve. Solo usa: catálogo del sistema
(`pg_class`, `pg_attribute`, `pg_database`, `pg_stat_activity`), `count(*)` por tabla para
verificar respaldos, `pg_dump`/`pg_restore` y DDL de nivel de base y rol.

**Alternativas.** Lectura informativa del esquema (frágil, legalmente más gris); escritura para
"reparar" bloqueos (riesgo de corromper proyectos).

**Consecuencias.** sqsolve sigue funcionando aunque Resolve cambie su esquema. La interfaz no
muestra proyectos ni bins. Cualquier propuesta que rompa este principio necesita un ADR que lo
reemplace explícitamente.
