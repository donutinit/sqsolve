# ADR 0020. "Node" visual identity

**Status:** Accepted (2026-10-19). Diego chose it among five proposals (Track, Frame, Node,
File and Still) presented that day.

**Context.** Until 0.1.3 the panel's mark was the protection timeline in miniature (three
markers on a lane), with no logo and no brand palette. The project is going to be published with
an image on GitHub and GHCR, it installs as an app on a NAS, and it needs an identity that a
Resolve user recognizes without explanation and that does not clash with the UI rules
(`docs/09` §1): neutral greys, color only for states.

**Decision.**

- The symbol is a **node** like the ones on Resolve's color page: a rounded rectangle with an
  input port (teal), an s-shaped signal crossing it and an output port (orange). sqsolve is
  that node between the workstations and the projects.
- The wordmark is "sqsolve" in lowercase in **Bricolage Grotesque 700** (optical size 96,
  tracking of 3 % negative), converted to outlines. The files do not depend on having the font
  installed and the panel loads no external fonts: it keeps the system typeface.
- Brand palette: warm black `#17191b`, smoke `#ecebe7`, teal `#178F84` (on dark `#3FB8AC`) and
  orange `#D9692A` (on dark `#E8884D`). **In the panel the greys stay neutral and the two brand
  colors appear only in the ports of the logo and of the navigation icons**, as the variables
  `--brand-in` and `--brand-out`. They do not replace the green for "OK", the amber for
  warnings, the red for errors or the blue for "running".
- Iconography: strokes on a 24 grid and one colored port per icon (teal for inputs and states,
  orange for outputs and warnings). Twelve icons in `docs/brand/icons`; seven of them in the
  panel's navigation.
- The source files live in `docs/brand` with their usage guide. The favicon and the panel's
  touch icon are copied from there to `internal/web/static`.

**Consequences.**

- `docs/09` §1 changes the sentence about the mark; the visual direction of the UI stays.
- The generators in `docs/brand/tools` (Python, fontTools, uharfbuzz) are an optional tool to
  regenerate the files; they are not part of the build or of the image, so they do not extend
  the stack of `docs/06`.
- The font is used under the SIL Open Font License 1.1; only outlines inside the SVGs are
  distributed, not the font.
- Pending, by hand on GitHub: upload `docs/brand/social-preview.png` as the repository's social
  image.
