package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runKeyGen(t *testing.T, args ...string) string {
	t.Helper()
	c := NewKeyGenerate(nil)
	c.SetArgs(args)
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute: %v\noutput: %s", err, buf.String())
	}
	return buf.String()
}

func TestKeyGenerate_PrintOnlyEmitsBase64Key(t *testing.T) {
	out := runKeyGen(t, "--print-only")
	if !strings.HasPrefix(strings.TrimSpace(out), "base64:") {
		t.Fatalf("expected base64: prefix in %q", out)
	}
}

func TestKeyGenerate_WritesToEnv(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	_ = runKeyGen(t, "--env", envPath)
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	if !strings.Contains(string(data), "APP_KEY=base64:") {
		t.Fatalf("APP_KEY missing in %q", data)
	}
}

func TestKeyGenerate_RefusesOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	_ = os.WriteFile(envPath, []byte("APP_KEY=base64:original\nDB_HOST=x\n"), 0o600)

	c := NewKeyGenerate(nil)
	c.SetArgs([]string{"--env", envPath})
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	err := c.Execute()
	if err == nil {
		t.Fatal("must fail without --force")
	}
	data, _ := os.ReadFile(envPath)
	if !strings.Contains(string(data), "base64:original") {
		t.Fatal("original APP_KEY must be preserved")
	}
}

func TestKeyGenerate_ForceReplaces(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	_ = os.WriteFile(envPath, []byte("APP_KEY=base64:original\nDB_HOST=x\n"), 0o600)

	_ = runKeyGen(t, "--env", envPath, "--force")
	data, _ := os.ReadFile(envPath)
	if strings.Contains(string(data), "base64:original") {
		t.Fatal("APP_KEY must be replaced under --force")
	}
	if !strings.Contains(string(data), "DB_HOST=x") {
		t.Fatal("other lines must be preserved")
	}
	if !strings.Contains(string(data), "APP_KEY=base64:") {
		t.Fatal("new APP_KEY missing")
	}
}

func TestKeyGenerate_EmptyAppKeyAcceptsWriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	_ = os.WriteFile(envPath, []byte("APP_KEY=\nDB_HOST=x\n"), 0o600)

	_ = runKeyGen(t, "--env", envPath)
	data, _ := os.ReadFile(envPath)
	if !strings.Contains(string(data), "APP_KEY=base64:") {
		t.Fatalf("empty APP_KEY must be filled in without --force, got %q", data)
	}
}
