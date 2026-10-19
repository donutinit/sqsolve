// SPDX-License-Identifier: AGPL-3.0-or-later

package pg

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"fmt"

	"github.com/donutinit/sqsolve/internal/secrets"
)

const scramIterations = 4096

// ScramVerifier computes the SCRAM-SHA-256 verifier PostgreSQL stores, so the
// plain password never travels in a CREATE or ALTER ROLE statement.
func ScramVerifier(password secrets.String) (string, error) {
	return scramVerifier(password.Reveal(), secrets.RandomBytes(16), scramIterations)
}

func scramVerifier(password string, salt []byte, iterations int) (string, error) {
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return "", err
	}
	clientKey := hmacSHA256(salted, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	serverKey := hmacSHA256(salted, "Server Key")
	b64 := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", iterations, b64(salt), b64(storedKey[:]), b64(serverKey)), nil
}

func hmacSHA256(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}
