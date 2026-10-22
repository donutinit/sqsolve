// SPDX-License-Identifier: AGPL-3.0-only

package secrets

import "testing"

// A sealed value opens with its additional data and with nothing else, and
// a changed sealed text never opens.
func FuzzBoxRoundTrip(f *testing.F) {
	f.Add("the password", "library_credentials.password_enc.01X", 3)
	f.Add("", "", 0)
	b, _ := NewBox(RandomBytes(32))
	f.Fuzz(func(t *testing.T, plain, aad string, flip int) {
		sealed, err := b.Seal(New(plain), aad)
		if err != nil {
			t.Fatal(err)
		}
		got, err := b.Open(sealed, aad)
		if err != nil || got.Reveal() != plain {
			t.Fatalf("round trip: %v", err)
		}
		if _, err := b.Open(sealed, aad+"x"); err == nil {
			t.Fatal("opened with other additional data")
		}
		// Not the last character: its low bits can be base64 padding.
		i := len(sealed) - 3 - int(uint(flip)%8)
		c := byte('A')
		if sealed[i] == 'A' {
			c = 'B'
		}
		if _, err := b.Open(sealed[:i]+string(c)+sealed[i+1:], aad); err == nil {
			t.Fatal("a changed sealed text opened")
		}
	})
}
