// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
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

// argonKey is Argon2id; a variable so tests can observe how many run at once
// without paying 64 MiB for each.
var argonKey = argon2.IDKey

// HashSlots bounds how many Argon2id computations run at once in the whole
// process. Each one takes 64 MiB, so without a cap a burst of anonymous
// logins could exhaust the memory of the NAS (ADR 0025).
const HashSlots = 4

// hashSlots is the process-wide semaphore; hashWait is how long a request
// waits for a free slot before it is told to try again.
var (
	hashSlots = make(chan struct{}, HashSlots)
	hashWait  = 2 * time.Second
)

// ErrBusy is returned when every hashing slot stayed taken for hashWait. The
// panel answers 503 with Retry-After; nothing was checked, so the attempt
// reveals nothing about the password.
var ErrBusy = errors.New("SQS-A050 the server is checking too many passwords at once; try again in a few seconds")

func acquireHash(ctx context.Context) error {
	select {
	case hashSlots <- struct{}{}:
		return nil
	default:
	}
	t := time.NewTimer(hashWait)
	defer t.Stop()
	select {
	case hashSlots <- struct{}{}:
		return nil
	case <-t.C:
		return ErrBusy
	case <-ctx.Done():
		return ErrBusy
	}
}

func releaseHash() { <-hashSlots }

// HashPassword returns a PHC-formatted Argon2id hash. It takes one of the
// HashSlots and returns ErrBusy if none frees up in time.
func HashPassword(ctx context.Context, pw string) (string, error) {
	if err := acquireHash(ctx); err != nil {
		return "", err
	}
	defer releaseHash()
	return hashPassword(pw), nil
}

func hashPassword(pw string) string {
	salt := secrets.RandomBytes(16)
	key := argonKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding.EncodeToString
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads, b64(salt), b64(key))
}

// CheckPassword compares pw with a stored hash in constant time. Like
// HashPassword, it runs inside one of the HashSlots and returns ErrBusy if
// none frees up in time.
func CheckPassword(ctx context.Context, hash, pw string) (bool, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, nil
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, nil
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, nil
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, nil
	}
	if err := acquireHash(ctx); err != nil {
		return false, err
	}
	defer releaseHash()
	got := argonKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash makes failed logins for unknown users take as long as real ones.
// It is computed once at start, outside the slots.
var dummyHash = hashPassword("sqsolve-dummy-password-for-timing")

// ErrWeakPassword describes why a password is rejected.
var ErrWeakPassword = errors.New("SQS-A010 weak password")

//go:embed common_passwords.txt
var commonList string

// common holds the offline list of passwords that must never be used, in
// lower case (docs 08 §4). No external lookups.
var common = map[string]bool{}

func init() {
	for _, line := range strings.Split(commonList, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, p := range strings.Fields(line) {
			common[strings.ToLower(p)] = true
		}
	}
}

// MinPasswordLength is the policy minimum (ADR 0021); the panel forms use the
// same number in their minlength attribute.
const MinPasswordLength = 8

// ValidatePassword enforces the minimum policy.
func ValidatePassword(pw, username string) error {
	if utf8.RuneCountInString(pw) < MinPasswordLength {
		return fmt.Errorf("%w: use at least %d characters", ErrWeakPassword, MinPasswordLength)
	}
	if utf8.RuneCountInString(pw) > 256 {
		return fmt.Errorf("%w: at most 256 characters", ErrWeakPassword)
	}
	low := strings.ToLower(pw)
	if common[low] {
		return fmt.Errorf("%w: it is in the list of common passwords", ErrWeakPassword)
	}
	if username != "" && strings.Contains(low, strings.ToLower(username)) {
		return fmt.Errorf("%w: cannot contain the username", ErrWeakPassword)
	}
	distinct := map[rune]bool{}
	for _, r := range pw {
		distinct[r] = true
	}
	if len(distinct) < 4 {
		return fmt.Errorf("%w: has too few distinct characters", ErrWeakPassword)
	}
	return nil
}
