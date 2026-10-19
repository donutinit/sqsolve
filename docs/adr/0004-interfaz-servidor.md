# ADR 0004. Interfaz renderizada en el servidor

**Estado:** Aceptado (2026-10-18), actualizado al implementar: sin htmx.

**Contexto.** Se necesita un panel moderno con estados en vivo, pero el proyecto quiere evitar la
cadena de suministro de npm y una segunda aplicación que mantener.

**Decisión.** `html/template` con formularios estándar y el patrón POST, redirección y GET; un
aviso de una sola lectura en cookie para el resultado. El progreso de las tareas llega por
Server-Sent Events. El único JavaScript es `app.js`, propio y sin dependencias (copiar, confirmar
tecleando, progreso en vivo). Recursos embebidos en el binario. La API REST es un adaptador
hermano sobre los mismos servicios.

Al implementar se vio que htmx no aportaba nada que los formularios y unas decenas de líneas de
JavaScript no resolvieran, así que no se vendoriza.

**Alternativas.** SPA con Svelte, React o Vue; `templ`; htmx (`06-stack.md` §2).

**Consecuencias.** Sin Node en la compilación y sin JavaScript de terceros. La política de
seguridad de contenido prohíbe estilos y scripts en línea: barras y medidores usan `<meter>` y
`<progress>`, y la línea de tiempo de protección es SVG con atributos.
