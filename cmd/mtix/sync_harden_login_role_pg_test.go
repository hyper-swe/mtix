// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// scramVerifier returns the SCRAM-SHA-256 verifier of password, the form
// in which a role password is sent to the server so the plaintext never
// reaches it (directive SQL Rule 1a).
func scramVerifier(t *testing.T, password string) string {
	t.Helper()
	const iterations = 4096
	salt := make([]byte, 16)
	_, err := rand.Read(salt)
	require.NoError(t, err)
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	require.NoError(t, err)
	mac := func(key []byte, msg string) []byte {
		h := hmac.New(sha256.New, key)
		_, err := h.Write([]byte(msg))
		require.NoError(t, err)
		return h.Sum(nil)
	}
	stored := sha256.Sum256(mac(salted, "Client Key"))
	b64 := base64.StdEncoding.EncodeToString
	return "SCRAM-SHA-256$4096:" + b64(salt) + "$" + b64(stored[:]) + ":" + b64(mac(salted, "Server Key"))
}

// loginRoleDSN makes role a login role with a random password and returns
// the fixture database's DSN for it.
func loginRoleDSN(t *testing.T, f *hardenFixture, role string) string {
	t.Helper()
	password := hardenRandomHex(t, 12)
	f.ddl("ALTER ROLE %I LOGIN PASSWORD %L", role, scramVerifier(t, password))
	u := f.dbURL
	u.User = url.UserPassword(role, password)
	return u.String()
}
