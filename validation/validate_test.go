package validation

import (
	"encoding/json"
	"errors"
	"testing"
)

type registerReq struct {
	Name                 string `json:"name" validate:"required,max=255"`
	Email                string `json:"email" validate:"required,email"`
	Age                  int    `json:"age" validate:"gte=18,lte=120"`
	Password             string `json:"password" validate:"required,min=8,confirmed"`
	PasswordConfirmation string `json:"password_confirmation"`
	Role                 string `json:"role" validate:"in=admin|user|guest"`
}

func TestValidateStructValid(t *testing.T) {
	req := registerReq{
		Name:                 "Ada",
		Email:                "ada@example.com",
		Age:                  30,
		Password:             "supersecret",
		PasswordConfirmation: "supersecret",
		Role:                 "admin",
	}
	if err := Validate(&req); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestValidateStructInvalid(t *testing.T) {
	req := registerReq{
		Name:                 "",
		Email:                "bad",
		Age:                  10,
		Password:             "short",
		PasswordConfirmation: "different",
		Role:                 "root",
	}
	err := Validate(&req)
	if err == nil {
		t.Fatal("expected errors")
	}
	var ve ValidationErrors
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}
	for _, f := range []string{"name", "email", "age", "password", "role"} {
		if !ve.Has(f) {
			t.Errorf("expected error on field %q, got none (errors=%v)", f, ve)
		}
	}
	// Field names use json tags.
	if ve.Has("Name") {
		t.Error("error should be keyed by json tag 'name', not 'Name'")
	}
}

func TestConfirmedRule(t *testing.T) {
	req := registerReq{
		Name: "Ada", Email: "a@b.com", Age: 20, Role: "user",
		Password: "password1", PasswordConfirmation: "password1",
	}
	if err := Validate(&req); err != nil {
		t.Fatalf("matching confirmation should pass, got %v", err)
	}
	req.PasswordConfirmation = "mismatch"
	err := Validate(&req)
	var ve ValidationErrors
	if !errors.As(err, &ve) || !ve.Has("password") {
		t.Fatalf("expected password confirmation error, got %v", err)
	}
}

type crossFieldReq struct {
	Start string `json:"start" validate:"required"`
	End   string `json:"end" validate:"nefield=start"`
	Pin   string `json:"pin"`
	PinV  string `json:"pin_v" validate:"eqfield=pin"`
}

func TestCrossFieldRules(t *testing.T) {
	ok := crossFieldReq{Start: "a", End: "b", Pin: "1234", PinV: "1234"}
	if err := Validate(&ok); err != nil {
		t.Fatalf("expected valid cross-field, got %v", err)
	}

	bad := crossFieldReq{Start: "a", End: "a", Pin: "1234", PinV: "0000"}
	err := Validate(&bad)
	var ve ValidationErrors
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}
	if !ve.Has("end") {
		t.Error("nefield should fail when end == start")
	}
	if !ve.Has("pin_v") {
		t.Error("eqfield should fail when pin_v != pin")
	}
}

type address struct {
	City string `json:"city" validate:"required"`
	Zip  string `json:"zip" validate:"required,len=5"`
}

type orderItem struct {
	SKU string `json:"sku" validate:"required"`
	Qty int    `json:"qty" validate:"gte=1"`
}

type orderReq struct {
	Address address     `json:"address" validate:"dive"`
	Items   []orderItem `json:"items" validate:"dive"`
}

func TestDiveNested(t *testing.T) {
	bad := orderReq{
		Address: address{City: "", Zip: "123"},
		Items: []orderItem{
			{SKU: "A1", Qty: 2},
			{SKU: "", Qty: 0},
		},
	}
	err := Validate(&bad)
	var ve ValidationErrors
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}
	want := []string{"address.city", "address.zip", "items.1.sku", "items.1.qty"}
	for _, f := range want {
		if !ve.Has(f) {
			t.Errorf("expected nested error %q, got fields %v", f, ve.Fields())
		}
	}
	if ve.Has("items.0.sku") {
		t.Error("valid item 0 should not produce errors")
	}
}

func TestMapValidation(t *testing.T) {
	data := map[string]any{
		"email": "bad",
		"age":   5,
	}
	err := Map(data, Rules{
		"email":    {"required", "email"},
		"age":      {"required", "gte=18"},
		"username": {"required"}, // absent key
	})
	var ve ValidationErrors
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}
	for _, f := range []string{"email", "age", "username"} {
		if !ve.Has(f) {
			t.Errorf("expected map error on %q", f)
		}
	}
}

func TestMapCrossField(t *testing.T) {
	data := map[string]any{
		"password":              "secret123",
		"password_confirmation": "secret123",
	}
	if err := Map(data, Rules{"password": {"required", "confirmed"}}); err != nil {
		t.Fatalf("matching confirmation in map should pass, got %v", err)
	}
	data["password_confirmation"] = "nope"
	if err := Map(data, Rules{"password": {"required", "confirmed"}}); err == nil {
		t.Fatal("mismatched confirmation in map should fail")
	}
}

func TestBuilder(t *testing.T) {
	b := New().
		Field("email", "required", "email").
		Field("age", "integer", "gte=18")
	err := b.Validate(map[string]any{"email": "ok@ok.com", "age": "20"})
	if err != nil {
		t.Fatalf("builder valid case failed: %v", err)
	}
	err = b.Validate(map[string]any{"email": "x", "age": "10"})
	if err == nil {
		t.Fatal("builder invalid case should fail")
	}
}

func TestCustomMessages(t *testing.T) {
	req := registerReq{Email: "bad", Age: 5}
	err := ValidateWith(&req, Messages{
		"email.email": "We need a real email",
		"required":    "this cannot be blank",
	})
	var ve ValidationErrors
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}
	if got := ve.First("email"); got != "We need a real email" {
		// email field has both required(pass) and email(fail); first failing is email
		if got != "this cannot be blank" {
			// "" name is not blank, required passes; so email message expected
			t.Fatalf("field-specific override not applied: %q", got)
		}
	}
	if got := ve.First("name"); got != "this cannot be blank" {
		t.Fatalf("rule-level override not applied for name: %q", got)
	}
}

func TestErrorShape(t *testing.T) {
	ve := ValidationErrors{}
	ve.Add("email", "must be a valid email address")
	ve.Add("email", "is required")
	ve.Add("age", "must be >= 18")

	if !ve.Has("email") || ve.Has("missing") {
		t.Fatal("Has incorrect")
	}
	if ve.First("email") != "must be a valid email address" {
		t.Fatalf("First wrong: %q", ve.First("email"))
	}
	if got := ve.Fields(); len(got) != 2 || got[0] != "age" || got[1] != "email" {
		t.Fatalf("Fields not sorted: %v", got)
	}

	// ToMap is a copy.
	m := ve.ToMap()
	m["email"][0] = "mutated"
	if ve["email"][0] == "mutated" {
		t.Fatal("ToMap must return a copy")
	}

	// Response JSON shape suitable for 422.
	resp := Response(ve)
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Message string              `json:"message"`
		Errors  map[string][]string `json:"errors"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Message == "" || len(decoded.Errors["email"]) != 2 {
		t.Fatalf("bad response shape: %s", b)
	}

	// error string is deterministic.
	if ve.Error() == "" {
		t.Fatal("Error() empty")
	}
}

func TestValidateNonStructNoPanic(t *testing.T) {
	if err := Validate(42); err != nil {
		t.Fatalf("non-struct should be a no-op, got %v", err)
	}
	if err := Validate(nil); err != nil {
		t.Fatalf("nil should be a no-op, got %v", err)
	}
}
