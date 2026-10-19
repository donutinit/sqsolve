// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"github.com/donutinit/sqsolve/internal/secrets"
)

// Argon2id parameters (docs 08 §4). They travel inside each hash, so they can
// be raised later without breaking old hashes.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

// HashPassword returns a PHC-formatted Argon2id hash.
func HashPassword(pw string) string {
	salt := secrets.RandomBytes(16)
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding.EncodeToString
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads, b64(salt), b64(key))
}

// CheckPassword compares pw with a stored hash in constant time.
func CheckPassword(hash, pw string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash makes failed logins for unknown users take as long as real ones.
var dummyHash = HashPassword("sqsolve-dummy-password-for-timing")

// ErrWeakPassword describes why a password is rejected.
var ErrWeakPassword = errors.New("SQS-A010 contraseña débil")

// common holds a small offline list of passwords that must never be used.
var common = map[string]bool{}

func init() {
	for _, p := range strings.Fields(`123456789012 password1234 qwertyuiop12 contraseña12 contrasena123 administrator
		123412341234 adminadmin12 passwordpassword iloveyou1234 qwerty123456 1q2w3e4r5t6y abcdefghijkl 111111111111
		000000000000 davinciresolve davinci12345 resolve12345 postgres1234 password123! administrador
		holaholahola welcome12345 letmein12345 changeme1234 sqsolve12345 blackmagic12 trustno11234 monkey123456
		dragon123456 football1234 baseball1234 superman1234 starwars1234 princess1234 sunshine1234`) {
		common[strings.ToLower(p)] = true
	}
}

// ValidatePassword enforces the minimum policy.
func ValidatePassword(pw, username string) error {
	if utf8.RuneCountInString(pw) < 12 {
		return fmt.Errorf("%w: usa al menos 12 caracteres", ErrWeakPassword)
	}
	if utf8.RuneCountInString(pw) > 256 {
		return fmt.Errorf("%w: máximo 256 caracteres", ErrWeakPassword)
	}
	low := strings.ToLower(pw)
	if common[low] {
		return fmt.Errorf("%w: está en la lista de contraseñas comunes", ErrWeakPassword)
	}
	if username != "" && strings.Contains(low, strings.ToLower(username)) {
		return fmt.Errorf("%w: no puede contener el nombre de usuario", ErrWeakPassword)
	}
	distinct := map[rune]bool{}
	for _, r := range pw {
		distinct[r] = true
	}
	if len(distinct) < 5 {
		return fmt.Errorf("%w: tiene muy pocos caracteres distintos", ErrWeakPassword)
	}
	return nil
}
