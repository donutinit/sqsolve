# ADR 0020. Identidad visual "Nodo"

**Estado:** Aceptado (2026-10-19). Diego la eligió entre cinco propuestas (Pista, Cuadro, Nodo,
Archivo y Fotograma) presentadas ese día.

**Contexto.** Hasta la 0.1.3 la marca del panel era la línea de tiempo de protección en
miniatura (tres marcadores sobre un carril), sin logotipo ni paleta de marca. El proyecto se va
a publicar con imagen en GitHub y en GHCR, se instala como app en NAS y necesita una identidad
que un usuario de Resolve reconozca sin explicación y que no choque con las reglas de la
interfaz (`docs/09` §1): grises neutros, color solo para estados.

**Decisión.**

- El símbolo es un **nodo** como los de la página de color de Resolve: un rectángulo redondeado
  con un puerto de entrada (verde azulado), una señal en forma de s que lo atraviesa y un
  puerto de salida (naranja). sqsolve es ese nodo entre las estaciones y los proyectos.
- El logotipo es "sqsolve" en minúsculas en **Bricolage Grotesque 700** (tamaño óptico 96,
  interletrado del 3 % negativo), convertido a trazos. Los archivos no dependen de tener la
  fuente instalada y el panel no carga fuentes externas: sigue con la tipografía del sistema.
- Paleta de marca: negro cálido `#17191b`, humo `#ecebe7`, verde azulado `#178F84` (sobre
  oscuro `#3FB8AC`) y naranja `#D9692A` (sobre oscuro `#E8884D`). **En el panel los grises
  siguen siendo neutros y los dos colores de marca aparecen solo en los puertos del imagotipo y
  de los íconos de navegación**, como variables `--brand-in` y `--brand-out`. No sustituyen al
  verde de "correcto", al ámbar de aviso, al rojo de error ni al azul de "en curso".
- Iconografía: trazo en retícula de 24 y un puerto de color por ícono (verde azulado para
  entradas y estados, naranja para salidas y avisos). Doce íconos en `docs/identidad/iconos`;
  siete de ellos en la navegación del panel.
- Los archivos fuente viven en `docs/identidad` con su manual de uso. El favicon y el ícono
  táctil del panel se copian de ahí a `internal/web/static`.

**Consecuencias.**

- `docs/09` §1 cambia la frase sobre la marca; la dirección visual de la interfaz se mantiene.
- Los generadores de `docs/identidad/herramientas` (Python, fontTools, uharfbuzz) son una
  herramienta opcional para regenerar los archivos; no forman parte de la compilación ni de la
  imagen, así que no amplían el stack de `docs/06`.
- La fuente se usa bajo la SIL Open Font License 1.1; solo se distribuyen contornos dentro de
  los SVG, no la fuente.
- Queda pendiente, a mano en GitHub: subir `docs/identidad/vista-previa-social.png` como
  imagen social del repositorio.
