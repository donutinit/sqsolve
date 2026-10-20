# sqsolve visual identity: "Node"

Decided in [ADR 0020](../adr/0020-visual-identity.md). Chosen on 2026-10-19 among five
proposals. This folder holds the final files and the rules for using them.

<p align="center">
  <img src="logo.svg" alt="sqsolve" width="420">
</p>

## 1. Concept

The visual language of anyone who uses Resolve every day is the node: a box with an input and an
output that a signal passes through. sqsolve is that node between the workstations and the
projects: it takes connections, stores and protects, delivers. The curve inside is a lying "s",
the initial of the name, drawn as a continuous signal. The tone is a colorist's: warm and precise,
with two accents that recall the most used grading pair, teal and orange, on a warm black and a
smoke grey.

## 2. Files

| File | What it is | When to use it |
|---|---|---|
| `symbol.svg` | The symbol alone, in color on a light background | Square avatars, watermarks, wherever the name is already written next to it |
| `symbol-dark.svg` | The symbol on a dark background | Dark backgrounds or photographs |
| `symbol-mono.svg` | One ink (`currentColor`) | Stamps, engraving, embroidery, single-color print |
| `wordmark.svg`, `wordmark-dark.svg` | The word "sqsolve" as paths | When the symbol already appears elsewhere on the piece |
| `logo.svg`, `logo-dark.svg`, `logo-mono.svg` | Symbol and wordmark, separable | **The main form.** Headers, documents, README, slides |
| `emblem.svg`, `emblem-dark.svg` | The wordmark inside the node, with its ports | Pieces where the brand is the subject and there is room: covers, shirts, stickers |
| `app-icon.svg`, `app-icon-512.png`, `app-icon-192.png` | App icon on a rounded square | NAS app stores, shortcuts, Docker Hub and GHCR |
| `apple-touch-icon.png` | 180 px | Served by the panel at `/static/apple-touch-icon.png` |
| `favicon.svg` | Reduced version: node and ports, no signal | Served by the panel at `/static/favicon.svg`; any use under 24 px |
| `social-preview.svg`, `social-preview.png` | 1280 x 640 on warm black | Social image of the GitHub repository (uploaded by hand in Settings), social media |
| `icons/` | Twelve icons and the `icons.svg` sprite | Interface, documentation, diagrams |
| `tools/` | Generators | Regenerate everything above (see §9) |

The SVG files do not depend on any font: the wordmark is converted to paths.

## 3. Construction

The symbol lives on a 64-unit grid: a 40 x 28 node with radius 9 and stroke 4, the signal
`M12 32 C 20 18, 26 18, 32 32 S 44 46, 52 32` with stroke 4, ports of radius 5.5 at (12, 32) and
(52, 32). In the logo the wordmark is set at 46 units, 10 units away from the symbol, with its
x-height centered on the node's axis. In the emblem the wordmark is set at 40 units inside a node
64 units tall, radius 22, stroke 4, with ports of radius 10.

**Clear space:** around any version, at least the diameter of a port (11 units of 64, that is
17 % of the symbol's height). **Minimum size:** 24 px tall for the symbol with its signal; below
that use `favicon.svg`, where the signal disappears and node and ports keep a thicker stroke.

## 4. Palette

| Name | Hex | On dark | Use |
|---|---|---|---|
| Warm black | `#17191b` | | Text, dark background, node and wordmark on light |
| Smoke | `#ecebe7` | | Light background; node and wordmark on dark. Cards in `#f7f6f3` |
| Teal | `#178F84` | `#3FB8AC` | Input port and signal. For small text on light use `#11756C` |
| Orange | `#D9692A` | `#E8884D` | Output port and emphasis. For small text on light use `#A84C1B` |
| Ash | `#5d5f5b` | `#a9aba6` | Secondary text |

Measured contrast (WCAG 2.x) on smoke `#ecebe7`: warm black 14.8:1; ash 5.4:1; `#11756C` 4.7:1;
`#A84C1B` 4.7:1. Teal and orange at full intensity do not reach 4.5:1 for small text on light:
they are for shapes, not for small type.

**In the web panel** the greys stay strictly neutral (`docs/09-web-ui.md` §1) and the two accents
exist only as `--brand-in` and `--brand-out`, on the ports of the logo and of the navigation
icons. They are never used for states: OK is green, warning amber, error red, running blue.

## 5. Typography

| Use | Family | Weight | Notes |
|---|---|---|---|
| Brand and headlines of communication pieces | [Bricolage Grotesque](https://fonts.google.com/specimen/Bricolage+Grotesque) (OFL) | 700, optical size 96 | Lowercase, 3 % negative tracking for "sqsolve"; 600 for titles |
| Body text in documents and web | [Instrument Sans](https://fonts.google.com/specimen/Instrument+Sans) (OFL) | 400 and 500 | More neutral than the brand face |
| Identifiers, paths, figures | [JetBrains Mono](https://fonts.google.com/specimen/JetBrains+Mono) (OFL) | 400 and 500 | |
| Web panel | System fonts | | The panel works offline and loads no fonts; the wordmark is paths |

## 6. Iconography

Stroke 1.5 on a 24 grid (1.75 in the panel at 18 px), round caps and joins, no fills, and **one
colored port per icon**: teal for inputs and states, orange for outputs and alerts. The dot is the
signature of the system; an icon without a port is not sqsolve's.

| File | Stands for | Port |
|---|---|---|
| `dashboard` | Dashboard | input |
| `library` | Project library (cylinder) | input |
| `backup` | Backup (copy) | output |
| `restore` | Restore (return arrow) | input |
| `workstation` | Workstation running Resolve (monitor) | input |
| `job` | Job (node with play) | input and output |
| `alert` | Warning or error (triangle) | output |
| `access` | Credential or access (key) | input |
| `schedule` | Schedule (clock) | output |
| `activity` | Events and audit log (list) | input |
| `health` | System health (pulse) | output |
| `settings` | Settings (sliders) | input |

In the panel they sit in the sidebar, in the text color, through `.ni` and `.port` in `app.css`.

## 7. Applications

- **Web panel:** the logo in the sidebar (24 px tall) and on the pages without a session (40 px),
  always in the text color with the ports in the brand colors. Favicon and touch icon are served
  from `internal/web/static`.
- **GitHub:** the README shows `logo.svg` in the light theme and `logo-dark.svg` in the dark one.
  The social image is `social-preview.png`.
- **Image registries and app stores:** `app-icon-512.png`.
- **Documents and slides:** the logo on the cover; the symbol on inner pages.

## 8. What not to do

- Do not separate the ports from the node or move them: input always on the left, output on the
  right.
- Do not recolor the ports with state colors, and never use the orange as "error".
- Do not write "SQSolve", "SqSolve" or "SQSOLVE": always lowercase, also in prose.
- Do not slant, condense, outline, or add shadows or gradients.
- Do not combine it with Blackmagic Design marks or with the Resolve interface
  (`docs/18-documentation-and-license.md` §4).

## 9. Regenerating the files

The generators live in `tools/` and are optional: they are not part of the build. They need
Python 3, `fontTools`, `uharfbuzz` and the Bricolage Grotesque variable font, downloaded from the
Google Fonts repository (link in the header of `marks.py`):

```
python3 tools/marks.py Bricolage.ttf
python3 tools/icons.py Bricolage.ttf
magick -background none app-icon.svg -resize 512x512 app-icon-512.png
magick social-preview.svg social-preview.png
```

`marks.py` also writes `tools/lockup-panel.svg.txt` and `tools/nav-icons.txt`, the logo and the
seven navigation icons exactly as they are pasted into `internal/web/templates/layout.html`.

## 10. Licenses

The files in this folder follow the documentation license of the project (CC BY-SA 4.0,
[ADR 0001](../adr/0001-license.md)). Bricolage Grotesque is used under the SIL Open Font License
1.1: the SVG files contain letter outlines, not the font.
