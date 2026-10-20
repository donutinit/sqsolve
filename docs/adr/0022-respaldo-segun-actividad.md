# ADR 0022. Respaldo según actividad

**Estado:** Aceptado (2026-10-19). Pedido por Diego.

**Contexto.** Una programación por cron respalda a horas fijas: de noche cuando nadie trabajó y
nada cambió, y una sola vez al día cuando un colorista pasó ocho horas tocando el proyecto. Diego
quiere lo contrario: cada 10 o 15 minutos mientras haya cambios en la biblioteca, y nada cuando
está plana. "Diferencial" en sus palabras: si no hay diferencia desde el último respaldo, no
sale otro.

**Decisión.**

- La política de respaldo tiene un **modo** (`mode`): `cron`, el de siempre, o `activity`.
  En modo `activity` el programador despierta cada `check_minutes` (5, 10, 15, 30 o 60; 15 por
  defecto) y, antes de encolar la tarea, una **compuerta** compara la **marca de actividad** de
  la biblioteca con la del último respaldo correcto. Si coinciden, no hay tarea ni respaldo; si
  no, se respalda.
- La marca de actividad es `tup_inserted + tup_updated + tup_deleted` de `pg_stat_database`
  para esa base, más `stats_reset`, para que un reinicio de estadísticas nunca se lea como "sin
  cambios". Es una consulta de catálogo del servidor, no de tablas de Resolve (ADR 0007). Cada
  respaldo guarda la marca de su instantánea (`backups.activity_mark` y el manifiesto).
- Cada revisión queda en la política (`last_check_at`, `last_check_same`) y se muestra en la
  pestaña Programación. No genera eventos ni tareas: 96 revisiones diarias por biblioteca no
  son noticia.
- **Un respaldo vigente no es un respaldo atrasado.** La línea de protección y el aviso de
  "sin respaldo verificado en N horas" comparan la marca actual con la del último respaldo
  verificado: si no cambió nada, no hay franja ámbar ni aviso, y la insignia dice "hace 3 días,
  sin cambios". Solo se evalúa el tramo final (del último verificado a ahora); los huecos
  entre respaldos antiguos se siguen dibujando por tiempo.
- Las instalaciones nuevas arrancan en modo `activity` cada 15 minutos; las existentes
  conservan su cron hasta que alguien cambie la política.
- Cada respaldo sigue siendo un `pg_dump` completo. Un respaldo incremental de verdad es PITR
  (ADR en 2.0); aquí "diferencial" significa no repetir lo que ya está respaldado.

**Consecuencias.** Migración 0003; `docs/07` §7 documenta la consulta; `docs/11` §4 y §10, `docs/09`
y `docs/10` cambian en el mismo commit. PoC-10 decide si Resolve escribe mientras un proyecto
está abierto sin que nadie edite; si lo hace, el modo según actividad respalda cada revisión
mientras haya un proyecto abierto, que sigue siendo el comportamiento pedido.
