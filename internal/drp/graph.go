// SPDX-License-Identifier: AGPL-3.0-only

package drp

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// row is one object loaded from the library.
type row struct {
	table string
	id    string
	class string
	vals  map[string]any
}

// graph holds every object an export needs, loaded breadth first so each
// table is queried once per level instead of once per object.
type graph struct {
	rows    map[string]map[string]*row // table -> id -> row
	assoc   map[string][]string        // assoc table, property, owner -> associates in DbIndex order
	backref map[string][]string        // child table, column, owner -> child ids
}

func newGraph() *graph {
	return &graph{rows: map[string]map[string]*row{}, assoc: map[string][]string{}, backref: map[string][]string{}}
}

func (g *graph) get(table, id string) *row { return g.rows[table][id] }

func (g *graph) ofTable(table string) []*row {
	out := make([]*row, 0, len(g.rows[table]))
	for _, r := range g.rows[table] {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

func assocKey(table, prop, owner string) string { return table + "\x00" + prop + "\x00" + owner }

func (g *graph) associates(table, prop, owner string) []string {
	return g.assoc[assocKey(table, prop, owner)]
}

func (g *graph) backrefs(table, column, owner string) []string {
	return g.backref[assocKey(table, column, owner)]
}

const chunkSize = 500

// load fetches the rows of ids in table and everything they reference.
func (s *Session) load(ctx context.Context, g *graph, table string, ids []string) error {
	pending := map[string][]string{table: ids}
	for len(pending) > 0 {
		next := map[string][]string{}
		for t, ids := range pending {
			ids = g.missing(t, ids)
			if len(ids) == 0 {
				continue
			}
			rows, err := s.fetch(ctx, t, ids)
			if err != nil {
				return err
			}
			byClass := map[string][]*row{}
			for _, r := range rows {
				if g.rows[t] == nil {
					g.rows[t] = map[string]*row{}
				}
				g.rows[t][r.id] = r
				byClass[r.class] = append(byClass[r.class], r)
			}
			for cls, rs := range byClass {
				spec := s.m.Classes[cls]
				if spec == nil {
					return fmt.Errorf("%w: class %q of table %s", ErrUnsupported, cls, t)
				}
				if err := s.loadReferences(ctx, g, cls, spec, rs, next); err != nil {
					return err
				}
			}
		}
		pending = next
	}
	return nil
}

func (g *graph) missing(table string, ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if id == "" || seen[id] || g.rows[table][id] != nil {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// loadReferences queues the objects referenced by rows of one class and
// checks the guards of that class.
func (s *Session) loadReferences(ctx context.Context, g *graph, cls string, spec *Class, rs []*row, next map[string][]string) error {
	owners := make([]string, len(rs))
	for i, r := range rs {
		owners[i] = r.id
	}
	for _, f := range spec.Fields {
		switch spec.Kind(f) {
		case KindObject:
			ct := s.m.ChildTable(spec, f)
			for _, r := range rs {
				if v, _ := r.vals[f].(string); v != "" {
					next[ct] = append(next[ct], v)
				}
			}
		case KindListObj, KindListText:
			key := cls + "." + f
			if at, ok := s.m.Assoc[key]; ok {
				if err := s.loadAssoc(ctx, g, at, f, owners); err != nil {
					return err
				}
				if spec.Kind(f) == KindListObj {
					ct := s.m.ChildTable(spec, f)
					for _, o := range owners {
						next[ct] = append(next[ct], g.associates(at, f, o)...)
					}
				}
			} else if br, ok := s.m.Backref[key]; ok {
				if err := s.loadBackref(ctx, g, br, owners); err != nil {
					return err
				}
				for _, o := range owners {
					next[br.Table] = append(next[br.Table], g.backrefs(br.Table, br.Column, o)...)
				}
			}
		}
	}
	for _, gd := range s.m.Guards {
		if gd.Class != cls || !s.schema.has(gd.Table) {
			continue
		}
		if err := s.checkGuard(ctx, gd, owners); err != nil {
			return err
		}
	}
	return nil
}

// fetch reads the model's columns of the given rows.
func (s *Session) fetch(ctx context.Context, table string, ids []string) ([]*row, error) {
	cols := s.m.Tables[table]
	names := make([]string, 0, len(cols))
	for c := range cols {
		names = append(names, c)
	}
	sort.Strings(names)
	exprs := make([]string, len(names))
	for i, c := range names {
		exprs[i] = selectExpr(c, familyOf(cols[c]))
	}
	pk := table + "_id"
	q := "SELECT " + strings.Join(exprs, ", ") + " FROM " + ident(table) + " WHERE " + ident(pk) + "::text = ANY($1)"
	var out []*row
	for _, chunk := range chunks(ids) {
		rows, err := s.tx.Query(ctx, q, chunk)
		if err != nil {
			return nil, fmt.Errorf("SQS-E005 reading %s: %w", table, err)
		}
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				rows.Close()
				return nil, err
			}
			r := &row{table: table, vals: make(map[string]any, len(names))}
			for i, c := range names {
				r.vals[c] = vals[i]
			}
			r.id, _ = r.vals[pk].(string)
			r.class, _ = r.vals["DbType"].(string)
			out = append(out, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("SQS-E005 reading %s: %w", table, err)
		}
	}
	return out, nil
}

// selectExpr renders a column so that pgx returns it in the form the emitter
// writes: uuids and arrays as text, timestamps with three decimals.
func selectExpr(col string, fam family) string {
	q := ident(col)
	switch fam {
	case famUUID, famIntArray:
		return q + "::text"
	case famTime:
		return `to_char(` + q + `, 'YYYY-MM-DD"T"HH24:MI:SS.MS')`
	}
	return q
}

func (s *Session) loadAssoc(ctx context.Context, g *graph, table, prop string, owners []string) error {
	q := `SELECT "DbOwner"::text, "DbAssociate"::text FROM ` + ident(table) +
		` WHERE "DbPropertyName" = $1 AND "DbOwner"::text = ANY($2) ORDER BY "DbOwner", "DbIndex"`
	for _, o := range owners {
		g.assoc[assocKey(table, prop, o)] = nil
	}
	for _, chunk := range chunks(owners) {
		rows, err := s.tx.Query(ctx, q, prop, chunk)
		if err != nil {
			return fmt.Errorf("SQS-E005 reading %s: %w", table, err)
		}
		for rows.Next() {
			var owner, assoc string
			if err := rows.Scan(&owner, &assoc); err != nil {
				rows.Close()
				return err
			}
			k := assocKey(table, prop, owner)
			g.assoc[k] = append(g.assoc[k], assoc)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) loadBackref(ctx context.Context, g *graph, br Backref, owners []string) error {
	pk := br.Table + "_id"
	q := `SELECT ` + ident(br.Column) + `::text, ` + ident(pk) + `::text FROM ` + ident(br.Table) +
		` WHERE ` + ident(br.Column) + `::text = ANY($1) ORDER BY 1, 2`
	for _, o := range owners {
		g.backref[assocKey(br.Table, br.Column, o)] = nil
	}
	for _, chunk := range chunks(owners) {
		rows, err := s.tx.Query(ctx, q, chunk)
		if err != nil {
			return fmt.Errorf("SQS-E005 reading %s: %w", br.Table, err)
		}
		for rows.Next() {
			var owner, id string
			if err := rows.Scan(&owner, &id); err != nil {
				rows.Close()
				return err
			}
			k := assocKey(br.Table, br.Column, owner)
			g.backref[k] = append(g.backref[k], id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) checkGuard(ctx context.Context, gd Guard, owners []string) error {
	q := `SELECT 1 FROM ` + ident(gd.Table) + ` WHERE "DbOwner"::text = ANY($1)`
	args := []any{owners}
	if gd.Prop != "" {
		q += ` AND "DbPropertyName" = $2`
		args = append(args, gd.Prop)
	}
	q += " LIMIT 1"
	for _, chunk := range chunks(owners) {
		args[0] = chunk
		var one int
		err := s.tx.QueryRow(ctx, q, args...).Scan(&one)
		if err == nil {
			return fmt.Errorf("%w: the project uses %s (%s)", ErrUnsupported, gd.Field, gd.Class)
		}
		if !strings.Contains(err.Error(), "no rows") {
			return fmt.Errorf("SQS-E005 reading %s: %w", gd.Table, err)
		}
	}
	return nil
}

func chunks(ids []string) [][]string {
	var out [][]string
	for len(ids) > chunkSize {
		out = append(out, ids[:chunkSize])
		ids = ids[chunkSize:]
	}
	if len(ids) > 0 {
		out = append(out, ids)
	}
	return out
}
