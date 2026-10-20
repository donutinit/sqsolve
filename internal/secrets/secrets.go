// SPDX-License-Identifier: AGPL-3.0-only

// Package secrets handles secret files, random credentials and the
// encryption of secrets that sqsolve must be able to show again.
package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

// String holds a secret value and never prints it.
type String struct{ v string }

// New wraps a value.
func New(v string) String { return String{v} }

// Reveal returns the secret value. Call sites are easy to audit with grep.
func (s String) Reveal() string { return s.v }

// Empty reports whether the secret is unset.
func (s String) Empty() bool { return s.v == "" }

func (s String) String() string { return "[redacted]" }

// GoString hides the value from %#v.
func (s String) GoString() string { return "[redacted]" }

// LogValue hides the value from slog.
func (s String) LogValue() slog.Value { return slog.StringValue("[redacted]") }

// MarshalJSON hides the value from JSON output.
func (s String) MarshalJSON() ([]byte, error) { return json.Marshal("[redacted]") }

// Password alphabet: no symbols and no look-alike characters.
const passwordAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

// GeneratePassword returns a random password of n characters.
func GeneratePassword(n int) String {
	return New(randomFrom(passwordAlphabet, n))
}

func randomFrom(alphabet string, n int) string {
	out := make([]byte, n)
	max := 256 - (256 % len(alphabet))
	buf := make([]byte, 1)
	for i := 0; i < n; {
		if _, err := rand.Read(buf); err != nil {
			panic(err)
		}
		if int(buf[0]) >= max {
			continue
		}
		out[i] = alphabet[int(buf[0])%len(alphabet)]
		i++
	}
	return string(out)
}

// RandomBytes returns n random bytes.
func RandomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// RandomToken returns a URL-safe random token with n bytes of entropy.
func RandomToken(n int) string {
	return base64.RawURLEncoding.EncodeToString(RandomBytes(n))
}

// ErrPermissions is returned for secret files readable by others.
var ErrPermissions = errors.New("SQS-C010 permissions too open on a secrets file")

// ReadFile reads a secret file and rejects files that others can read or
// anyone can write.
func ReadFile(path string) (String, error) {
	st, err := os.Stat(path)
	if err != nil {
		return String{}, err
	}
	if st.Mode().Perm()&0o037 != 0 {
		return String{}, fmt.Errorf("%w: %s has mode %o", ErrPermissions, path, st.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return String{}, err
	}
	return New(strings.TrimRight(string(b), "\r\n")), nil
}

// Box encrypts and decrypts recoverable secrets with the master key.
type Box struct {
	keyID string
	key   []byte
}

// NewBox builds a Box from a 32-byte key.
func NewBox(key []byte) (*Box, error) {
	if len(key) != chacha20poly1305.KeySize {
		return nil, fmt.Errorf("SQS-C011 the master key must be %d bytes, it has %d", chacha20poly1305.KeySize, len(key))
	}
	id := base64.RawURLEncoding.EncodeToString(key[:4])
	return &Box{keyID: id, key: append([]byte(nil), key...)}, nil
}

// Seal encrypts plaintext. aad binds the value to its place (table, column,
// row) so it cannot be moved elsewhere.
func (b *Box) Seal(plaintext String, aad string) (string, error) {
	aead, err := chacha20poly1305.NewX(b.key)
	if err != nil {
		return "", err
	}
	nonce := RandomBytes(aead.NonceSize())
	ct := aead.Seal(nil, nonce, []byte(plaintext.Reveal()), []byte(b.keyID+"|"+aad))
	return "v1:" + b.keyID + ":" + base64.RawStdEncoding.EncodeToString(append(nonce, ct...)), nil
}

// ErrDecrypt means the value cannot be decrypted with this key and context.
var ErrDecrypt = errors.New("SQS-C012 could not decrypt the secret")

// Open decrypts a value produced by Seal.
func (b *Box) Open(sealed, aad string) (String, error) {
	parts := strings.SplitN(sealed, ":", 3)
	if len(parts) != 3 || parts[0] != "v1" {
		return String{}, ErrDecrypt
	}
	if parts[1] != b.keyID {
		return String{}, fmt.Errorf("%w: encrypted with another master key", ErrDecrypt)
	}
	raw, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return String{}, ErrDecrypt
	}
	aead, err := chacha20poly1305.NewX(b.key)
	if err != nil {
		return String{}, err
	}
	if len(raw) < aead.NonceSize() {
		return String{}, ErrDecrypt
	}
	pt, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte(parts[1]+"|"+aad))
	if err != nil {
		return String{}, ErrDecrypt
	}
	return New(string(pt)), nil
}

// KeyID identifies the master key without revealing it.
func (b *Box) KeyID() string { return b.keyID }
