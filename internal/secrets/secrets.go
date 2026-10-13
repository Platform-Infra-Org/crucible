// Package secrets encrypts the credentials people hand Crucible at run time — a repository's token or key — so that
// a database dump, a backup or a read replica holds only ciphertext. The key never lives in the database: it comes
// from CRUCIBLE_SECRET_KEY. The Helm chart reads it from the crucible-secrets Secret, where whoever runs the chart
// puts it (unset, credentials are refused). On AWS, `crucible aws init` generates it into the persistent stack as the
// SSM parameter /<name>/secret_key, so it survives teardown, and deploy copies it into that Secret. The local compose
// stack sets a fixed development key.
//
// Lose or change the key and no credential can be read back: the repositories that used them go unreachable and are
// reconnected. Nothing else in the instance depends on it.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Box seals and opens one instance's secrets. The zero Box is unusable; New reports why.
type Box struct{ aead cipher.AEAD }

// prefix marks what this package wrote, so a future scheme (a secret manager reference, a rotated key) is told apart
// from today's ciphertext by looking at the stored value.
const prefix = "v1:"

// New derives the key from the passphrase in CRUCIBLE_SECRET_KEY. Any length is accepted and hashed to 32 bytes, so
// an operator can use a long random string without worrying about key sizes; a short one is refused because it is
// guessable, not because AES minds.
func New(passphrase string) (*Box, error) {
	if len(strings.TrimSpace(passphrase)) < 16 {
		return nil, errors.New("CRUCIBLE_SECRET_KEY must be at least 16 characters: it protects the credentials of every connected repository")
	}
	sum := sha256.Sum256([]byte(passphrase))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plain and returns a storable string. Sealing the same value twice gives different output (a fresh
// nonce each time), so nobody can tell from the database that two repositories share a credential.
func (b *Box) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := b.aead.Seal(nonce, nonce, []byte(plain), nil)
	return prefix + base64.StdEncoding.EncodeToString(out), nil
}

// Open reverses Seal. A value sealed with a different key fails to authenticate, so a wrong or rotated
// CRUCIBLE_SECRET_KEY is an error rather than silent nonsense.
func (b *Box) Open(stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	raw, ok := strings.CutPrefix(stored, prefix)
	if !ok {
		return "", fmt.Errorf("stored secret is not %s", prefix)
	}
	blob, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", fmt.Errorf("stored secret is not valid base64: %w", err)
	}
	n := b.aead.NonceSize()
	if len(blob) < n {
		return "", errors.New("stored secret is too short to hold a nonce")
	}
	plain, err := b.aead.Open(nil, blob[:n], blob[n:], nil)
	if err != nil {
		return "", errors.New("this secret cannot be read with the current CRUCIBLE_SECRET_KEY: it was sealed with a different one")
	}
	return string(plain), nil
}
