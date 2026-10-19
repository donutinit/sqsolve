// SPDX-License-Identifier: AGPL-3.0-only

package drp

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// family groups PostgreSQL types by how Resolve writes them.
type family int

const (
	famUnknown family = iota
	famUUID
	famBool
	famInt
	famFloat
	famText
	famBytes
	famTime
	famIntArray
)

// familyOf classifies a PostgreSQL type name as format_type prints it.
func familyOf(pgType string) family {
	t := strings.ToLower(strings.TrimSpace(pgType))
	switch {
	case t == "uuid":
		return famUUID
	case t == "boolean":
		return famBool
	case t == "integer", t == "bigint", t == "smallint":
		return famInt
	case t == "double precision", t == "real", t == "numeric":
		return famFloat
	case t == "text", t == "character varying", strings.HasPrefix(t, "character varying("), strings.HasPrefix(t, "character("):
		return famText
	case t == "bytea":
		return famBytes
	case t == "timestamp without time zone", t == "timestamp with time zone":
		return famTime
	case t == "integer[]", t == "bigint[]", t == "smallint[]":
		return famIntArray
	}
	return famUnknown
}

func (f family) String() string {
	switch f {
	case famUUID:
		return "uuid"
	case famBool:
		return "booleano"
	case famInt:
		return "entero"
	case famFloat:
		return "decimal"
	case famText:
		return "texto"
	case famBytes:
		return "bytea"
	case famTime:
		return "fecha"
	case famIntArray:
		return "arreglo de enteros"
	}
	return "desconocido"
}

// formatScalar renders a scanned value the way Resolve writes it. Nil must be
// handled by the caller.
func formatScalar(v any, text bool) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case bool:
		if x {
			return "true", nil
		}
		return "false", nil
	case int16:
		return strconv.FormatInt(int64(x), 10), nil
	case int32:
		return strconv.FormatInt(int64(x), 10), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float32:
		return formatFloat(float64(x)), nil
	case float64:
		return formatFloat(x), nil
	case []byte:
		if text {
			return string(x), nil
		}
		return hex.EncodeToString(x), nil
	}
	return "", fmt.Errorf("SQS-E006 valor de tipo %T no previsto", v)
}

// formatFloat writes the shortest decimal that round-trips, without exponent
// and without a trailing ".0", as seen in every value of the corpus (docs 22).
func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// parseVector splits a PostgreSQL array literal such as "{0,1}" into its items.
func parseVector(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return nil, fmt.Errorf("SQS-E006 arreglo con formato no previsto: %q", s)
	}
	s = s[1 : len(s)-1]
	if s == "" {
		return nil, nil
	}
	return strings.Split(s, ","), nil
}

// escape writes text content the way Qt's XML writer does.
func escape(s string) string {
	if !strings.ContainsAny(s, "&<>\"") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
