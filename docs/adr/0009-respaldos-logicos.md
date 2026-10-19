# ADR 0009. Respaldos lógicos por biblioteca

**Estado:** Aceptado, pendiente de PoC-04 (compatibilidad con la restauración de Resolve).

**Contexto.** La restauración útil para un estudio es por biblioteca, a veces solo para sacar un
proyecto. Los respaldos de Resolve parecen ser archivos de `pg_dump` en formato custom [01 §7].

**Decisión.** `pg_dump --format=custom --quote-all-identifiers` con instantánea exportada,
conteos por tabla en la misma instantánea, manifiesto JSON, SHA-256, verificación L1 siempre y L2
semanal. PITR con pgBackRest se agrega en 2.0 como capa adicional, no como sustituto.

**Alternativas.** Solo respaldos físicos del clúster (restauran todo o nada); herramientas de
respaldo en contenedor aparte (sin verificación ni catálogo).

**Consecuencias.** RPO limitado por la frecuencia programada hasta 2.0. El formato es estándar de
PostgreSQL y no depende de sqsolve para leerse.
