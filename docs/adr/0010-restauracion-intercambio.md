# ADR 0010. Restauración por intercambio

**Estado:** Aceptado, pendiente de PoC-09 (comportamiento de Resolve al reconectar).

**Contexto.** Restaurar sobre la base activa (como hace Studio Server con el aviso de que todos
salgan de Resolve) destruye el estado actual antes de saber si la restauración sale bien.

**Decisión.** Restaurar siempre en una base nueva; verificar contra el manifiesto; respaldo
previo; bloquear y desconectar la activa; renombrarla a `__prev`; renombrar la restaurada al
nombre original; reabrir. Cada paso en un diario para recuperar tras un corte. Conservar `__prev`
7 días con reversión de un clic. El modo por defecto en la interfaz es "como biblioteca nueva".

**Alternativas.** `pg_restore --clean` sobre la activa; restaurar con otro nombre y pedir a cada
estación que se reconecte (más pasos para los editores).

**Consecuencias.** Necesita espacio libre para dos copias de la biblioteca durante la operación.
Si PoC-09 muestra que Resolve conserva cachés que confunden tras el intercambio, se exige cerrar
Resolve en las estaciones y la interfaz lo indica como paso obligatorio.
