// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIP returns the address of the caller: the TCP peer, or, when the
// peer is one of the trusted proxies (SQSOLVE_TRUSTED_PROXIES), the right-most
// X-Forwarded-For address that is not itself a trusted proxy. Entries a client
// prepends are never reached, and a value that is not an address is not
// believed. The panel and the API both use it, so the rate limiter, the audit
// log and the sessions see the same address (docs 08 §7).
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	if !isTrusted(peer, trusted) {
		return peer.String()
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	client := peer
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return peer.String()
		}
		client = a.Unmap()
		if !isTrusted(client, trusted) {
			break
		}
	}
	return client.String()
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
