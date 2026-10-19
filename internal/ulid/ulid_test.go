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
