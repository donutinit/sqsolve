# Herramienta opcional, fuera de la compilación: regenera los archivos de docs/identidad.
# Requiere Python 3, fontTools y uharfbuzz, y la fuente variable Bricolage Grotesque (OFL):
#   https://github.com/google/fonts/raw/main/ofl/bricolagegrotesque/BricolageGrotesque%5Bopsz%2Cwdth%2Cwght%5D.ttf
# Uso: python3 marcas.py <ruta-a-Bricolage.ttf>   (iconos.py igual; corre después de marcas.py)
import os, sys, tempfile
FONT = sys.argv[1] if len(sys.argv) > 1 else 'Bricolage.ttf'
TMP = tempfile.mkdtemp()
import json, os
from fontTools.ttLib import TTFont
from fontTools.varLib import instancer
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
import uharfbuzz as hb

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), '..')
os.makedirs(OUT, exist_ok=True)
src = TTFont(FONT)
inst = instancer.instantiateVariableFont(src, {"wght": 700, "opsz": 96, "wdth": 100})
inst.save(os.path.join(TMP, 'Bricolage-700.ttf'))
face = hb.Face(hb.Blob.from_file_path(os.path.join(TMP, 'Bricolage-700.ttf'))); font = hb.Font(face)
UPEM = face.upem; XH = inst['OS/2'].sxHeight
order = inst.getGlyphOrder(); gs = inst.getGlyphSet()

def word_path(text, size, x0, baseline, tracking=-0.03):
    """Path d of `text` at font-size `size` (SVG units), origin x0/baseline. Returns (d, width)."""
    buf = hb.Buffer(); buf.add_str(text); buf.guess_segment_properties()
    hb.shape(font, buf, {"kern": True, "liga": True})
    s = size / UPEM; pen_all = SVGPathPen(gs, ntos=lambda v: f"{v:.2f}")
    x = x0
    for info, pos in zip(buf.glyph_infos, buf.glyph_positions):
        g = gs[order[info.codepoint]]
        tp = TransformPen(pen_all, (s, 0, 0, -s, x + pos.x_offset * s, baseline - pos.y_offset * s))
        g.draw(tp)
        x += pos.x_advance * s + tracking * size
    return pen_all.getCommands(), x - x0 - tracking * size

INK, HUMO, TEAL, TEAL_D, NAR, NAR_D = "#17191b", "#ecebe7", "#178F84", "#3FB8AC", "#D9692A", "#E8884D"

def mark(ox, oy, ink, teal, nar, cls=False):
    # isotipo en retícula de 64, desplazado (ox, oy)
    g = f'<g transform="translate({ox} {oy})">' if (ox or oy) else '<g>'
    a = (lambda c, k: f'class="{k}"') if cls else (lambda c, k: f'{"stroke" if k in ("node","wave") else "fill"}="{c}"')
    return (g +
      f'<rect x="12" y="18" width="40" height="28" rx="9" fill="none" {a(ink,"node")} stroke-width="4"/>'
      f'<path d="M12 32 C 20 18, 26 18, 32 32 S 44 46, 52 32" fill="none" {a(teal,"wave")} stroke-width="4" stroke-linecap="round"/>'
      f'<circle cx="12" cy="32" r="5.5" {a(teal,"in")}/><circle cx="52" cy="32" r="5.5" {a(nar,"out")}/></g>')

def svg(w, h, body, vb=None, title="sqsolve"):
    vb = vb or f"0 0 {w} {h}"
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{vb}" width="{w}" height="{h}" role="img" aria-label="{title}">'
            f'{body}</svg>\n')

def write(name, text):
    with open(os.path.join(OUT, name), 'w') as f: f.write(text)

# Isotipo
write('isotipo.svg', svg(64, 64, mark(0, 0, INK, TEAL, NAR), title="sqsolve, isotipo"))
write('isotipo-negativo.svg', svg(64, 64, mark(0, 0, HUMO, TEAL_D, NAR_D), title="sqsolve, isotipo en negativo"))
write('isotipo-mono.svg', svg(64, 64, mark(0, 0, "currentColor", "currentColor", "currentColor"), title="sqsolve, isotipo monocromo"))

# Logotipo: palabra a 46 unidades, con la x-height centrada en y=32 (como el isotipo)
SIZE = 46; base = 32 + XH * SIZE / UPEM / 2
d, wadv = word_path("sqsolve", SIZE, 0, base)
W = round(wadv + 2, 1)
write('logotipo.svg', svg(W, 64, f'<path fill="{INK}" d="{d}"/>', title="sqsolve, logotipo"))
write('logotipo-negativo.svg', svg(W, 64, f'<path fill="{HUMO}" d="{d}"/>', title="sqsolve, logotipo en negativo"))

# Imagotipo: isotipo + palabra, separación 10
dw, _ = word_path("sqsolve", SIZE, 74, base); WI = round(74 + wadv + 2, 1)
for name, ink, teal, nar in (("imagotipo.svg", INK, TEAL, NAR), ("imagotipo-negativo.svg", HUMO, TEAL_D, NAR_D), ("imagotipo-mono.svg", "currentColor",)*1 + ("currentColor", "currentColor")):
    write(name, svg(WI, 64, mark(0, 0, ink, teal, nar) + f'<path fill="{ink}" d="{dw}"/>', title="sqsolve"))

# Isologo: la palabra dentro del nodo, con sus dos puertos
S2 = 40; base2 = 32 + XH * S2 / UPEM / 2
d2, w2 = word_path("sqsolve", S2, 32, base2); WN = round(32 + w2 + 32, 1)
for name, ink, teal, nar in (("isologo.svg", INK, TEAL, NAR), ("isologo-negativo.svg", HUMO, TEAL_D, NAR_D)):
    body = (f'<rect x="2" y="2" width="{WN-4}" height="60" rx="22" fill="none" stroke="{ink}" stroke-width="4"/>'
            f'<path fill="{ink}" d="{d2}"/><circle cx="2" cy="32" r="10" fill="{teal}"/><circle cx="{WN-2}" cy="32" r="10" fill="{nar}"/>')
    write(name, svg(WN + 20, 64, body, vb=f"-10 0 {WN+20} 64", title="sqsolve, isologo"))

# Ícono de app (512) y favicon (32, versión reducida sin señal)
write('icono-app.svg', svg(512, 512, f'<rect width="512" height="512" rx="112" fill="{INK}"/><g transform="translate(32 32) scale(7)">{mark(0,0,HUMO,TEAL_D,NAR_D)[3:-4]}</g>', title="sqsolve, ícono de app"))
fav = (f'<rect width="32" height="32" rx="7" fill="{INK}"/><rect x="5" y="8" width="22" height="16" rx="5" fill="none" stroke="{HUMO}" stroke-width="3.5"/>'
       f'<circle cx="5" cy="16" r="4.5" fill="{TEAL_D}"/><circle cx="27" cy="16" r="4.5" fill="{NAR_D}"/>')
write('favicon.svg', svg(32, 32, fav, title="sqsolve"))

# Lockup para el panel: clases en vez de colores (app.css los pinta)
panel = mark(0, 0, "", "", "", cls=True) + f'<path class="word" d="{dw}"/>'
with open(os.path.join(OUT, 'herramientas', 'lockup-panel.svg.txt'), 'w') as f:
    f.write(f'<svg viewBox="0 0 {WI} 64" aria-hidden="true" focusable="false">{panel}</svg>')
json.dump({"word_width": WI, "isologo_width": WN, "xheight": XH, "upem": UPEM}, open(os.path.join(OUT, 'herramientas', 'marcas.json'), 'w'))
print("ok", WI, WN, len(dw))
