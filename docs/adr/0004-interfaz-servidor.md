# ADR 0004. Interfaz renderizada en el servidor

**Estado:** Aceptado (2026-10-18).

**Contexto.** Se necesita un panel moderno con estados en vivo, pero el proyecto quiere evitar la
cadena de suministro de npm y una segunda aplicación que mantener.

**Decisión.** `html/template` con htmx vendorizado, SSE para progreso, CSS propio con tokens, un
archivo `app.js` pequeño. Los recursos se embeben en el binario. La interfaz llama a los
servicios directamente; la API REST es un adaptador hermano.

**Alternativas.** SPA con Svelte, React o Vue; `templ` (`06-stack.md` §2).

**Consecuencias.** Sin Node en la compilación. Interacciones muy ricas (por ejemplo, editores
visuales complejos) quedan fuera; ninguna está en el alcance.
