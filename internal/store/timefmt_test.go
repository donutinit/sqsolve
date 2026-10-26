// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"database/sql"
	"testing"
	"time"
)

// Nullable times round-trip through their stored form: a NULL or empty
// column reads as nil, a stored time reads as that time, a nil pointer is
// stored as NULL, and an unparsable value reads as the zero time.
func TestNullableTimesRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 25, 8, 30, 0, 123456000, time.UTC)
	if NullT(sql.NullString{}) != nil || NullT(sql.NullString{Valid: true}) != nil {
		t.Fatal("a NULL or empty time is not nil")
	}
	stored := TPtr(&at)
	got := NullT(sql.NullString{String: stored.(string), Valid: true})
	if got == nil || !got.Equal(at) {
		t.Fatalf("round trip %v", got)
	}
	if TPtr(nil) != nil {
		t.Fatal("a nil time is not stored as NULL")
	}
	if !ParseT("yesterday").IsZero() || !ParseT("").IsZero() {
		t.Fatal("an invalid time is not the zero time")
	}
}

// JSON gives the encoding of a value, and "{}" for nil or a value that
// cannot be encoded, so a details column is always valid JSON.
func TestJSONAlwaysEncodes(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want string
	}{
		{nil, "{}"},
		{map[string]any{"a": 1}, `{"a":1}`},
		{[]string{"x"}, `["x"]`},
		{make(chan int), "{}"},
		{map[string]any{"f": func() {}}, "{}"},
	} {
		if got := JSON(tc.in); got != tc.want {
			t.Errorf("JSON(%T) = %s, want %s", tc.in, got, tc.want)
		}
	}
}
