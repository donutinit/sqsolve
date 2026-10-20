// SPDX-License-Identifier: AGPL-3.0-only

package drp

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

//go:embed model.json
var modelJSON []byte

// Field kinds in the serialization model.
const (
	KindScalar   = ""          // text content from a column
	KindObject   = "obj"       // a nested object; the column holds its id
	KindListText = "list:text" // <Element>text</Element> items from an array column or an association
	KindListObj  = "list:obj"  // <Element><Class DbId=...>...</Class></Element> items
)

// Model describes how Resolve serializes objects into .drp and .drx files.
type Model struct {
	// Resolve is the version the model was learned from (informational).
	Resolve string `json:"resolve"`
	// ProjectVersion is the DbPrjVer seen in that version.
	ProjectVersion int `json:"project_version"`
	// Classes maps an object class (the XML element name and DbType) to its shape.
	Classes map[string]*Class `json:"classes"`
	// Tables lists, per table, the columns the exporter reads with their PostgreSQL types.
	Tables map[string]map[string]string `json:"tables"`
	// Assoc maps "Class.Field" of a list field to the association table that stores it.
	Assoc map[string]string `json:"assoc"`
	// Backref maps "Class.Field" of a list field to the child table and column that point back to the owner.
	Backref map[string]Backref `json:"backref"`
	// EmptyNullUUID lists "Class.Field" uuid columns written as empty elements when null (others are omitted).
	EmptyNullUUID []string `json:"empty_null_uuid"`
	// TextInBytea lists "Table.Column" bytea columns that hold text.
	TextInBytea []string `json:"text_in_bytea"`
	// Guards name features never seen in exports: rows in these tables make the export refuse.
	Guards []Guard `json:"guards"`

	emptyNull map[string]bool
	textBytea map[string]bool
	tableOf   map[string]string
}

// Class is the shape of one object class.
type Class struct {
	Table    string              `json:"table"`
	Fields   []string            `json:"fields"`
	Kinds    map[string]string   `json:"kinds"`
	Children map[string][]string `json:"children"`
}

// Backref locates children by a foreign key column instead of an association table.
type Backref struct {
	Table  string `json:"table"`
	Column string `json:"column"`
}

// Guard is a feature the exporter does not reproduce; the presence of rows for
// an owner in Table (optionally with DbPropertyName = Prop) refuses the export.
type Guard struct {
	Class string `json:"class"`
	Field string `json:"field"`
	Table string `json:"table"`
	Prop  string `json:"prop"`
}

// Kind returns the kind of a field.
func (c *Class) Kind(field string) string { return c.Kinds[field] }

// ParseModel parses and validates a model.
func ParseModel(data []byte) (*Model, error) {
	m := &Model{}
	if err := json.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("invalid export model: %w", err)
	}
	m.emptyNull = map[string]bool{}
	for _, k := range m.EmptyNullUUID {
		m.emptyNull[k] = true
	}
	m.textBytea = map[string]bool{}
	for _, k := range m.TextInBytea {
		m.textBytea[k] = true
	}
	m.tableOf = map[string]string{}
	for name, c := range m.Classes {
		if c.Table == "" || m.Tables[c.Table] == nil {
			return nil, fmt.Errorf("export model: class %s has no table", name)
		}
		if m.Tables[c.Table][c.Table+"_id"] == "" {
			return nil, fmt.Errorf("export model: table %s does not declare its id column", c.Table)
		}
		m.tableOf[name] = c.Table
		for f, kids := range c.Children {
			var tables []string
			for _, k := range kids {
				kc := m.Classes[k]
				if kc == nil {
					return nil, fmt.Errorf("export model: %s.%s points to unknown class %s", name, f, k)
				}
				tables = append(tables, kc.Table)
			}
			sort.Strings(tables)
			if len(tables) > 0 && tables[0] != tables[len(tables)-1] {
				return nil, fmt.Errorf("export model: %s.%s mixes tables %v", name, f, tables)
			}
		}
		for f, k := range c.Kinds {
			switch k {
			case KindObject, KindListObj:
				if len(c.Children[f]) == 0 {
					return nil, fmt.Errorf("export model: %s.%s does not declare the child class", name, f)
				}
			case KindListText:
			default:
				return nil, fmt.Errorf("export model: %s.%s has unknown type %q", name, f, k)
			}
		}
	}
	for key, t := range m.Assoc {
		if m.Tables[t] == nil {
			return nil, fmt.Errorf("export model: association %s uses unknown table %s", key, t)
		}
	}
	for key, b := range m.Backref {
		if m.Tables[b.Table] == nil || m.Tables[b.Table][b.Column] == "" {
			return nil, fmt.Errorf("export model: reverse reference %s is invalid", key)
		}
	}
	return m, nil
}

var defaultModel = sync.OnceValues(func() (*Model, error) { return ParseModel(modelJSON) })

// DefaultModel returns the embedded model.
func DefaultModel() (*Model, error) { return defaultModel() }

// ChildTable returns the table of the objects nested in field f of class c.
func (m *Model) ChildTable(c *Class, f string) string {
	kids := c.Children[f]
	if len(kids) == 0 {
		return ""
	}
	return m.tableOf[kids[0]]
}

// TableOf returns the table of a class.
func (m *Model) TableOf(class string) string { return m.tableOf[class] }

// IsEmptyNullUUID reports whether a null uuid field is written as an empty element.
func (m *Model) IsEmptyNullUUID(class, field string) bool { return m.emptyNull[class+"."+field] }

// IsTextInBytea reports whether a bytea column holds text.
func (m *Model) IsTextInBytea(table, column string) bool { return m.textBytea[table+"."+column] }

// TableNames lists the tables the model reads, sorted.
func (m *Model) TableNames() []string {
	out := make([]string, 0, len(m.Tables))
	for t := range m.Tables {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// ClassNames lists the classes, sorted.
func (m *Model) ClassNames() []string {
	out := make([]string, 0, len(m.Classes))
	for c := range m.Classes {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
