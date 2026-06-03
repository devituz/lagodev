// Package crypt provides symmetric encryption and HMAC signing helpers
// modelled on Laravel's Crypt facade.
//
// Two primitives are offered:
//
//   - Encrypt / Decrypt — authenticated AES-256-GCM encryption. The
//     ciphertext is base64-encoded and includes a random nonce, so the
//     same plaintext under the same key produces different output every
//     time.
//   - Sign / Verify — keyed HMAC-SHA256 of arbitrary bytes (use when
//     you only need integrity/authenticity, not confidentiality).
//
// Keys must be exactly 32 bytes. Generate one with `lago key:generate`
// or by reading 32 random bytes and base64-encoding them.
package crypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
)

// KeySize is the required length of an encryption key (AES-256).
const KeySize = 32

// ErrInvalidKey is returned when the key length is not KeySize.
var ErrInvalidKey = errors.New("crypt: key must be 32 bytes")

// ErrCiphertextMalformed indicates the input could not be decoded or is
// shorter than the GCM nonce.
var ErrCiphertextMalformed = errors.New("crypt: ciphertext malformed")

// ErrSignatureMismatch is returned by Verify when the signature does
// not match.
var ErrSignatureMismatch = errors.New("crypt: signature mismatch")

// GenerateKey returns 32 random bytes suitable for use as an encryption
// key. Persist the output (e.g. base64-encoded) in APP_KEY.
func GenerateKey() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("crypt: read random: %w", err)
	}
	return key, nil
}

// GenerateKeyString returns a base64 (std encoding) string of 32 random
// bytes — the format the `lago key:generate` command writes to .env.
func GenerateKeyString() (string, error) {
	k, err := GenerateKey()
	if err != nil {
		return "", err
	}
	return "base64:" + base64.StdEncoding.EncodeToString(k), nil
}

// DecodeKey accepts either raw 32 bytes (as a string) or the
// "base64:..." form written by GenerateKeyString. Empty / wrong-size
// keys return ErrInvalidKey.
func DecodeKey(s string) ([]byte, error) {
	if s == "" {
		return nil, ErrInvalidKey
	}
	if len(s) > 7 && s[:7] == "base64:" {
		b, err := base64.StdEncoding.DecodeString(s[7:])
		if err != nil {
			return nil, fmt.Errorf("crypt: decode base64 key: %w", err)
		}
		if len(b) != KeySize {
			return nil, ErrInvalidKey
		}
		return b, nil
	}
	if len(s) != KeySize {
		return nil, ErrInvalidKey
	}
	return []byte(s), nil
}

// Encrypt seals plaintext with AES-256-GCM and returns a base64 string
// of (nonce || ciphertext || tag). Each call uses a fresh random
// nonce, so the same plaintext+key yields different output every time.
func Encrypt(key, plaintext []byte) (string, error) {
	if len(key) != KeySize {
		return "", ErrInvalidKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("crypt: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("crypt: new gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("crypt: read nonce: %w", err)
	}
	ct := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

// EncryptString is a convenience for string plaintext.
func EncryptString(key []byte, s string) (string, error) {
	return Encrypt(key, []byte(s))
}

// Decrypt reverses Encrypt. It verifies the GCM tag and returns the
// original plaintext. Any tampering causes ErrCiphertextMalformed.
func Decrypt(key []byte, encoded string) ([]byte, error) {
	if len(key) != KeySize {
		return nil, ErrInvalidKey
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, ErrCiphertextMalformed
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypt: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypt: new gcm: %w", err)
	}
	ns := gcm.NonceSize()
	if len(data) < ns {
		return nil, ErrCiphertextMalformed
	}
	nonce, ct := data[:ns], data[ns:]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, ErrCiphertextMalformed
	}
	return plain, nil
}

// DecryptString is a convenience that returns the plaintext as string.
func DecryptString(key []byte, encoded string) (string, error) {
	b, err := Decrypt(key, encoded)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Sign computes HMAC-SHA256 of data with the given key and returns
// the lowercase hex signature. Useful for stateless tokens like
// password-reset links or signed URLs.
func Sign(key, data []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify checks signature against the HMAC of data using key.
// Comparison is constant-time.
func Verify(key, data []byte, signature string) error {
	expected := Sign(key, data)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) != 1 {
		return ErrSignatureMismatch
	}
	return nil
}
