# Optional tool, outside the build: regenerates the files in docs/brand.
# Needs Python 3, fontTools, uharfbuzz and the Bricolage Grotesque variable font (OFL):
#   https://github.com/google/fonts/raw/main/ofl/bricolagegrotesque/BricolageGrotesque%5Bopsz%2Cwdth%2Cwght%5D.ttf
# Usage: python3 icons.py <path-to-Bricolage.ttf>   (run marks.py first)
import os, sys, tempfile
FONT = sys.argv[1] if len(sys.argv) > 1 else 'Bricolage.ttf'
TMP = tempfile.mkdtemp()
import os, re
from fontTools.ttLib import TTFont
from fontTools.varLib import instancer
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
import uharfbuzz as hb

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), '..'); IC = os.path.join(OUT, 'icons'); os.makedirs(IC, exist_ok=True)
INK, HUMO, TEAL, TEAL_D, NAR, NAR_D = "#17191b", "#ecebe7", "#178F84", "#3FB8AC", "#D9692A", "#E8884D"
P = lambda x, y, out=False, r=1.4: f'<circle cx="{x}" cy="{y}" r="{r}" class="port{" out" if out else ""}" stroke="none" fill="{NAR if out else TEAL}"/>'
ICONS = {
 "library": '<ellipse cx="12" cy="6" rx="7" ry="2.6"/><path d="M5 6v12c0 1.4 3.1 2.6 7 2.6s7-1.2 7-2.6V6"/><path d="M5 12c0 1.4 3.1 2.6 7 2.6s7-1.2 7-2.6"/>' + P(12, 18.5),
 "backup": '<rect x="8" y="8" width="12" height="12" rx="3"/><path d="M16 8V5.5A1.5 1.5 0 0 0 14.5 4h-9A1.5 1.5 0 0 0 4 5.5v9A1.5 1.5 0 0 0 5.5 16H8"/>' + P(20, 14, True),
 "restore": '<path d="M3.5 12a8.5 8.5 0 1 0 2.5-6"/><path d="M3.5 3.5V8H8"/>' + P(12, 12),
 "workstation": '<rect x="3" y="4" width="18" height="12" rx="3"/><path d="M8 20h8M12 16v4"/>' + P(3, 10),
 "job": '<rect x="3" y="6" width="18" height="12" rx="4"/><path d="M10 9l5 3-5 3z"/>' + P(3, 12) + P(21, 12, True),
 "alert": '<path d="M12 3.5L2.5 20h19L12 3.5z"/><path d="M12 10v4"/>' + P(12, 17, True),
 "access": '<circle cx="8" cy="16" r="4"/><path d="M11 13l9-9M17 7l2.5 2.5M14 10l2.5 2.5"/>' + P(8, 16),
 "schedule": '<circle cx="12" cy="12" r="8.5"/><path d="M12 7v5l3.5 2"/>' + P(12, 12, True),
 "dashboard": '<rect x="3.5" y="3.5" width="7" height="7" rx="2"/><rect x="13.5" y="3.5" width="7" height="7" rx="2"/><rect x="3.5" y="13.5" width="7" height="7" rx="2"/><rect x="13.5" y="13.5" width="7" height="7" rx="2"/>' + P(7, 7),
 "activity": '<path d="M9 6h11M9 12h11M9 18h11"/><circle cx="5" cy="12" r="1.6"/><circle cx="5" cy="18" r="1.6"/>' + P(5, 6, r=1.6),
 "health": '<path d="M3 12h4l2.5-6 4 12 2.5-6H21"/>' + P(21, 12, True),
 "settings": '<path d="M4 6h16M4 12h16M4 18h16"/><circle cx="9" cy="6" r="2.2"/><circle cx="7" cy="18" r="2.2"/>' + P(15, 12, r=2.2),
}
ROOT = 'fill="none" stroke="{c}" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"'
sprite = []
for name, body in ICONS.items():
    with open(os.path.join(IC, f'{name}.svg'), 'w') as f:
        f.write(f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24" {ROOT.format(c=INK)} role="img" aria-label="{name}">{body}</svg>\n')
    sprite.append(f'<symbol id="{name}" viewBox="0 0 24 24">{body}</symbol>')
with open(os.path.join(IC, 'icons.svg'), 'w') as f:
    f.write(f'<svg xmlns="http://www.w3.org/2000/svg" {ROOT.format(c=INK)}>{"".join(sprite)}</svg>\n')
# Panel version: no literal colors (app.css paints .ni and .port)
nav = {n: re.sub(r' fill="#[0-9A-Fa-f]{6}"', '', ICONS[n]) for n in ("dashboard", "library", "backup", "job", "activity", "health", "settings")}
with open(os.path.join(OUT, 'tools', 'nav-icons.txt'), 'w') as f:
    for n, b in nav.items():
        f.write(f'{n}\t<svg class="ni" viewBox="0 0 24 24" aria-hidden="true" focusable="false">{b}</svg>\n')

# Social preview 1280 x 640
src = TTFont(FONT)
def inst(w, o):
    t = instancer.instantiateVariableFont(TTFont(FONT), {"wght": w, "opsz": o, "wdth": 100}); p = os.path.join(TMP, f'B-{w}.ttf'); t.save(p); return t, p
def shape(ttf, path, text, size, x0, base, tracking):
    face = hb.Face(hb.Blob.from_file_path(path)); font = hb.Font(face); order = ttf.getGlyphOrder(); gs = ttf.getGlyphSet()
    buf = hb.Buffer(); buf.add_str(text); buf.guess_segment_properties(); hb.shape(font, buf, {"kern": True, "liga": True})
    pen = SVGPathPen(gs, ntos=lambda v: f"{v:.1f}"); s = size / face.upem; x = x0
    for i, p in zip(buf.glyph_infos, buf.glyph_positions):
        gs[order[i.codepoint]].draw(TransformPen(pen, (s, 0, 0, -s, x + p.x_offset * s, base - p.y_offset * s))); x += p.x_advance * s + tracking * size
    return pen.getCommands(), x - x0
t5, p5 = inst(500, 36)
lock = open(os.path.join(OUT, 'tools', 'lockup-panel.svg.txt')).read()
inner = lock[lock.index('>') + 1: lock.rindex('</svg>')]
inner = inner.replace('class="node"', f'stroke="{HUMO}"').replace('class="wave"', f'stroke="{TEAL_D}"').replace('class="in"', f'fill="{TEAL_D}"').replace('class="out"', f'fill="{NAR_D}"').replace('class="word"', f'fill="{HUMO}"')
tag1, w1 = shape(t5, p5, "Free, self-hosted project library server for DaVinci Resolve", 44, 0, 0, -0.01)
tag2, w2 = shape(t5, p5, "Verified backups, web panel, works offline, AGPL", 30, 0, 0, -0.005)
sc = 4.2; lw = 224.1 * sc; lx = (1280 - lw) / 2
body = (f'<rect width="1280" height="640" fill="{INK}"/><g transform="translate({lx:.1f} 110) scale({sc})">{inner}</g>'
        f'<path fill="{HUMO}" transform="translate({(1280-w1)/2:.1f} 470)" d="{tag1}"/><path fill="#a9aba6" transform="translate({(1280-w2)/2:.1f} 530)" d="{tag2}"/>')
with open(os.path.join(OUT, 'social-preview.svg'), 'w') as f:
    f.write(f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1280 640" width="1280" height="640" role="img" aria-label="sqsolve: free, self-hosted project library server for DaVinci Resolve">{body}</svg>\n')
print("ok", len(ICONS))
