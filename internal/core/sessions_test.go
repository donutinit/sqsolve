// SPDX-License-Identifier: AGPL-3.0-only

package core_test

import "testing"

// TestSessionsShowEncryption checks that the session list carries whether
// each connection is encrypted (pg_stat_ssl), the fact an administrator
// checks before turning on "Solo TLS".
func TestSessionsShowEncryption(t *testing.T) {
	e := newEnv(t)
	l, pass := e.createActive("cifrado")
	conn := e.connect(l.OwnerRole, pass, l.DBName)
	defer conn.Close(e.ctx)
	ss, err := e.svc.Sessions(e.ctx, l.ID)
	if err != nil || len(ss) == 0 {
		t.Fatalf("sessions: %v %+v", err, ss)
	}
	for _, s := range ss {
		if s.SSL != (s.TLSVersion != "") {
			t.Fatalf("inconsistent encryption fields: %+v", s)
		}
		if s.ClientAddr == "" {
			t.Fatalf("session without address: %+v", s)
		}
	}
}
