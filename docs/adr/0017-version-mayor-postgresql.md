# ADR 0017. Versiones mayores de PostgreSQL

**Estado:** Aceptado (2026-10-18). Implementación en 2.0.

**Contexto.** PostgreSQL 18 tiene soporte hasta 2030; en algún momento habrá que pasar a otra
versión mayor. `pg_upgrade` necesita binarios de ambas versiones y, si falla a medias, deja el
directorio de datos en un estado delicado.

**Decisión.** Migración lógica: clúster nuevo en un volumen nuevo, cada biblioteca respaldada y
restaurada con verificación L2, cambio de servicio en el YAML, y el volumen anterior intacto para
volver atrás hasta que el administrador lo borre.

**Alternativas.** `pg_upgrade --link` (rápido, pero sin vuelta atrás simple); imagen de terceros
para `pg_upgrade` (otra dependencia).

**Consecuencias.** Requiere espacio para dos clústeres durante la migración y una ventana con las
bibliotecas en mantenimiento. Las bibliotecas de Resolve son de tamaño moderado, así que el tiempo
es aceptable; se mide antes de implementar.
