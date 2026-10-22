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
