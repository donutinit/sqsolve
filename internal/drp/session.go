// SPDX-License-Identifier: AGPL-3.0-only

package drp

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/donutinit/sqsolve/internal/compat"
	"github.com/donutinit/sqsolve/internal/pg"
)

// Errors of the exporter. Every message carries its SQS-E code.
var (
	// ErrSchema means the library's tables do not match the model.
	ErrSchema = errors.New("SQS-E001 the library does not have the tables and columns the experimental export knows")
	// ErrNotFound means the project, still or album does not exist.
	ErrNotFound = errors.New("SQS-E003 does not exist in the library")
	// ErrUnsupported means the project uses something the exporter never saw.
	ErrUnsupported = errors.New("SQS-E004 the experimental export does not reproduce this feature")
)

// StatementTimeout bounds every query of a session.
const StatementTimeout = "60s"

// Session is a read-only view of one library for the duration of an export.
type Session struct {
	conn   *pgx.Conn
	tx     pgx.Tx
	m      *Model
	schema schema
	// Fingerprint is the live schema fingerprint of the library.
	Fingerprint string
	// Known is the registry entry of the fingerprint, if any.
	Known compat.Known
	// KnownOK reports whether the fingerprint is in the registry.
	KnownOK bool
}

// schema is the subset of the catalog the model reads: table -> column -> type family.
type schema map[string]map[string]family

func (s schema) has(table string) bool { return s[table] != nil }

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidID reports whether s looks like an object id and returns it lowercased.
func ValidID(s string) (string, bool) {
	if !uuidRe.MatchString(s) {
		return "", false
	}
	return strings.ToLower(s), true
}

// Open starts a READ ONLY transaction on conn, checks the library against the
// model and takes ownership of conn: Close releases both.
func Open(ctx context.Context, conn *pgx.Conn) (*Session, error) {
	m, err := DefaultModel()
	if err != nil {
		return nil, err
	}
	return open(ctx, conn, m)
}

func open(ctx context.Context, conn *pgx.Conn, m *Model) (*Session, error) {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("SQS-E005 could not open the library: %w", err)
	}
	s := &Session{conn: conn, tx: tx, m: m}
	ok := false
	defer func() {
		if !ok {
			s.Close(ctx)
		}
	}()
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '"+StatementTimeout+"'"); err != nil {
		return nil, err
	}
	fp, err := pg.SchemaFingerprint(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("SQS-E005 could not read the catalog: %w", err)
	}
	s.Fingerprint = fp.Hash
	s.Known, s.KnownOK = compat.Lookup(fp.Hash)
	if s.schema, err = readSchema(ctx, tx, m); err != nil {
		return nil, err
	}
	if err := checkContract(m, s.schema); err != nil {
		return nil, err
	}
	ok = true
	return s, nil
}

// Close ends the transaction and the connection.
func (s *Session) Close(ctx context.Context) {
	if s.tx != nil {
		s.tx.Rollback(ctx)
		s.tx = nil
	}
	if s.conn != nil {
		s.conn.Close(ctx)
		s.conn = nil
	}
}

// Model returns the serialization model in use.
func (s *Session) Model() *Model { return s.m }

// Header returns the version comment Resolve writes after the XML declaration,
// or "" when the registry does not know this schema. prjVer 0 takes the
// registry's project version.
func (s *Session) Header(prjVer int) string {
	if !s.KnownOK {
		return ""
	}
	if prjVer == 0 {
		prjVer = s.Known.ProjectVersion
	}
	return fmt.Sprintf(`<!--DbAppVer="%s" DbPrjVer="%d"-->`, s.Known.AppVersion, prjVer)
}

func readSchema(ctx context.Context, tx pgx.Tx, m *Model) (schema, error) {
	rows, err := tx.Query(ctx, `SELECT c.relname, a.attname, format_type(a.atttypid, a.atttypmod)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid
		WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND a.attnum > 0 AND NOT a.attisdropped
		  AND c.relname = ANY($1)`, m.TableNames())
	if err != nil {
		return nil, fmt.Errorf("SQS-E005 could not read the catalog: %w", err)
	}
	defer rows.Close()
	out := schema{}
	for rows.Next() {
		var t, col, typ string
		if err := rows.Scan(&t, &col, &typ); err != nil {
			return nil, err
		}
		if out[t] == nil {
			out[t] = map[string]family{}
		}
		out[t][col] = familyOf(typ)
	}
	return out, rows.Err()
}

// checkContract verifies that every table and column the model reads exists
// with a compatible type. Guard tables may be absent.
func checkContract(m *Model, s schema) error {
	optional := map[string]bool{}
	for _, g := range m.Guards {
		optional[g.Table] = true
	}
	var missing []string
	for _, t := range m.TableNames() {
		cols := s[t]
		if cols == nil {
			if !optional[t] {
				missing = append(missing, "table "+t)
			}
			continue
		}
		names := make([]string, 0, len(m.Tables[t]))
		for c := range m.Tables[t] {
			names = append(names, c)
		}
		sort.Strings(names)
		for _, c := range names {
			want := familyOf(m.Tables[t][c])
			got, ok := cols[c]
			switch {
			case !ok:
				missing = append(missing, t+"."+c)
			case got != want:
				missing = append(missing, fmt.Sprintf("%s.%s is %s and %s was expected", t, c, got, want))
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	detail := strings.Join(missing[:min(len(missing), 5)], "; ")
	if len(missing) > 5 {
		detail += fmt.Sprintf(" and %d more", len(missing)-5)
	}
	return fmt.Errorf("%w: %s", ErrSchema, detail)
}

// ident quotes a table or column name from the model.
func ident(name string) string { return pgx.Identifier{name}.Sanitize() }
