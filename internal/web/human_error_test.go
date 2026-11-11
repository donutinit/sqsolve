// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// A long error is shortened for the notice at a character boundary: a name
// with accents that falls on the cut is never left as half a character.
func TestHumanErrorCutsAtACharacterBoundary(t *testing.T) {
	// One ASCII byte, then two-byte characters: byte 600 is the second half
	// of one of them.
	long := "x" + strings.Repeat("ñ", 400)
	got := humanError(errors.New(long))
	if !utf8.ValidString(got) {
		t.Fatalf("the shortened text is not valid UTF-8: %q", got[len(got)-12:])
	}
	if want := "x" + strings.Repeat("ñ", 299) + "..."; got != want {
		t.Fatalf("shortened to %d bytes ending %q, want %d bytes", len(got), got[len(got)-9:], len(want))
	}
	if short := humanError(errors.New("SQS-L002 library not found")); short != "SQS-L002 library not found" {
		t.Fatalf("a short error was changed: %q", short)
	}
}
