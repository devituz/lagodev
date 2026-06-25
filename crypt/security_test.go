package crypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// gcmParams returns the nonce size and tag overhead for AES-256-GCM so the
// tamper tests can target each region of the (nonce || ciphertext || tag)
// layout precisely.
func gcmParams(t *testing.T) (nonceSize, overhead int) {
	t.Helper()
	block, err := aes.NewCipher(make([]byte, KeySize))
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("cipher.NewGCM: %v", err)
	}
	return gcm.NonceSize(), gcm.Overhead()
}

// TestDecrypt_TamperEachRegion flips a single byte in every byte position of
// the encoded message (covering nonce, ciphertext, and tag regions) and
// asserts Decrypt never returns the original plaintext and always fails with
// ErrCiphertextMalformed. This is the core authenticated-encryption property:
// any modification is detected, never silently yields wrong plaintext.
func TestDecrypt_TamperEachRegion(t *testing.T) {
	key := mustKey(t)
	plain := []byte("attack at dawn — sensitive payload 0123456789")

	encoded, err := Encrypt(key, plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode own ciphertext: %v", err)
	}

	nonceSize, overhead := gcmParams(t)
	tagStart := len(raw) - overhead

	region := func(i int) string {
		switch {
		case i < nonceSize:
			return "nonce"
		case i >= tagStart:
			return "tag"
		default:
			return "ciphertext"
		}
	}

	for i := 0; i < len(raw); i++ {
		// Flip the lowest bit of byte i; a single-bit change in any region
		// must invalidate the GCM tag.
		tampered := make([]byte, len(raw))
		copy(tampered, raw)
		tampered[i] ^= 0x01
		enc := base64.StdEncoding.EncodeToString(tampered)

		out, err := Decrypt(key, enc)
		if err == nil {
			t.Fatalf("byte %d (%s): tampered ciphertext decrypted without error, got %q",
				i, region(i), out)
		}
		if !errors.Is(err, ErrCiphertextMalformed) {
			t.Fatalf("byte %d (%s): want ErrCiphertextMalformed, got %v",
				i, region(i), err)
		}
		if out != nil {
			t.Fatalf("byte %d (%s): plaintext must be nil on failure, got %q",
				i, region(i), out)
		}
		if bytes.Equal(out, plain) {
			t.Fatalf("byte %d (%s): returned original plaintext on tamper", i, region(i))
		}
	}
}

// TestEncrypt_NonceUniqueness encrypts the same plaintext+key many times and
// asserts every ciphertext (and thus every nonce) is unique. A repeated nonce
// in GCM is catastrophic, so this guards the random-nonce contract.
func TestEncrypt_NonceUniqueness(t *testing.T) {
	key := mustKey(t)
	const n = 256
	seen := make(map[string]struct{}, n)
	nonceSize, _ := gcmParams(t)

	for i := 0; i < n; i++ {
		enc, err := Encrypt(key, []byte("same plaintext"))
		if err != nil {
			t.Fatalf("Encrypt #%d: %v", i, err)
		}
		raw, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			t.Fatalf("decode #%d: %v", i, err)
		}
		if len(raw) < nonceSize {
			t.Fatalf("ciphertext #%d shorter than nonce", i)
		}
		nonce := string(raw[:nonceSize])
		if _, dup := seen[nonce]; dup {
			t.Fatalf("nonce reuse detected at iteration %d", i)
		}
		seen[nonce] = struct{}{}
	}
}

// TestDecrypt_WrongKeyClean confirms a different (valid-length) key fails
// cleanly with ErrCiphertextMalformed and never returns plaintext.
func TestDecrypt_WrongKeyClean(t *testing.T) {
	k1, k2 := mustKey(t), mustKey(t)
	enc, err := Encrypt(k1, []byte("top secret"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	out, err := Decrypt(k2, enc)
	if !errors.Is(err, ErrCiphertextMalformed) {
		t.Fatalf("wrong key: want ErrCiphertextMalformed, got %v", err)
	}
	if out != nil {
		t.Fatalf("wrong key: plaintext must be nil, got %q", out)
	}
}

// TestDecrypt_ShortAndEmpty ensures inputs shorter than nonce+tag, empty
// strings, and malformed base64 produce a clean error and never panic.
func TestDecrypt_ShortAndEmpty(t *testing.T) {
	key := mustKey(t)
	nonceSize, overhead := gcmParams(t)
	minValid := nonceSize + overhead

	b64 := func(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

	cases := []struct {
		name string
		in   string
	}{
		{"empty string", ""},
		{"zero bytes b64", b64(nil)},
		{"one byte", b64([]byte{0x00})},
		{"nonce minus one", b64(make([]byte, nonceSize-1))},
		{"exactly nonce, no tag", b64(make([]byte, nonceSize))},
		{"nonce plus partial tag", b64(make([]byte, nonceSize+1))},
		{"one short of min valid", b64(make([]byte, minValid-1))},
		{"all zero min-valid (bad tag)", b64(make([]byte, minValid))},
		{"not base64", "@@@not-base64@@@"},
		{"base64 with bad padding", "AAA"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := Decrypt(key, c.in)
			if err == nil {
				t.Fatalf("want error, got plaintext %q", out)
			}
			if !errors.Is(err, ErrCiphertextMalformed) {
				t.Fatalf("want ErrCiphertextMalformed, got %v", err)
			}
			if out != nil {
				t.Fatalf("plaintext must be nil on error, got %q", out)
			}
		})
	}
}

// TestGenerateKey_ProducesKeySize asserts GenerateKey yields exactly KeySize
// bytes and that two keys differ (i.e. they are random, not zeroed).
func TestGenerateKey_ProducesKeySize(t *testing.T) {
	a, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if len(a) != KeySize {
		t.Fatalf("want %d bytes, got %d", KeySize, len(a))
	}
	b, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two generated keys are identical — randomness broken")
	}
	if bytes.Equal(a, make([]byte, KeySize)) {
		t.Fatal("generated key is all zeroes")
	}
}

// TestDecodeKey_RejectsBadInput covers the validation contract: wrong-length
// raw keys and wrong-length base64 keys return ErrInvalidKey; invalid base64
// returns a (non-panicking) decode error; valid forms round-trip.
func TestDecodeKey_RejectsBadInput(t *testing.T) {
	validRaw := strings.Repeat("a", KeySize) // 32 bytes raw
	validB64 := "base64:" + base64.StdEncoding.EncodeToString(make([]byte, KeySize))

	t.Run("invalid", func(t *testing.T) {
		cases := []struct {
			name    string
			in      string
			wantErr error // nil = any error, but never ErrInvalidKey
		}{
			{"empty", "", ErrInvalidKey},
			{"raw too short", "short", ErrInvalidKey},
			{"raw too long", strings.Repeat("a", KeySize+1), ErrInvalidKey},
			{"base64 prefix only", "base64:", ErrInvalidKey},
			{"base64 wrong length", "base64:AAAA", ErrInvalidKey},
			{"base64 invalid", "base64:!!!not-base64", nil},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				k, err := DecodeKey(c.in)
				if err == nil {
					t.Fatalf("DecodeKey(%q) must fail, got key len %d", c.in, len(k))
				}
				if k != nil {
					t.Fatalf("DecodeKey(%q) must return nil key on error, got %v", c.in, k)
				}
				if c.wantErr != nil && !errors.Is(err, c.wantErr) {
					t.Fatalf("DecodeKey(%q) = %v, want %v", c.in, err, c.wantErr)
				}
				if c.wantErr == nil && errors.Is(err, ErrInvalidKey) {
					t.Fatalf("DecodeKey(%q): base64 decode error misreported as ErrInvalidKey", c.in)
				}
			})
		}
	})

	t.Run("valid raw", func(t *testing.T) {
		k, err := DecodeKey(validRaw)
		if err != nil || len(k) != KeySize {
			t.Fatalf("DecodeKey(raw) = %v, %v", k, err)
		}
	})
	t.Run("valid base64", func(t *testing.T) {
		k, err := DecodeKey(validB64)
		if err != nil || len(k) != KeySize {
			t.Fatalf("DecodeKey(base64) = %v, %v", k, err)
		}
	})
}

// TestEncrypt_BadKeyNoPanic asserts Encrypt with a wrong-length key returns
// ErrInvalidKey rather than panicking, for several bad lengths.
func TestEncrypt_BadKeyNoPanic(t *testing.T) {
	for _, n := range []int{0, 1, 15, 16, 24, 31, 33, 64} {
		key := make([]byte, n)
		out, err := Encrypt(key, []byte("x"))
		if !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("Encrypt with %d-byte key: want ErrInvalidKey, got %v", n, err)
		}
		if out != "" {
			t.Fatalf("Encrypt with %d-byte key: want empty output, got %q", n, out)
		}
	}
}

// TestDecrypt_BadKeyNoPanic asserts Decrypt with a wrong-length key returns
// ErrInvalidKey rather than panicking.
func TestDecrypt_BadKeyNoPanic(t *testing.T) {
	for _, n := range []int{0, 1, 15, 31, 33} {
		key := make([]byte, n)
		out, err := Decrypt(key, base64.StdEncoding.EncodeToString(make([]byte, 64)))
		if !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("Decrypt with %d-byte key: want ErrInvalidKey, got %v", n, err)
		}
		if out != nil {
			t.Fatalf("Decrypt with %d-byte key: want nil output, got %q", n, out)
		}
	}
}

// TestVerify_ConstantTimeUsesSubtle is a behavioural check that Verify rejects
// tampered messages and signatures, accepts the genuine one, and (per the
// constant-time contract) treats wrong-length signatures as a mismatch rather
// than panicking or short-circuiting.
func TestVerify_TamperAndLengths(t *testing.T) {
	key := mustKey(t)
	data := []byte("session=abc;role=user")
	sig := Sign(key, data)

	if err := Verify(key, data, sig); err != nil {
		t.Fatalf("genuine signature rejected: %v", err)
	}

	// Tampered message.
	if err := Verify(key, []byte("session=abc;role=admin"), sig); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("tampered message: want ErrSignatureMismatch, got %v", err)
	}

	// Tampered signature (flip first char).
	bad := []byte(sig)
	if bad[0] == 'A' {
		bad[0] = 'B'
	} else {
		bad[0] = 'A'
	}
	if err := Verify(key, data, string(bad)); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("tampered signature: want ErrSignatureMismatch, got %v", err)
	}

	// Garbage / wrong-length signatures must not panic.
	garbage := []string{"", "x", "not-a-signature", strings.Repeat("Z", 1000), sig + "extra"}
	for _, g := range garbage {
		if err := Verify(key, data, g); !errors.Is(err, ErrSignatureMismatch) {
			t.Fatalf("garbage signature %q: want ErrSignatureMismatch, got %v", g, err)
		}
	}
}

// FuzzDecrypt feeds arbitrary keys and encoded strings to Decrypt and asserts
// it never panics and never returns a non-nil plaintext together with a
// non-nil error. Run: go test -fuzz=FuzzDecrypt -fuzztime=15s ./crypt
func FuzzDecrypt(f *testing.F) {
	good := mustKeyF(f)
	enc, err := Encrypt(good, []byte("seed plaintext"))
	if err != nil {
		f.Fatalf("seed Encrypt: %v", err)
	}
	f.Add(good, enc)
	f.Add(good, "")
	f.Add(good, "not-base64!!!")
	f.Add([]byte("short-key"), enc)
	f.Add(make([]byte, KeySize), base64.StdEncoding.EncodeToString(make([]byte, 64)))
	f.Add([]byte(nil), "")

	f.Fuzz(func(t *testing.T, key []byte, encoded string) {
		out, err := Decrypt(key, encoded)
		if err != nil && out != nil {
			t.Fatalf("Decrypt returned both error %v and plaintext %q", err, out)
		}
		// If it succeeded, it must round-trip back to the same ciphertext
		// region is not asserted here; success only happens for valid inputs.
		_ = out
	})
}

// mustKeyF is the *testing.F counterpart of mustKey.
func mustKeyF(f *testing.F) []byte {
	f.Helper()
	k, err := GenerateKey()
	if err != nil {
		f.Fatalf("GenerateKey: %v", err)
	}
	return k
}
