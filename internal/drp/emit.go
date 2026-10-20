// SPDX-License-Identifier: AGPL-3.0-only

package drp

import (
	"bytes"
	"fmt"
	"strings"
)

// maxDepth bounds object nesting; Resolve's own files never pass 20 levels.
const maxDepth = 64

// emitter writes objects of a loaded graph as Resolve's XML.
type emitter struct {
	m *Model
	g *graph
	b bytes.Buffer

	// Context the exporter's rewrite rules depend on (docs 22 §4).
	projectName string
	hasContent  bool
	inMediaPool bool

	notes []string
	depth int
}

// document renders one object as a complete XML document.
func (e *emitter) document(header, table, id string) ([]byte, error) {
	e.b.Reset()
	e.b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	if header != "" {
		e.b.WriteString(header)
		e.b.WriteByte('\n')
	}
	if e.g.get(table, id) == nil {
		return nil, fmt.Errorf("%w: object %s of %s", ErrNotFound, id, table)
	}
	if err := e.object(table, id, "", 0); err != nil {
		return nil, err
	}
	e.b.WriteByte('\n')
	return append([]byte(nil), e.b.Bytes()...), nil
}

func (e *emitter) note(format string, args ...any) {
	if len(e.notes) < 50 {
		e.notes = append(e.notes, fmt.Sprintf(format, args...))
	}
}

func pad(n int) string { return strings.Repeat(" ", n) }

func (e *emitter) object(table, id, expectClass string, indent int) error {
	r := e.g.get(table, id)
	if r == nil {
		return fmt.Errorf("%w: object %s of %s", ErrNotFound, id, table)
	}
	class := r.class
	if class == "" {
		class = expectClass
	}
	spec := e.m.Classes[class]
	if spec == nil {
		return fmt.Errorf("%w: class %q of table %s", ErrUnsupported, class, table)
	}
	e.depth++
	defer func() { e.depth-- }()
	if e.depth > maxDepth {
		return fmt.Errorf("SQS-E006 objects nest more than %d levels deep in %s %s", maxDepth, class, id)
	}
	e.b.WriteString(pad(indent))
	e.b.WriteString("<" + class + " DbId=\"" + id + "\">\n")
	for _, f := range spec.Fields {
		if err := e.field(class, spec, r, f, indent+1); err != nil {
			return err
		}
	}
	e.b.WriteString(pad(indent))
	e.b.WriteString("</" + class + ">")
	if indent > 0 {
		e.b.WriteByte('\n')
	}
	return nil
}

func (e *emitter) empty(f string, indent int) {
	e.b.WriteString(pad(indent) + "<" + f + "/>\n")
}

func (e *emitter) text(f, value string, indent int) {
	if value == "" {
		e.empty(f, indent)
		return
	}
	e.b.WriteString(pad(indent) + "<" + f + ">" + escape(value) + "</" + f + ">\n")
}

func (e *emitter) field(class string, spec *Class, r *row, f string, indent int) error {
	if done, err := e.override(class, r, f, indent); done || err != nil {
		return err
	}
	colType, hasCol := e.m.Tables[spec.Table][f]
	switch spec.Kind(f) {
	case KindObject:
		v, _ := r.vals[f].(string)
		if v == "" {
			if e.m.IsEmptyNullUUID(class, f) {
				e.empty(f, indent)
			}
			return nil
		}
		ct := e.m.ChildTable(spec, f)
		if e.g.get(ct, v) == nil {
			e.note("%s.%s points to object %s, which does not exist; skipped", class, f, v)
			return nil
		}
		e.b.WriteString(pad(indent) + "<" + f + ">\n")
		if err := e.object(ct, v, spec.Children[f][0], indent+1); err != nil {
			return err
		}
		e.b.WriteString(pad(indent) + "</" + f + ">\n")
		return nil
	case KindListText:
		var items []string
		if hasCol {
			s, _ := r.vals[f].(string)
			var err error
			if items, err = parseVector(s); err != nil {
				return err
			}
		} else if at, ok := e.m.Assoc[class+"."+f]; ok {
			items = e.g.associates(at, f, r.id)
		}
		if len(items) == 0 {
			e.empty(f, indent)
			return nil
		}
		e.b.WriteString(pad(indent) + "<" + f + ">\n")
		for _, it := range items {
			e.b.WriteString(pad(indent+1) + "<Element>" + escape(it) + "</Element>\n")
		}
		e.b.WriteString(pad(indent) + "</" + f + ">\n")
		return nil
	case KindListObj:
		var ids []string
		ct := e.m.ChildTable(spec, f)
		if at, ok := e.m.Assoc[class+"."+f]; ok {
			ids = e.g.associates(at, f, r.id)
		} else if br, ok := e.m.Backref[class+"."+f]; ok {
			ids = e.g.backrefs(br.Table, br.Column, r.id)
		}
		var present []string
		for _, id := range ids {
			if e.g.get(ct, id) == nil {
				e.note("%s.%s lists object %s, which does not exist; skipped", class, f, id)
				continue
			}
			present = append(present, id)
		}
		if len(present) == 0 {
			e.empty(f, indent)
			return nil
		}
		e.b.WriteString(pad(indent) + "<" + f + ">\n")
		for _, id := range present {
			e.b.WriteString(pad(indent+1) + "<Element>\n")
			if err := e.object(ct, id, spec.Children[f][0], indent+2); err != nil {
				return err
			}
			e.b.WriteString(pad(indent+1) + "</Element>\n")
		}
		e.b.WriteString(pad(indent) + "</" + f + ">\n")
		return nil
	}
	// Scalar. Fields without a column are lists the exporter never saw filled.
	if !hasCol {
		e.empty(f, indent)
		return nil
	}
	v := r.vals[f]
	fam := familyOf(colType)
	if v == nil {
		if fam == famUUID && !e.m.IsEmptyNullUUID(class, f) {
			return nil
		}
		e.empty(f, indent)
		return nil
	}
	if b, ok := v.([]byte); ok && class == "ListMgt::LmVersion" && f == "Body" && e.inMediaPool {
		nb, err := rewriteOFXContext(b, r.id)
		if err != nil {
			return fmt.Errorf("SQS-E006 node tree of %s: %w", r.id, err)
		}
		v = nb
	}
	s, err := formatScalar(v, e.m.IsTextInBytea(spec.Table, f))
	if err != nil {
		return fmt.Errorf("%w (%s.%s)", err, class, f)
	}
	e.text(f, s, indent)
	return nil
}

// override applies the rewrites the Resolve exporter makes (docs 22 §4).
func (e *emitter) override(class string, r *row, f string, indent int) (bool, error) {
	switch class + "." + f {
	case "SM_Project.UpToDate":
		if e.hasContent {
			e.text(f, "false", indent)
			return true, nil
		}
	case "Sm2TiAudioClip.RenderTextEnabled", "Sm2TiAudioClip.RenderTextGanged", "Sm2TiAudioClip.RenderTextPrefixed":
		e.text(f, "false", indent)
		return true, nil
	case "SM_Config.Name":
		if e.projectName != "" {
			e.text(f, e.projectName+".Cfg", indent)
			return true, nil
		}
	case "Sm2TiVideoClip.Thumbnail":
		// Rendered from the media at export time; it is not in the library.
		e.empty(f, indent)
		return true, nil
	case "SM_Project.StreamCursorVector":
		s, _ := r.vals[f].(string)
		items, err := parseVector(s)
		if err != nil {
			return true, err
		}
		if len(items) == 0 {
			e.empty(f, indent)
			return true, nil
		}
		e.b.WriteString(pad(indent) + "<" + f + ">\n")
		for range items {
			e.b.WriteString(pad(indent+1) + "<Element>0</Element>\n")
		}
		e.b.WriteString(pad(indent) + "</" + f + ">\n")
		return true, nil
	}
	return false, nil
}
