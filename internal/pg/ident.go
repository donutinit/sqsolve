// SPDX-License-Identifier: AGPL-3.0-or-later

package pg

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	slugRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{2,39}$`)
	personRe = regexp.MustCompile(`^[a-z][a-z0-9]{1,19}$`)
	identRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
)

// Reserved prefixes for slugs.
var reservedPrefixes = []string{"pg_", "sqsolve", "template", "postgres"}

// ErrInvalidSlug is returned for slugs that do not follow the rules.
var ErrInvalidSlug = errors.New("SQS-L001 identificador inválido")

// ValidateSlug checks a library slug.
func ValidateSlug(s string) error {
	if !slugRe.MatchString(s) {
		return fmt.Errorf("%w: usa de 3 a 40 caracteres, minúsculas, números y guion bajo, empezando con letra", ErrInvalidSlug)
	}
	if strings.Contains(s, "__") {
		return fmt.Errorf("%w: no puede contener dos guiones bajos seguidos", ErrInvalidSlug)
	}
	for _, p := range reservedPrefixes {
		if strings.HasPrefix(s, p) {
			return fmt.Errorf("%w: no puede empezar con %q", ErrInvalidSlug, p)
		}
	}
	return nil
}

// ValidatePerson checks the person part of a member role name.
func ValidatePerson(s string) error {
	if !personRe.MatchString(s) {
		return fmt.Errorf("%w: el nombre de persona usa de 2 a 20 minúsculas o números, empezando con letra", ErrInvalidSlug)
	}
	return nil
}

// ValidateIdent checks any identifier sqsolve composes (auxiliary names).
func ValidateIdent(s string) error {
	if !identRe.MatchString(s) {
		return fmt.Errorf("%w: %q", ErrInvalidSlug, s)
	}
	return nil
}

// Ident validates and quotes an identifier. Both checks are always applied.
func Ident(s string) (string, error) {
	if err := ValidateIdent(s); err != nil {
		return "", err
	}
	return pgx.Identifier{s}.Sanitize(), nil
}

// MustIdent is Ident for names built by sqsolve itself from validated parts.
func MustIdent(s string) string {
	q, err := Ident(s)
	if err != nil {
		panic(err)
	}
	return q
}

// Literal quotes a string literal for utility statements that cannot take
// parameters. Values with backslashes or NUL bytes are rejected.
func Literal(s string) (string, error) {
	if strings.ContainsAny(s, "\\\x00") {
		return "", errors.New("SQS-P010 literal con caracteres no permitidos")
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'", nil
}

// MemberRole is the role name of a person credential.
func MemberRole(slug, person string) string { return slug + "__" + person }

// RestoreDB is the temporary database of a restore.
func RestoreDB(slug, short string) string { return slug + "__restore_" + short }

// PrevDB keeps the previous state after a swap.
func PrevDB(slug string, t time.Time) string {
	return slug + "__prev_" + t.UTC().Format("200601021504")
}

// TrashDB is a library in the trash.
func TrashDB(slug string, t time.Time) string {
	return slug + "__trash_" + t.UTC().Format("20060102150405")
}

// VerifyDB is the scratch database of a level 2 verification.
func VerifyDB(short string) string { return "sqsolve_verify_" + short }

// IsAuxiliary reports whether a database name is one of sqsolve's auxiliary
// names.
func IsAuxiliary(name string) bool {
	return strings.HasPrefix(name, "sqsolve_verify_") || strings.Contains(name, "__")
}
