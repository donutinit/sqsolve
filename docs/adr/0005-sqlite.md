# ADR 0005. Estado del plano de control

**Estado:** Aceptado (2026-10-18).

**Contexto.** Usuarios del panel, catálogo de respaldos, tareas y auditoría deben seguir
disponibles cuando PostgreSQL está caído o restaurándose.

**Decisión.** SQLite (`modernc.org/sqlite`, sin CGO) en el volumen `state`, modo WAL, copia
diaria con `VACUUM INTO` al volumen de respaldos y catálogo reconstruible desde manifiestos.

**Alternativas.** Base `sqsolve` dentro del mismo PostgreSQL; archivos JSON (`06-stack.md` §3).

**Consecuencias.** Dos motores de base de datos, pero uno es una biblioteca embebida sin
servidor. Escrituras serializadas, suficiente para la carga esperada.
