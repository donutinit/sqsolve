// SPDX-License-Identifier: AGPL-3.0-only

// Package ulid generates lexicographically sortable identifiers (ULID).
package ulid

import (
	"crypto/rand"
	"encoding/binary"
	"strings"
	"sync"
	"time"
)

const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	mu       sync.Mutex
	lastMs   uint64
	lastRand [10]byte
)

// New returns a new ULID string. IDs generated in the same millisecond are
// monotonic.
func New() string {
	mu.Lock()
	defer mu.Unlock()
	ms := uint64(time.Now().UnixMilli())
	var r [10]byte
	if ms == lastMs {
		r = lastRand
		for i := len(r) - 1; i >= 0; i-- {
			r[i]++
			if r[i] != 0 {
				break
			}
		}
	} else {
		if _, err := rand.Read(r[:]); err != nil {
			panic(err)
		}
	}
	lastMs, lastRand = ms, r

	var b [16]byte
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], ms)
	copy(b[0:6], ts[2:8])
	copy(b[6:], r[:])
	return encode(b)
}

func encode(b [16]byte) string {
	out := make([]byte, 26)
	// 128 bits -> 26 base32 chars (first char holds 3 bits).
	var acc uint64
	bits := 0
	idx := 25
	for i := 15; i >= 0; i-- {
		acc |= uint64(b[i]) << bits
		bits += 8
		for bits >= 5 && idx >= 0 {
			out[idx] = alphabet[acc&31]
			acc >>= 5
			bits -= 5
			idx--
		}
	}
	if idx >= 0 {
		out[idx] = alphabet[acc&31]
	}
	return string(out)
}

// Short returns the last 8 lowercase characters of an ID, for auxiliary
// database names.
func Short(id string) string {
	if len(id) < 8 {
		return lower(id)
	}
	return lower(id[len(id)-8:])
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// Valid reports whether s is a ULID as New writes it: 26 characters of the
// Crockford alphabet in upper case, the first one at most 7.
func Valid(s string) bool {
	if len(s) != 26 || s[0] > '7' {
		return false
	}
	for i := range len(s) {
		if strings.IndexByte(alphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}
