// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/donutinit/sqsolve/internal/secrets"
)

// TOTP parameters (RFC 6238 defaults that every authenticator app supports).
const (
	totpPeriod = 30
	totpDigits = 6
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random base32 secret.
func NewTOTPSecret() string { return b32.EncodeToString(secrets.RandomBytes(20)) }

// TOTPCode computes the code for a counter.
func TOTPCode(secret string, counter uint64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil {
		return "", err
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := (binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff) % 1000000
	return fmt.Sprintf("%0*d", totpDigits, v), nil
}

// Counter for a time.
func Counter(t time.Time) uint64 { return uint64(t.Unix()) / totpPeriod }

// VerifyTOTP checks code against now ±1 step. It returns the matched counter
// so callers can reject reuse.
func VerifyTOTP(secret, code string, now time.Time) (uint64, bool) {
	code = strings.TrimSpace(strings.ReplaceAll(code, " ", ""))
	if len(code) != totpDigits {
		return 0, false
	}
	c := Counter(now)
	for _, d := range []int64{0, -1, 1} {
		n := uint64(int64(c) + d)
		want, err := TOTPCode(secret, n)
		if err == nil && subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return n, true
		}
	}
	return 0, false
}

// OTPAuthURI is what authenticator apps import.
func OTPAuthURI(secret, account, issuer string) string {
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("period", fmt.Sprint(totpPeriod))
	v.Set("digits", fmt.Sprint(totpDigits))
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + v.Encode()
}
