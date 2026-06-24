package reflectutil

import (
	"reflect"
	"testing"
)

// Base is an embedded *pointer* base model. A zero-valued PtrUser leaves it
// nil, which previously panicked through v.FieldByIndex.
type Base struct {
	ID        int64  `orm:"primary;autoincrement"`
	TenantKey string `column:"tenant_key"`
}

type PtrUser struct {
	*Base
	Name string
}

func TestInsertableColumns_NilEmbeddedPointer(t *testing.T) {
	s := Parse(PtrUser{})

	// Must not panic even though *Base is nil.
	cols, vals := s.InsertableColumns(reflect.ValueOf(PtrUser{Name: "x"}))
	if len(cols) != len(vals) {
		t.Fatalf("cols/vals length mismatch: %d vs %d", len(cols), len(vals))
	}

	got := map[string]any{}
	for i, c := range cols {
		got[c] = vals[i]
	}
	if _, ok := got["name"]; !ok {
		t.Fatalf("expected name column, got %v", cols)
	}
	if got["name"] != "x" {
		t.Fatalf("name = %v, want x", got["name"])
	}
	// id is auto-increment and zero behind the nil embed -> skipped.
	if _, ok := got["id"]; ok {
		t.Fatalf("auto-increment id should be skipped, got %v", cols)
	}
	// tenant_key lives behind the nil embed -> emitted as zero value.
	if v, ok := got["tenant_key"]; !ok {
		t.Fatalf("expected tenant_key column, got %v", cols)
	} else if v != "" {
		t.Fatalf("tenant_key = %v, want zero string", v)
	}
}

func TestInsertableColumns_PopulatedEmbeddedPointer(t *testing.T) {
	s := Parse(PtrUser{})
	u := PtrUser{Base: &Base{ID: 0, TenantKey: "acme"}, Name: "y"}
	cols, vals := s.InsertableColumns(reflect.ValueOf(u))

	got := map[string]any{}
	for i, c := range cols {
		got[c] = vals[i]
	}
	if got["tenant_key"] != "acme" {
		t.Fatalf("tenant_key = %v, want acme", got["tenant_key"])
	}
	if got["name"] != "y" {
		t.Fatalf("name = %v, want y", got["name"])
	}
}
