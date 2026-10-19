# Lista de verificación de la interfaz

Páginas del panel con `pagehead` (lo decide lo que renderiza la referencia: un encabezado
`pagehead` con título y acciones, y debajo secciones `card`). Referencia: `library.html`, la
cambiada más recientemente. 14 miembros de 29 candidatos en `internal/web` (plantillas,
`static/app.css`, `funcs.go`), servido por `cmd/sqsolve serve`. Leído el 2026-10-18 con la
fase de patrones de `ui-consistency`.

Miembros: account, activity, backup, backups, dashboard, health, import, job, jobs, libraries,
library, library_new, restore, settings. No miembros (15): las páginas de una sola tarjeta
`card narrow` sin `pagehead` (confirmaciones, acceso, kit, error, instrucciones); siguen las
mismas reglas de insignias, avisos, fechas y CSRF.

Es una foto: si esta lista y el código no coinciden, manda el código y se vuelve a contar.

- [ ] Contenedor: la página solo define `content`. `layout.html` pone la navegación, el aviso
      de una sola lectura (`{{template "flash" .}}`) y `<main id="contenido">`. 14 de 14.
- [ ] Encabezado: `<div class="pagehead">` con un solo `<h1>`; la acción principal va a la
      derecha, siempre dentro de `<div class="actions">`. `h1` en 14 de 14; acción en el
      encabezado en 6 de 14, 3 de ellas sueltas hasta el 2026-10-18 (ahora 6 de 6 en
      `div.actions`; sin mayoría antes, decidió la referencia, nivel 7).
- [ ] Secciones: `<section class="card">` con `<h2>` como título; nunca se salta del `h1` al
      `h3`. 61 secciones en 14 de 14 archivos; 52 de 61 con `h2`. Las 9 sin título: la tabla
      `flush` que es toda la página (3), la tarjeta principal bajo el `h1` (import, library_new y
      la de progreso en job), un formulario con `legend` (ajustes generales), un `details` con su `summary` y un
      aviso de una línea.
- [ ] Estado: siempre el parcial `badge` (`dict "tone" "label"`) o los que lo envuelven
      (`jobstate`, `librarystate`), que ponen texto y figura (círculo, rombo, cuadrado,
      contorno). Nunca `<span class="badge">` a mano. Pieza compartida (nivel 4): antes 15 usos
      en 8 de 14 contra 13 a mano en 9 archivos, 8 de ellos sin figura; corregido el
      2026-10-18. Excepción: los enlaces `a.badge` de la barra superior, escritos a mano porque
      el parcial no hace enlaces, con su `span.dot`.
- [ ] Aviso dentro de la página: `<div class="flash TONO" role="...">`, con `alert` solo para
      `error` y `status` para lo demás, la misma regla del parcial `flash` (nivel 4). 10 avisos
      en 6 archivos; los 2 que no tenían `role` se corrigieron.
- [ ] Vacío: una frase corta con `.muted` que nombra lo que falta ("Sin eventos."), en el
      elemento del contenedor: `p` en una sección o bajo el encabezado (11), `li` en una lista y
      `td colspan` en una tabla (5). Toda lista o tabla tiene su vacío (la auditoría no lo tenía; corregido). El
      parcial `empty`, con explicación y acción, es solo para el primer uso sin bibliotecas
      (2 usos).
- [ ] Formularios: `{{template "csrf" .CSRF}}` es el primer campo, obligatorio en los
      `multipart` porque el servidor lo lee antes que el archivo. 48 de 48 por el parcial.
      Botón principal `<button type="submit">` sin clase (21); secundario `ghost` (13);
      destructivo principal `danger`; destructivo secundario `ghost danger` (2) y, dentro de
      tablas, `ghost small danger`; `warn` para revertir y para mantenimiento (2). Toda acción destructiva pide
      confirmación: `data-confirm` o escribir el identificador (8 de 8; "Revocar" token no
      la pedía, corregido).
- [ ] Fechas: siempre `dt`, `ago` o `until`, que usan la zona del servidor; nunca `.Format`
      en una plantilla. 37 de 37 en las páginas, más 4 en los parciales. Un `<time>` lleva
      `datetime="{{iso ...}}"`.
- [ ] Palabras: un nombre para cada cosa en todo el panel, y las etiquetas de estado en
      español con mayúscula inicial (nunca el código interno como `ok` o `denied`):
      "Eventos" (no "Avisos", que es una severidad), "Almacenamiento", "Conectada" para la
      hora de una sesión, "Papelera" y "Purgada", "PostgreSQL sin respuesta", "Quitar" el
      segundo factor ("Desactivado" es solo una cuenta deshabilitada), "Correcto", "Aviso" y
      "Fallido" para resultados, "la dirección que usan las estaciones". Corregido el 2026-10-18
      tras la revisión.
- [ ] Valores: colores solo por las variables de `:root` en `app.css` (`--text`, `--muted`,
      `--ok`, `--warn`, `--bad`, `--run`, `--on-run`, `--on-danger`), que también definen el
      tema oscuro; tres colores literales pasaron a variables el 2026-10-18. Sin estilos en
      línea, que la CSP bloquea: 0 `style=`. El espaciado no tiene base (literales de 1 a
      56 px): reutiliza los ya escritos, sobre todo 8, 10, 12 y 16 px. Tipografía en `rem`:
      `h1` 1.5rem peso 650, `h2` 1.07rem peso 600, ayuda `.hint` 0.87rem en `--muted`.
- [ ] No copiado de `library.html`: sus siete pestañas, la línea de tiempo de 30 días y la
      cuadrícula de datos para copiar (`copy`), que solo tiene ella.

Búsquedas: grep por línea de los nombres exactos (`class="pagehead`, `<h1>`,
`<section class="card`, `<h2`, `class="tabs`, `{{template "badge"`, `class="badge`,
`class="flash`, `{{template "csrf"`, `name="csrf"`, `{{template "empty"`, `{{dt `, `{{ago `,
`{{until `, `style=`) sobre los 14 archivos miembros, cada una probada primero en
`library.html`. Quedó fuera, sin convención que contar: las pestañas, que solo tienen 5 de
14 páginas, cada una con las suyas.
