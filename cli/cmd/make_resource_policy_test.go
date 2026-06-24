package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMakeResource_GeneratesFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "resources")

	c := NewMakeResource(nil)
	c.SetArgs([]string{"User", "--dir", out, "--fields", "name:string,email:string"})
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute: %v\noutput: %s", err, buf.String())
	}

	path := filepath.Join(out, "user_resource.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated resource: %v", err)
	}
	body := string(data)
	for _, want := range []string{
		"package resources",
		"var UserResource = resource.Func",
		"m.ID",
		`"name"`,
		"m.Name",
		`"email"`,
		"m.Email",
		"github.com/devituz/lagodev/resource",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("resource missing %q:\n%s", want, body)
		}
	}
}

func TestMakeResource_AppendsSuffix(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "resources")

	c := NewMakeResource(nil)
	c.SetArgs([]string{"PostResource", "--dir", out, "--model", "Post"})
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute: %v\noutput: %s", err, buf.String())
	}

	if _, err := os.Stat(filepath.Join(out, "post_resource.go")); err != nil {
		t.Fatalf("expected post_resource.go: %v", err)
	}
}

func TestMakePolicy_GeneratesFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "policies")

	c := NewMakePolicy(nil)
	c.SetArgs([]string{"Post", "--dir", out, "--model", "Post"})
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute: %v\noutput: %s", err, buf.String())
	}

	path := filepath.Join(out, "post_policy.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated policy: %v", err)
	}
	body := string(data)
	for _, want := range []string{
		"package policies",
		"type PostPolicy struct{}",
		"type User = any",
		"func (PostPolicy) ViewAny(",
		"func (PostPolicy) View(",
		"func (PostPolicy) Create(",
		"func (PostPolicy) Update(",
		"func (PostPolicy) Delete(",
		"_ Post) bool",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("policy missing %q:\n%s", want, body)
		}
	}
}

func TestMakePolicy_CustomUserType(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "policies")

	c := NewMakePolicy(nil)
	c.SetArgs([]string{"OrderPolicy", "--dir", out, "--model", "Order", "--user", "Account"})
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute: %v\noutput: %s", err, buf.String())
	}

	data, err := os.ReadFile(filepath.Join(out, "order_policy.go"))
	if err != nil {
		t.Fatalf("read generated policy: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "type Account = any") {
		t.Fatalf("expected custom user type alias:\n%s", body)
	}
	if !strings.Contains(body, "_ Account, _ Order) bool") {
		t.Fatalf("expected receivers using custom user type:\n%s", body)
	}
}
