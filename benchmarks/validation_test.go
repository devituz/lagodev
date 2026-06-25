package benchmarks

import (
	"testing"

	"github.com/devituz/lagodev/validation"
)

type registerReq struct {
	Name     string `json:"name" validate:"required,max=255"`
	Email    string `json:"email" validate:"required,email"`
	Age      int    `json:"age" validate:"gte=18,lte=120"`
	Password string `json:"password" validate:"required,min=8"`
	Role     string `json:"role" validate:"in=admin|user|guest"`
}

// BenchmarkValidate_Struct measures the reflection-based struct tag validator
// on a valid payload (the common case — rules pass, no error allocation).
func BenchmarkValidate_Struct(b *testing.B) {
	req := registerReq{
		Name:     "Ada Lovelace",
		Email:    "ada@example.com",
		Age:      36,
		Password: "supersecret",
		Role:     "admin",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := validation.Validate(&req); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkValidate_Map measures the map-based validator that runs against
// decoded JSON without a backing struct.
func BenchmarkValidate_Map(b *testing.B) {
	data := map[string]any{
		"name":  "Ada Lovelace",
		"email": "ada@example.com",
		"age":   36,
	}
	rules := validation.Rules{
		"name":  {"required", "max=255"},
		"email": {"required", "email"},
		"age":   {"required", "integer", "gte=18"},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := validation.Map(data, rules); err != nil {
			b.Fatal(err)
		}
	}
}
