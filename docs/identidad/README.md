# Identidad visual de sqsolve: "Nodo"

Decisión en [ADR 0020](../adr/0020-identidad-visual.md). Elegida el 2026-10-19 entre cinco
propuestas. Esta carpeta tiene los archivos definitivos y las reglas para usarlos.

<p align="center">
  <img src="imagotipo.svg" alt="sqsolve" width="420">
</p>

## 1. Concepto

El lenguaje visual de quien usa Resolve a diario es el nodo: una caja con entrada y salida por la
que pasa la señal. sqsolve es ese nodo entre las estaciones y los proyectos: recibe conexiones,
guarda y protege, entrega. La curva interior es una "s" acostada, la inicial del nombre, dibujada
como una señal continua. El tono es de colorista, cálido y preciso: dos acentos que recuerdan al
par de corrección más usado, verde azulado y naranja, sobre un negro cálido y un gris humo.

## 2. Archivos

| Archivo | Qué es | Cuándo usarlo |
|---|---|---|
| `isotipo.svg` | El símbolo solo, en color sobre fondo claro | Avatares cuadrados, marcas de agua, cuando la palabra ya está escrita al lado |
| `isotipo-negativo.svg` | El símbolo sobre fondo oscuro | Fondos oscuros o fotografías |
| `isotipo-mono.svg` | Una sola tinta (`currentColor`) | Sellos, grabado, bordado, impresión a una tinta |
| `logotipo.svg`, `logotipo-negativo.svg` | La palabra "sqsolve" en trazos | Cuando el símbolo ya aparece en otro lugar de la pieza |
| `imagotipo.svg`, `imagotipo-negativo.svg`, `imagotipo-mono.svg` | Símbolo y palabra, separables | **La forma principal.** Cabeceras, documentos, README, presentaciones |
| `isologo.svg`, `isologo-negativo.svg` | La palabra dentro del nodo, con sus puertos | Piezas donde la marca es protagonista y hay espacio: portadas, camisetas, calcomanías |
| `icono-app.svg`, `icono-app-512.png`, `icono-app-192.png` | Ícono de aplicación en cuadrado redondeado | Tiendas de apps de NAS, accesos directos, Docker Hub y GHCR |
| `apple-touch-icon.png` | 180 px | Lo sirve el panel en `/static/apple-touch-icon.png` |
| `favicon.svg` | Versión reducida: nodo y puertos, sin señal | Lo sirve el panel en `/static/favicon.svg`; cualquier uso menor de 24 px |
| `vista-previa-social.svg`, `vista-previa-social.png` | 1280 x 640 sobre negro cálido | Imagen social del repositorio en GitHub (se sube a mano en Settings), redes |
| `iconos/` | Doce íconos y el sprite `iconos.svg` | Interfaz, documentación, diagramas |
| `herramientas/` | Generadores | Regenerar todo lo anterior (ver §9) |

Los SVG no dependen de ninguna fuente: el logotipo está convertido a trazos.

## 3. Construcción

El símbolo vive en una retícula de 64 unidades: nodo de 40 x 28 con radio 9 y trazo 4, señal
`M12 32 C 20 18, 26 18, 32 32 S 44 46, 52 32` con trazo 4, puertos de radio 5.5 en (12, 32) y
(52, 32). En el imagotipo la palabra va a 46 unidades de cuerpo, separada 10 unidades del
símbolo, con la altura de x centrada en el eje del nodo. En el isologo la palabra va a 40
unidades dentro de un nodo de 64 de alto, radio 22 y trazo 4, con puertos de radio 10.

**Espacio de respeto:** alrededor de cualquier versión, al menos el diámetro de un puerto (11
unidades de 64, es decir, un 17 % de la altura del símbolo). **Tamaño mínimo:** 24 px de alto
para el símbolo con señal; por debajo se usa `favicon.svg`, donde la señal desaparece y quedan
nodo y puertos con trazo más grueso.

## 4. Paleta

| Nombre | Hex | Sobre oscuro | Uso |
|---|---|---|---|
| Negro cálido | `#17191b` | | Texto, fondo oscuro, nodo y palabra sobre claro |
| Humo | `#ecebe7` | | Fondo claro; nodo y palabra sobre oscuro. Tarjetas en `#f7f6f3` |
| Verde azulado | `#178F84` | `#3FB8AC` | Puerto de entrada y señal. Para texto pequeño sobre claro usar `#11756C` |
| Naranja | `#D9692A` | `#E8884D` | Puerto de salida y énfasis. Para texto pequeño sobre claro usar `#A84C1B` |
| Ceniza | `#5d5f5b` | `#a9aba6` | Texto secundario |

Contraste medido (WCAG 2.x) sobre humo `#ecebe7`: negro cálido 14.8:1; ceniza 5.4:1;
`#11756C` 4.7:1; `#A84C1B` 4.7:1. El verde azulado y el naranja a plena intensidad no alcanzan
4.5:1 para texto pequeño sobre claro: son para formas, no para letra chica.

**En el panel** los grises siguen siendo estrictamente neutros (`docs/09` §1) y los dos acentos
existen solo como `--brand-in` y `--brand-out`, en los puertos del imagotipo y de los íconos de
navegación. No se usan para estados: correcto es verde, aviso ámbar, error rojo, en curso azul.

## 5. Tipografía

| Uso | Familia | Peso | Notas |
|---|---|---|---|
| Marca y titulares de piezas de comunicación | [Bricolage Grotesque](https://fonts.google.com/specimen/Bricolage+Grotesque) (OFL) | 700, tamaño óptico 96 | Minúsculas, interletrado del 3 % negativo para "sqsolve"; 600 para títulos |
| Texto en documentos y sitio | [Instrument Sans](https://fonts.google.com/specimen/Instrument+Sans) (OFL) | 400 y 500 | Más neutra que la de marca |
| Identificadores, rutas, cifras | [JetBrains Mono](https://fonts.google.com/specimen/JetBrains+Mono) (OFL) | 400 y 500 | |
| Panel web | Tipografía del sistema | | El panel funciona sin Internet y no carga fuentes; el logotipo va en trazos |

## 6. Iconografía

Trazo de 1.5 en retícula de 24 (1.75 en el panel a 18 px), puntas y uniones redondas, sin
relleno, y **un puerto de color en cada ícono**: verde azulado para entradas y estados, naranja
para salidas y avisos. El punto es la firma del sistema; un ícono sin puerto no es de sqsolve.

| Archivo | Representa | Puerto |
|---|---|---|
| `panel` | Panel principal | entrada |
| `biblioteca` | Biblioteca de proyectos (cilindro) | entrada |
| `respaldo` | Respaldo (copia) | salida |
| `restaurar` | Restaurar (flecha de retorno) | entrada |
| `estacion` | Estación con Resolve (monitor) | entrada |
| `tarea` | Tarea (nodo con play) | entrada y salida |
| `alerta` | Aviso o error (triángulo) | salida |
| `acceso` | Credencial o acceso (llave) | entrada |
| `horario` | Programación (reloj) | salida |
| `actividad` | Eventos y auditoría (lista) | entrada |
| `salud` | Salud del sistema (pulso) | salida |
| `ajustes` | Ajustes (deslizadores) | entrada |

En el panel van en la barra lateral, en el color del texto, con `.ni` y `.port` de `app.css`.

## 7. Aplicaciones

- **Panel:** imagotipo en la barra lateral (24 px de alto) y en las páginas sin sesión (40 px),
  siempre en el color del texto con los puertos en los colores de marca. Favicon e ícono táctil
  desde `internal/web/static`.
- **GitHub:** el README muestra `imagotipo.svg` en tema claro e `imagotipo-negativo.svg` en
  oscuro. La imagen social es `vista-previa-social.png`.
- **Registro de imágenes y tiendas de apps:** `icono-app-512.png`.
- **Documentos y presentaciones:** imagotipo en la portada; isotipo en páginas interiores.

## 8. Lo que no se hace

- No se separan los puertos del nodo ni se cambia su posición: siempre entrada a la izquierda,
  salida a la derecha.
- No se recolorean los puertos con colores de estado ni se usa el naranja como "error".
- No se escribe "SQSolve", "SqSolve" ni "SQSOLVE": siempre minúsculas, también en prosa.
- No se inclina, condensa, contornea ni se le ponen sombras o degradados.
- No se combina con las marcas de Blackmagic Design ni con la interfaz de Resolve (`docs/18`
  §4).

## 9. Regenerar los archivos

Los generadores están en `herramientas/` y son opcionales: no forman parte de la compilación.
Necesitan Python 3, `fontTools`, `uharfbuzz` y la fuente variable Bricolage Grotesque, que se
descarga del repositorio de Google Fonts (enlace en la cabecera de `marcas.py`):

```
python3 herramientas/marcas.py Bricolage.ttf
python3 herramientas/iconos.py Bricolage.ttf
magick -background none icono-app.svg -resize 512x512 icono-app-512.png
magick vista-previa-social.svg vista-previa-social.png
```

`marcas.py` deja además `herramientas/lockup-panel.svg.txt` y `herramientas/nav-icons.txt`, que
son el imagotipo y los siete íconos tal como van pegados en `internal/web/templates/layout.html`.

## 10. Licencias

Los archivos de esta carpeta siguen la licencia de la documentación del proyecto (CC BY-SA 4.0,
[ADR 0001](../adr/0001-licencia.md)). Bricolage Grotesque se usa bajo la SIL Open Font License
1.1: los SVG contienen contornos de letras, no la fuente.
