// SPDX-License-Identifier: AGPL-3.0-only

// Package compat holds the registry of known schema fingerprints (docs 07 §9,
// docs 14 §3). A fingerprint is the hash of a library's table, column and
// type names; the registry stores only the hash and what was verified against
// it, never the schema itself.
package compat

import (
	_ "embed"
	"encoding/json"
	"sort"
	"sync"
)

//go:embed fingerprints.json
var fingerprintsJSON []byte

// Known describes a verified schema fingerprint.
type Known struct {
	// Resolve is the version that created or last upgraded the schema.
	Resolve string `json:"resolve"`
	// AppVersion is the DbAppVer string Resolve writes in exported files.
	AppVersion string `json:"app_version"`
	// ProjectVersion is the DbPrjVer of projects in this schema.
	ProjectVersion int `json:"project_version"`
	// Verified lists what was checked against real Resolve output: "drx", "drp".
	Verified []string `json:"verified"`
	// Checked is the date of the last verification.
	Checked string `json:"checked"`
}

// Verified reports whether what was verified for this fingerprint.
func (k Known) Supports(what string) bool {
	for _, v := range k.Verified {
		if v == what {
			return true
		}
	}
	return false
}

var load = sync.OnceValues(func() (map[string]Known, error) {
	var m map[string]Known
	if err := json.Unmarshal(fingerprintsJSON, &m); err != nil {
		return nil, err
	}
	return m, nil
})

// Lookup returns the registry entry of a fingerprint hash ("sha256:...").
func Lookup(hash string) (Known, bool) {
	m, err := load()
	if err != nil || hash == "" {
		return Known{}, false
	}
	k, ok := m[hash]
	return k, ok
}

// Hashes lists the registered fingerprints, sorted.
func Hashes() []string {
	m, _ := load()
	out := make([]string, 0, len(m))
	for h := range m {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}
