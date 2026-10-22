// SPDX-License-Identifier: AGPL-3.0-only

package pg

import (
	"net/netip"
	"strings"
	"testing"
)

// What ParseCIDR accepts is written into pg_hba.conf: it must be a valid,
// masked prefix with no spaces, and parsing it again gives the same text.
func FuzzParseCIDR(f *testing.F) {
	for _, s := range []string{"192.168.1.0/24", "10.0.0.7", "2001:db8::/32", "fe80::1%eth0", " 10.1.2.3/8 ", "1.2.3.4/33", "x\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out, err := ParseCIDR(s)
		if err != nil {
			return
		}
		p, perr := netip.ParsePrefix(out)
		if perr != nil || p != p.Masked() || strings.ContainsAny(out, " \t\r\n#") {
			t.Fatalf("%q gave %q (%v)", s, out, perr)
		}
		if again, err := ParseCIDR(out); err != nil || again != out {
			t.Fatalf("%q is not stable: %q %v", out, again, err)
		}
	})
}
