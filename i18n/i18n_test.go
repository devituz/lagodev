package i18n

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeLocale(t *testing.T, dir, locale, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, locale+".json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", locale, err)
	}
}

func TestLoad_Basic(t *testing.T) {
	dir := t.TempDir()
	writeLocale(t, dir, "en", `{"welcome": "Hello :name"}`)
	writeLocale(t, dir, "uz", `{"welcome": "Salom :name"}`)
	tr, err := Load(dir, "en")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := tr.Get("welcome", M{"name": "Ada"}); got != "Hello Ada" {
		t.Fatalf("Get = %q", got)
	}
	uz := tr.WithLocale("uz").Get("welcome", M{"name": "Ada"})
	if uz != "Salom Ada" {
		t.Fatalf("uz Get = %q", uz)
	}
}

func TestLoad_MissingDefaultReportsError(t *testing.T) {
	dir := t.TempDir()
	writeLocale(t, dir, "uz", `{"welcome": "Salom"}`)
	_, err := Load(dir, "en")
	if !errors.Is(err, ErrLocaleMissing) {
		t.Fatalf("want ErrLocaleMissing, got %v", err)
	}
}

func TestFallback_UsedWhenKeyMissing(t *testing.T) {
	dir := t.TempDir()
	writeLocale(t, dir, "en", `{"hello": "Hello", "footer": "Bye"}`)
	writeLocale(t, dir, "uz", `{"hello": "Salom"}`) // no "footer"
	tr, _ := Load(dir, "en")
	tr.SetFallback("en")
	got := tr.WithLocale("uz").Get("footer")
	if got != "Bye" {
		t.Fatalf("fallback Get = %q", got)
	}
}

func TestMissingKey_ReturnsKey(t *testing.T) {
	dir := t.TempDir()
	writeLocale(t, dir, "en", `{}`)
	tr, _ := Load(dir, "en")
	if got := tr.Get("totally.missing"); got != "totally.missing" {
		t.Fatalf("missing key should echo key, got %q", got)
	}
}

func TestNestedKeys_AreFlattened(t *testing.T) {
	dir := t.TempDir()
	writeLocale(t, dir, "en", `{
		"auth": {"failed": "Login failed", "throttle": "Too many attempts"},
		"hello": "Hi"
	}`)
	tr, _ := Load(dir, "en")
	if got := tr.Get("auth.failed"); got != "Login failed" {
		t.Fatalf("auth.failed = %q", got)
	}
	if got := tr.Get("auth.throttle"); got != "Too many attempts" {
		t.Fatalf("auth.throttle = %q", got)
	}
}

func TestChoice_PluralForms(t *testing.T) {
	dir := t.TempDir()
	writeLocale(t, dir, "en", `{
		"apples": {"zero": "no apples", "one": ":count apple", "other": ":count apples"}
	}`)
	tr, _ := Load(dir, "en")

	if got := tr.Choice("apples", 0); got != "no apples" {
		t.Fatalf("0 = %q", got)
	}
	if got := tr.Choice("apples", 1); got != "1 apple" {
		t.Fatalf("1 = %q", got)
	}
	if got := tr.Choice("apples", 5); got != "5 apples" {
		t.Fatalf("5 = %q", got)
	}
}

func TestChoice_FallsBackToOther(t *testing.T) {
	dir := t.TempDir()
	writeLocale(t, dir, "en", `{"items": {"one": "one item", "other": ":count items"}}`)
	tr, _ := Load(dir, "en")
	if got := tr.Choice("items", 0); got != "0 items" {
		t.Fatalf("zero falls back to other, got %q", got)
	}
}

func TestChoice_MissingKeyEchoes(t *testing.T) {
	dir := t.TempDir()
	writeLocale(t, dir, "en", `{}`)
	tr, _ := Load(dir, "en")
	if got := tr.Choice("missing", 5); got != "missing" {
		t.Fatalf("missing Choice = %q", got)
	}
}

func TestHas(t *testing.T) {
	dir := t.TempDir()
	writeLocale(t, dir, "en", `{"hello": "Hi"}`)
	tr, _ := Load(dir, "en")
	if !tr.Has("hello") {
		t.Fatal("Has must be true for present key")
	}
	if tr.Has("nope") {
		t.Fatal("Has must be false for absent key")
	}
}

func TestSubstitute_MultiplePlaceholders(t *testing.T) {
	dir := t.TempDir()
	writeLocale(t, dir, "en", `{"greet": "Hello :first :last"}`)
	tr, _ := Load(dir, "en")
	if got := tr.Get("greet", M{"first": "Ada", "last": "Lovelace"}); got != "Hello Ada Lovelace" {
		t.Fatalf("got %q", got)
	}
}

func TestAdd_AllowsInlineLocales(t *testing.T) {
	dir := t.TempDir()
	writeLocale(t, dir, "en", `{"x": "y"}`)
	tr, _ := Load(dir, "en")
	tr.Add("ru", map[string]any{"x": "русский"})
	if got := tr.WithLocale("ru").Get("x"); got != "русский" {
		t.Fatalf("ru Get = %q", got)
	}
}

func TestCurrentAndDefault(t *testing.T) {
	dir := t.TempDir()
	writeLocale(t, dir, "en", `{"x": "y"}`)
	tr, _ := Load(dir, "en")
	if tr.Default() != "en" || tr.Current() != "en" {
		t.Fatalf("default/current = %q/%q", tr.Default(), tr.Current())
	}
	uz := tr.WithLocale("uz")
	if uz.Current() != "uz" {
		t.Fatalf("WithLocale Current = %q", uz.Current())
	}
	if tr.Current() != "en" {
		t.Fatal("WithLocale must not mutate the receiver")
	}
}
