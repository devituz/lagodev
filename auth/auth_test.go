package auth

import (
	"testing"
	"time"
)

func newManager(t *testing.T) *Manager {
	t.Helper()
	m, err := New(Config{
		Secret:     "test-secret-do-not-use-in-prod-32bytes!!",
		Issuer:     "test",
		AccessTTL:  time.Minute,
		RefreshTTL: time.Hour,
		BcryptCost: 4, // Lowest cost so tests run fast.
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

func TestIssueAndParseAccess(t *testing.T) {
	m := newManager(t)
	token, exp, err := m.IssueAccess(42, "admin")
	if err != nil {
		t.Fatalf("IssueAccess: %v", err)
	}
	if exp.Before(time.Now()) {
		t.Fatalf("expiry already past: %v", exp)
	}

	claims, err := m.Parse(token)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if claims.UserID != 42 {
		t.Fatalf("UserID = %d, want 42", claims.UserID)
	}
	if claims.Role != "admin" {
		t.Fatalf("Role = %q, want admin", claims.Role)
	}
	if claims.Type != TokenAccess {
		t.Fatalf("Type = %q, want access", claims.Type)
	}
}

func TestParseRejectsBadSignature(t *testing.T) {
	m1 := newManager(t)
	m2, _ := New(Config{Secret: "another-secret-totally-different-key!!", AccessTTL: time.Minute})

	token, _, _ := m1.IssueAccess(1, "user")
	if _, err := m2.Parse(token); err != ErrInvalidToken {
		t.Fatalf("Parse with wrong secret = %v, want ErrInvalidToken", err)
	}
}

func TestParseRejectsExpired(t *testing.T) {
	m := newManager(t)
	// Issue() lets us pass a negative TTL directly so we don't need to sleep.
	token, _, err := m.Issue(1, "user", TokenAccess, -time.Second)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := m.Parse(token); err != ErrExpiredToken {
		t.Fatalf("Parse expired = %v, want ErrExpiredToken", err)
	}
}

func TestPasswordHashing(t *testing.T) {
	m := newManager(t)
	hash, err := m.HashPassword("hunter2")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !m.VerifyPassword(hash, "hunter2") {
		t.Fatalf("VerifyPassword: correct password rejected")
	}
	if m.VerifyPassword(hash, "wrong") {
		t.Fatalf("VerifyPassword: wrong password accepted")
	}
}

func TestNewRequiresSecret(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatalf("New with empty secret should fail")
	}
}
