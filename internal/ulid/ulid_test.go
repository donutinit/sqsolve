// SPDX-License-Identifier: AGPL-3.0-only

package ulid

import (
	"regexp"
	"testing"
)

func TestNewFormatAndOrder(t *testing.T) {
	re := regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
	prev := ""
	for i := 0; i < 2000; i++ {
		id := New()
		if !re.MatchString(id) {
			t.Fatalf("bad ulid %q", id)
		}
		if id <= prev {
			t.Fatalf("not monotonic: %q after %q", id, prev)
		}
		prev = id
	}
}

func TestShort(t *testing.T) {
	if got := Short("01JB7Y6Z3K9M2Q8R4T5V6W7X8Y"); got != "5v6w7x8y" {
		t.Fatalf("got %q", got)
	}
}

func TestValid(t *testing.T) {
	if id := New(); !Valid(id) {
		t.Fatalf("%s is not valid", id)
	}
	for _, s := range []string{"", "B1", "../../etc/passwd", "01JB0000000000000000000000X", "01jb0000000000000000000000", "01JB000000000000000000000I", "81JB0000000000000000000000", "01JB00000000000000000000/0"} {
		if Valid(s) {
			t.Errorf("%q accepted", s)
		}
	}
}
