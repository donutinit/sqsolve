// SPDX-License-Identifier: AGPL-3.0-only

package compat

import "testing"

func TestRegistry(t *testing.T) {
	hashes := Hashes()
	if len(hashes) == 0 {
		t.Fatal("empty registry")
	}
	for _, h := range hashes {
		k, ok := Lookup(h)
		if !ok || k.Resolve == "" || k.AppVersion == "" || k.ProjectVersion == 0 || k.Checked == "" {
			t.Errorf("%s: incomplete entry %+v", h, k)
		}
	}
	if _, ok := Lookup("sha256:nope"); ok {
		t.Error("unknown hash found")
	}
	if _, ok := Lookup(""); ok {
		t.Error("empty hash found")
	}
	k, _ := Lookup("sha256:2bf9e02acd6f8389b240fddf0cb312b8a7b6a72dff48d8101609e6a80f150fee")
	if !k.Supports("drp") || !k.Supports("drx") || k.Supports("stills") {
		t.Errorf("verified list wrong: %v", k.Verified)
	}
}
