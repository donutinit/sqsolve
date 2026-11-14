// SPDX-License-Identifier: AGPL-3.0-only

package pg_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/donutinit/sqsolve/internal/pg"
)

// redactVerifier is the secret of the redaction tests; its literal is what
// the statements carry.
const (
	redactVerifier = "SCRAM-SHA-256$4096:c2FsdA==$c3RvcmVk:c2VydmVy"
	redactLiteral  = "'" + redactVerifier + "'"
)

// hidden is s with secret replaced, for what a failing test prints: a broken
// redaction must not put the secret in the test output either.
func hidden(s, secret string) string { return strings.ReplaceAll(s, secret, "<secret>") }

// refusedStatement is a PostgreSQL error with the verifier in every text
// field that can quote a statement.
func refusedStatement() *pgconn.PgError {
	return &pgconn.PgError{
		Severity: "ERROR", Code: "42601", Position: 31,
		Message: `syntax error at or near "` + redactLiteral + `"`,
		Detail:  "d " + redactVerifier, Hint: "h " + redactVerifier,
		Where: "w " + redactVerifier, InternalQuery: "q " + redactVerifier,
	}
}

// valueError is an error whose text is in a field of a value, which fmt
// prints by reflection when nothing stops it.
type valueError struct{ s string }

func (e valueError) Error() string { return e.s }

// TestRedactKeepsTheChain: the error of a statement that carried a verifier
// still matches the sentinels and the SQLSTATE of what it was made from, and
// the PostgreSQL error it hands out is a copy without the verifier.
func TestRedactKeepsTheChain(t *testing.T) {
	const v = redactVerifier
	show := func(x any) string { return hidden(fmt.Sprintf("%+v", x), v) }
	// A PostgreSQL error.
	pe := refusedStatement()
	err := pg.Redact(pe, v)
	if got, want := err.Error(), `ERROR: syntax error at or near "'[redacted]'" (SQLSTATE 42601)`; got != want {
		t.Errorf("text %q, want %q", hidden(got, v), want)
	}
	if !pg.IsCode(err, "42601") || pg.IsCode(err, "42704") {
		t.Errorf("SQLSTATE lost: %s", show(err))
	}
	var got *pgconn.PgError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As does not find the PostgreSQL error: %s", show(err))
	}
	want := pgconn.PgError{
		Severity: "ERROR", Code: "42601", Position: 31,
		Message: `syntax error at or near "'[redacted]'"`,
		Detail:  "d [redacted]", Hint: "h [redacted]", Where: "w [redacted]", InternalQuery: "q [redacted]",
	}
	if got == pe || *got != want {
		t.Errorf("handed out %s (the original: %v), want a copy %+v", show(*got), got == pe, want)
	}
	if *pe != *refusedStatement() {
		t.Errorf("the original error was changed: %s", show(*pe))
	}

	// The timeouts WrapTimeout adds.
	lock := pg.Redact(pg.WrapTimeout(&pgconn.PgError{Severity: "ERROR", Code: "55P03", Message: "canceling statement due to lock timeout"}), v)
	if !errors.Is(lock, pg.ErrLockTimeout) || errors.Is(lock, pg.ErrStatementTimeout) || !pg.IsCode(lock, "55P03") ||
		lock.Error() != pg.ErrLockTimeout.Error()+": ERROR: canceling statement due to lock timeout (SQLSTATE 55P03)" {
		t.Errorf("lock timeout: %s", show(lock))
	}
	stmt := pg.Redact(pg.WrapTimeout(&pgconn.PgError{Severity: "ERROR", Code: "57014", Message: "canceling statement due to statement timeout"}), v)
	if !errors.Is(stmt, pg.ErrStatementTimeout) || errors.Is(stmt, pg.ErrLockTimeout) || !pg.IsCode(stmt, "57014") ||
		stmt.Error() != pg.ErrStatementTimeout.Error()+": ERROR: canceling statement due to statement timeout (SQLSTATE 57014)" {
		t.Errorf("statement timeout: %s", show(stmt))
	}

	// No server: the sentinel alone, with nothing to unwrap.
	down := pg.Redact(pg.ErrUnavailable, redactLiteral)
	if !errors.Is(down, pg.ErrUnavailable) || down.Error() != "SQS-P001 PostgreSQL is not available" || errors.Unwrap(down) != nil || errors.As(down, &got) {
		t.Errorf("no server: %s", show(down))
	}

	// A cancelled or expired context.
	late := pg.Redact(fmt.Errorf("sending %s: %w", redactLiteral, context.DeadlineExceeded), redactLiteral)
	if !errors.Is(late, context.DeadlineExceeded) || errors.Is(late, context.Canceled) || late.Error() != "sending [redacted]: context deadline exceeded" {
		t.Errorf("expired context: %s", show(late))
	}

	// Wrapped with a code, as CreateLibraryRole and Bootstrap do.
	w := fmt.Errorf("SQS-P020 creating role lib: %w", lock)
	if !errors.Is(w, pg.ErrLockTimeout) || !pg.IsCode(w, "55P03") || w.Error() != "SQS-P020 creating role lib: "+lock.Error() {
		t.Errorf("wrapped: %s", show(w))
	}

	// Nothing to redact.
	if pg.Redact(nil, v) != nil {
		t.Error("nil became an error")
	}
	if err := pg.Redact(pe, ""); err != error(pe) {
		t.Errorf("an empty secret changed the error: %s", show(err))
	}
}

// TestRedactedErrorNeverPrintsTheSecret: no fmt verb, slog handler, JSON
// encoding or unwrapped error of a redacted error shows the verifier, also
// when the error it was made from keeps its text in a field of a value.
func TestRedactedErrorNeverPrintsTheSecret(t *testing.T) {
	const v = redactVerifier
	verbs := []string{"%v", "%+v", "%s", "%q", "%#v", "%x", "%X", "%d", "%t", "%20.5s"}
	for name, err := range map[string]error{
		"PostgreSQL error": pg.Redact(refusedStatement(), v),
		"lock timeout":     pg.Redact(pg.WrapTimeout(&pgconn.PgError{Severity: "ERROR", Code: "55P03", Message: "lock timeout near " + redactLiteral}), redactLiteral),
		"value error":      pg.Redact(valueError{"boom " + v}, v),
	} {
		if strings.Contains(err.Error(), v) || !strings.Contains(err.Error(), "[redacted]") {
			t.Errorf("%s: text %q", name, hidden(err.Error(), v))
		}
		// Every verb prints what it would print of the redacted text.
		for _, verb := range verbs {
			if got, want := fmt.Sprintf(verb, err), fmt.Sprintf(verb, err.Error()); got != want {
				t.Errorf("%s with %s: %q, want %q", name, verb, hidden(got, v), hidden(want, v))
			}
		}
		out := map[string]string{
			"Sprint":  fmt.Sprint(err),
			"wrapped": fmt.Errorf("SQS-P020 creating role lib: %w", err).Error(),
		}
		for format, h := range map[string]func(*bytes.Buffer) slog.Handler{
			"slog JSON": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
			"slog text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
		} {
			var b bytes.Buffer
			slog.New(h(&b)).Warn("waiting for PostgreSQL", "err", err)
			out[format] = b.String()
			if !strings.Contains(b.String(), "[redacted]") {
				t.Errorf("%s in %s: the error is not in the line: %s", name, format, hidden(b.String(), v))
			}
		}
		j, jerr := json.Marshal(map[string]any{"err": err})
		if jerr != nil || string(j) != `{"err":{}}` {
			t.Errorf("%s as JSON: %s %v", name, hidden(string(j), v), jerr)
		}
		for u, depth := errors.Unwrap(err), 0; u != nil; u, depth = errors.Unwrap(u), depth+1 {
			out[fmt.Sprintf("unwrapped %d", depth)] = fmt.Sprintf("%v %+v %#v", u, u, u)
		}
		for where, s := range out {
			if strings.Contains(s, v) {
				t.Errorf("%s: %s shows the verifier: %s", name, where, hidden(s, v))
			}
		}
	}
}
