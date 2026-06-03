package crypt

import (
	"errors"
	"strings"
	"testing"
)

func mustKey(t *testing.T) []byte {
	t.Helper()
	k, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return k
}

func TestGenerateKey_Length(t *testing.T) {
	k := mustKey(t)
	if len(k) != KeySize {
		t.Fatalf("want %d bytes, got %d", KeySize, len(k))
	}
}

func TestGenerateKeyString_HasPrefixAndDecodes(t *testing.T) {
	s, err := GenerateKeyString()
	if err != nil {
		t.Fatalf("GenerateKeyString: %v", err)
	}
	if !strings.HasPrefix(s, "base64:") {
		t.Fatalf("expected base64: prefix, got %q", s)
	}
	k, err := DecodeKey(s)
	if err != nil || len(k) != KeySize {
		t.Fatalf("DecodeKey(%q) = %v, %v", s, k, err)
	}
}

func TestDecodeKey_Errors(t *testing.T) {
	cases := []struct {
		in   string
		want error
	}{
		{"", ErrInvalidKey},
		{"too-short", ErrInvalidKey},
		{"base64:!!!not-base64", nil}, // base64 decode error (not ErrInvalidKey)
		{"base64:" + "AAAA", ErrInvalidKey},
	}
	for _, c := range cases {
		_, err := DecodeKey(c.in)
		if err == nil {
			t.Fatalf("DecodeKey(%q) must fail", c.in)
		}
		if c.want != nil && !errors.Is(err, c.want) {
			t.Fatalf("DecodeKey(%q) = %v, want %v", c.in, err, c.want)
		}
	}
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	key := mustKey(t)
	plain := "the quick brown fox jumps over the lazy dog"
	ct, err := EncryptString(key, plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if ct == plain {
		t.Fatal("ciphertext must not equal plaintext")
	}
	out, err := DecryptString(key, ct)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if out != plain {
		t.Fatalf("roundtrip mismatch: %q", out)
	}
}

func TestEncrypt_NonceIsRandom(t *testing.T) {
	key := mustKey(t)
	a, _ := EncryptString(key, "same")
	b, _ := EncryptString(key, "same")
	if a == b {
		t.Fatal("two encryptions of the same plaintext must differ (random nonce)")
	}
}

func TestDecrypt_TamperingDetected(t *testing.T) {
	key := mustKey(t)
	ct, _ := EncryptString(key, "x")
	// Flip one character — base64 string must still parse but tag will fail
	tampered := ct[:len(ct)-2] + "AA"
	_, err := DecryptString(key, tampered)
	if !errors.Is(err, ErrCiphertextMalformed) {
		t.Fatalf("tampered ciphertext must fail, got %v", err)
	}
}

func TestDecrypt_WrongKeyFails(t *testing.T) {
	k1, k2 := mustKey(t), mustKey(t)
	ct, _ := EncryptString(k1, "secret")
	_, err := DecryptString(k2, ct)
	if !errors.Is(err, ErrCiphertextMalformed) {
		t.Fatalf("wrong key must fail, got %v", err)
	}
}

func TestEncrypt_InvalidKey(t *testing.T) {
	_, err := EncryptString([]byte("too-short"), "x")
	if !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("want ErrInvalidKey, got %v", err)
	}
}

func TestSignVerify(t *testing.T) {
	key := mustKey(t)
	data := []byte("user-id=42&exp=1234567890")
	sig := Sign(key, data)
	if sig == "" {
		t.Fatal("Sign returned empty")
	}
	if err := Verify(key, data, sig); err != nil {
		t.Fatalf("Verify valid: %v", err)
	}
	// Tampered data
	if err := Verify(key, []byte("user-id=43"), sig); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("tampered data must fail, got %v", err)
	}
	// Wrong key
	if err := Verify(mustKey(t), data, sig); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("wrong key must fail, got %v", err)
	}
}
