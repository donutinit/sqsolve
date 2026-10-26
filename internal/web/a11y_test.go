// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

var (
	cardTableRe = regexp.MustCompile(`(?s)<table[^>]*cards-on-mobile.*?</table>`)
	tableTagRe  = regexp.MustCompile(`<(table|thead|tbody|tr|th|td)(\s[^>]*)?>`)
	tableRoles  = map[string]string{"table": "table", "thead": "rowgroup", "tbody": "rowgroup", "tr": "row", "th": "columnheader", "td": "cell"}
)

// Audit AUD-009: on narrow screens the cards-on-mobile tables hid their
// header with display:none and changed the display of rows and cells, which
// takes the headers out of the accessibility tree and makes some browsers
// drop the table semantics. Every element of those tables now carries its
// table role, so the semantics survive the CSS.
func TestCardTablesKeepTheirTableRoles(t *testing.T) {
	pages, _ := fs.Glob(templatesFS, "templates/*.html")
	tables := 0
	for _, p := range pages {
		b, _ := fs.ReadFile(templatesFS, p)
		for _, table := range cardTableRe.FindAllString(string(b), -1) {
			tables++
			for _, m := range tableTagRe.FindAllStringSubmatch(table, -1) {
				want := `role="` + tableRoles[m[1]] + `"`
				if !strings.Contains(m[0], want) {
					t.Errorf("%s: %s without %s", p, m[0], want)
				}
			}
		}
	}
	if tables < 8 {
		t.Fatalf("found %d cards-on-mobile tables", tables)
	}
}

// The header is hidden only visually and the labels drawn before each cell
// are not read on top of it.
func TestCardTablesCSSKeepsHeadersForScreenReaders(t *testing.T) {
	b, _ := fs.ReadFile(staticFS, "static/app.css")
	css := string(b)
	if strings.Contains(css, ".table.cards-on-mobile thead { display: none; }") {
		t.Fatal("the header of cards-on-mobile tables is still display:none")
	}
	for _, want := range []string{
		".table.cards-on-mobile thead { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }",
		`content: attr(data-label) / "";`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css lacks %s", want)
		}
	}
}

// PLAN 1.2.7: a notice is one block of text next to its shape. As a flex
// container every text run and inline element of a notice became a column
// of its own, and a long list of connections overflowed the card.
func TestNoticesAreOneTextBlock(t *testing.T) {
	b, _ := fs.ReadFile(staticFS, "static/app.css")
	css := string(b)
	rule := cssRule(t, css, ".flash")
	if strings.Contains(rule, "display: flex") || !strings.Contains(rule, "overflow-wrap: anywhere") {
		t.Fatalf(".flash is still a flex container or does not wrap long values: %s", rule)
	}
	if shape := cssRule(t, css, ".flash::before"); !strings.Contains(shape, "position: absolute") {
		t.Fatalf("the shape of a notice is not placed outside the text: %s", shape)
	}
}

// PLAN 1.2.5 and docs 09 §6: every control takes taps on at least 44 px at
// every width. Controls drawn smaller keep their look and get an invisible
// area around them; the rest are at least 44 px tall.
func TestControlsHave44pxTargets(t *testing.T) {
	b, _ := fs.ReadFile(staticFS, "static/app.css")
	css := string(b)
	area := cssRule(t, css, "button::after, .button::after, .who a::after, a.badge::after, details.more summary::after")
	for _, want := range []string{`content: ""`, "position: absolute", "min-width: 44px", "min-height: 44px"} {
		if !strings.Contains(area, want) {
			t.Errorf("the target area lacks %s: %s", want, area)
		}
	}
	for _, sel := range []string{".nav nav a", ".tabs a", ".tabs.small a", "input, select, textarea"} {
		if r := cssRule(t, css, sel); !strings.Contains(r, "min-height: 44px") {
			t.Errorf("%s is not 44 px tall: %s", sel, r)
		}
	}
}

// cssRule returns the declarations of the first top-level rule whose
// selector is exactly sel.
func cssRule(t *testing.T, css, sel string) string {
	t.Helper()
	i := strings.Index(css, "\n"+sel+" {")
	if i < 0 {
		t.Fatalf("app.css has no rule %q", sel)
	}
	rest := css[i+len(sel)+3:]
	return rest[:strings.Index(rest, "}")]
}
