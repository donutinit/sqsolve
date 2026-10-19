// SPDX-License-Identifier: AGPL-3.0-only

package drp

import (
	"strings"
	"testing"
)

func TestDefaultModel(t *testing.T) {
	m, err := DefaultModel()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Classes) != 41 {
		t.Errorf("%d classes", len(m.Classes))
	}
	for _, c := range []string{"SM_Project", "Gallery::GyStill", "ListMgt::LmVersion"} {
		if m.Classes[c] == nil {
			t.Errorf("class %s missing", c)
		}
	}
	for name, c := range m.Classes {
		if c.Table == "" || m.Tables[c.Table] == nil || m.TableOf(name) != c.Table {
			t.Errorf("%s: table %q", name, c.Table)
		}
		for f, k := range c.Kinds {
			if (k == KindObject || k == KindListObj) && (len(c.Children[f]) == 0 || m.ChildTable(c, f) == "") {
				t.Errorf("%s.%s: no children", name, f)
			}
		}
	}
	if m.Resolve == "" || m.ProjectVersion == 0 {
		t.Error("model without version")
	}
	if !m.IsEmptyNullUUID("SM_Project", "Folder") || m.IsEmptyNullUUID("SM_Project", "User") {
		t.Error("empty null uuid list")
	}
	if !m.IsTextInBytea("Sm2MpMedia", "SlateTC") || m.IsTextInBytea("Sm2MpMedia", "FieldsBlob") {
		t.Error("text in bytea list")
	}
	if names := m.TableNames(); len(names) != len(m.Tables) || names[0] > names[len(names)-1] {
		t.Error("table names")
	}
	if names := m.ClassNames(); len(names) != 41 || names[0] != "BtAudioInfo" {
		t.Errorf("class names: %v", names[:3])
	}
}

const minimalModel = `{"classes": {"A": {"table": "A", "fields": ["X", "V"], "kinds": {"X": "obj"}, "children": {"X": ["B"]}},
 "B": {"table": "B", "fields": ["V"]}},
 "tables": {"A": {"A_id": "uuid", "X": "uuid", "V": "integer"}, "B": {"B_id": "uuid", "V": "integer"}}}`

func TestParseModelRejects(t *testing.T) {
	if _, err := ParseModel([]byte(minimalModel)); err != nil {
		t.Fatalf("minimal model: %v", err)
	}
	cases := map[string]string{
		"{":                 `{`,
		"no tiene tabla":    `{"classes": {"A": {"fields": []}}}`,
		"columna de id":     `{"classes": {"A": {"table": "A", "fields": []}}, "tables": {"A": {"V": "integer"}}}`,
		"clase desconocida": strings.Replace(minimalModel, `["B"]`, `["Z"]`, 1),
		"tipo desconocido":  strings.Replace(minimalModel, `"X": "obj"`, `"X": "weird"`, 1),
		"no declara la clase hija": `{"classes": {"A": {"table": "A", "fields": ["X"], "kinds": {"X": "obj"}}},
			"tables": {"A": {"A_id": "uuid", "X": "uuid"}}}`,
		"tabla desconocida":  strings.Replace(minimalModel, `"tables":`, `"assoc": {"A.X": "Nope"}, "tables":`, 1),
		"referencia inversa": strings.Replace(minimalModel, `"tables":`, `"backref": {"A.X": {"table": "B", "column": "nope"}}, "tables":`, 1),
	}
	for want, src := range cases {
		_, err := ParseModel([]byte(src))
		if err == nil || !strings.Contains(err.Error(), "modelo de exportación") || !strings.Contains(err.Error(), strings.TrimPrefix(want, "{")) {
			t.Errorf("%s: %v", want, err)
		}
	}
}
