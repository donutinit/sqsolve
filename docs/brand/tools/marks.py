# Optional tool, outside the build: regenerates the files in docs/brand.
# Needs Python 3, fontTools, uharfbuzz and the Bricolage Grotesque variable font (OFL):
#   https://github.com/google/fonts/raw/main/ofl/bricolagegrotesque/BricolageGrotesque%5Bopsz%2Cwdth%2Cwght%5D.ttf
# Usage: python3 marks.py <path-to-Bricolage.ttf>   (icons.py takes the same argument; run it after marks.py)
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
    # symbol on a 64 grid, offset by (ox, oy)
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

# Symbol
write('symbol.svg', svg(64, 64, mark(0, 0, INK, TEAL, NAR), title="sqsolve symbol"))
write('symbol-dark.svg', svg(64, 64, mark(0, 0, HUMO, TEAL_D, NAR_D), title="sqsolve symbol on dark"))
write('symbol-mono.svg', svg(64, 64, mark(0, 0, "currentColor", "currentColor", "currentColor"), title="sqsolve symbol, one color"))

# Wordmark: 46 units, x-height centered on y=32 like the symbol
SIZE = 46; base = 32 + XH * SIZE / UPEM / 2
d, wadv = word_path("sqsolve", SIZE, 0, base)
W = round(wadv + 2, 1)
write('wordmark.svg', svg(W, 64, f'<path fill="{INK}" d="{d}"/>', title="sqsolve wordmark"))
write('wordmark-dark.svg', svg(W, 64, f'<path fill="{HUMO}" d="{d}"/>', title="sqsolve wordmark on dark"))

# Logo: symbol plus wordmark, 10 units apart
dw, _ = word_path("sqsolve", SIZE, 74, base); WI = round(74 + wadv + 2, 1)
for name, ink, teal, nar in (("logo.svg", INK, TEAL, NAR), ("logo-dark.svg", HUMO, TEAL_D, NAR_D), ("logo-mono.svg", "currentColor", "currentColor", "currentColor")):
    write(name, svg(WI, 64, mark(0, 0, ink, teal, nar) + f'<path fill="{ink}" d="{dw}"/>', title="sqsolve"))

# Emblem: the wordmark inside the node, with its two ports
S2 = 40; base2 = 32 + XH * S2 / UPEM / 2
d2, w2 = word_path("sqsolve", S2, 32, base2); WN = round(32 + w2 + 32, 1)
for name, ink, teal, nar in (("emblem.svg", INK, TEAL, NAR), ("emblem-dark.svg", HUMO, TEAL_D, NAR_D)):
    body = (f'<rect x="2" y="2" width="{WN-4}" height="60" rx="22" fill="none" stroke="{ink}" stroke-width="4"/>'
            f'<path fill="{ink}" d="{d2}"/><circle cx="2" cy="32" r="10" fill="{teal}"/><circle cx="{WN-2}" cy="32" r="10" fill="{nar}"/>')
    write(name, svg(WN + 20, 64, body, vb=f"-10 0 {WN+20} 64", title="sqsolve emblem"))

# App icon (512) and favicon (32, reduced version without the signal)
write('app-icon.svg', svg(512, 512, f'<rect width="512" height="512" rx="112" fill="{INK}"/><g transform="translate(32 32) scale(7)">{mark(0,0,HUMO,TEAL_D,NAR_D)[3:-4]}</g>', title="sqsolve app icon"))
fav = (f'<rect width="32" height="32" rx="7" fill="{INK}"/><rect x="5" y="8" width="22" height="16" rx="5" fill="none" stroke="{HUMO}" stroke-width="3.5"/>'
       f'<circle cx="5" cy="16" r="4.5" fill="{TEAL_D}"/><circle cx="27" cy="16" r="4.5" fill="{NAR_D}"/>')
write('favicon.svg', svg(32, 32, fav, title="sqsolve"))

# Lockup for the web panel: classes instead of colors (app.css paints them)
panel = mark(0, 0, "", "", "", cls=True) + f'<path class="word" d="{dw}"/>'
with open(os.path.join(OUT, 'tools', 'lockup-panel.svg.txt'), 'w') as f:
    f.write(f'<svg viewBox="0 0 {WI} 64" aria-hidden="true" focusable="false">{panel}</svg>')
json.dump({"word_width": WI, "emblem_width": WN, "xheight": XH, "upem": UPEM}, open(os.path.join(OUT, 'tools', 'marks.json'), 'w'))
print("ok", WI, WN, len(dw))
