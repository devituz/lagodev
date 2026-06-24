package openapi

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

type address struct {
	City string `json:"city" validate:"required"`
	Zip  string `json:"zip,omitempty"`
}

type createUser struct {
	Name      string    `json:"name"       validate:"required,min=2,max=64"`
	Email     string    `json:"email"      validate:"required,email"`
	Role      string    `json:"role"       validate:"required,in=admin|user|guest"`
	Age       int       `json:"age"        validate:"gte=18,lte=120"`
	Token     string    `json:"token"      validate:"uuid"`
	Tags      []string  `json:"tags"       validate:"max=5"`
	Nickname  *string   `json:"nickname,omitempty"`
	Address   address   `json:"address"`
	CreatedAt time.Time `json:"created_at"`
	Secret    string    `json:"-"`
}

func userSchema() *Schema { return SchemaFor(reflect.TypeOf(createUser{})) }

func TestSchemaOf_RequiredFromValidate(t *testing.T) {
	s := userSchema()
	req := map[string]bool{}
	for _, r := range s.Required {
		req[r] = true
	}
	for _, want := range []string{"name", "email", "role"} {
		if !req[want] {
			t.Errorf("expected %q required, got required=%v", want, s.Required)
		}
	}
	if req["age"] {
		t.Errorf("age has no required rule; should not be in required=%v", s.Required)
	}
}

func TestSchemaOf_Formats(t *testing.T) {
	s := userSchema()
	if got := s.Properties["email"].Format; got != "email" {
		t.Errorf("email format = %q, want email", got)
	}
	if got := s.Properties["token"].Format; got != "uuid" {
		t.Errorf("token format = %q, want uuid", got)
	}
	ca := s.Properties["created_at"]
	if ca.Format != "date-time" || !ca.hasType("string") {
		t.Errorf("created_at = %+v, want string/date-time", ca)
	}
}

func TestSchemaOf_Bounds(t *testing.T) {
	s := userSchema()
	name := s.Properties["name"]
	if name.MinLength == nil || *name.MinLength != 2 {
		t.Errorf("name.minLength = %v, want 2", name.MinLength)
	}
	if name.MaxLength == nil || *name.MaxLength != 64 {
		t.Errorf("name.maxLength = %v, want 64", name.MaxLength)
	}
	age := s.Properties["age"]
	if age.Minimum == nil || *age.Minimum != 18 {
		t.Errorf("age.minimum = %v, want 18", age.Minimum)
	}
	if age.Maximum == nil || *age.Maximum != 120 {
		t.Errorf("age.maximum = %v, want 120", age.Maximum)
	}
	tags := s.Properties["tags"]
	if tags.MaxItems == nil || *tags.MaxItems != 5 {
		t.Errorf("tags.maxItems = %v, want 5 (slice max -> maxItems)", tags.MaxItems)
	}
}

func TestSchemaOf_Enum(t *testing.T) {
	s := userSchema()
	role := s.Properties["role"]
	if len(role.Enum) != 3 {
		t.Fatalf("role.enum = %v, want 3 values", role.Enum)
	}
	got := map[any]bool{}
	for _, v := range role.Enum {
		got[v] = true
	}
	for _, want := range []string{"admin", "user", "guest"} {
		if !got[want] {
			t.Errorf("missing enum %q in %v", want, role.Enum)
		}
	}
}

func TestSchemaOf_NestedSlicePointerNullable(t *testing.T) {
	s := userSchema()

	addr := s.Properties["address"]
	if !addr.hasType("object") {
		t.Errorf("address type = %v, want object", addr.Type)
	}
	if addr.Properties["city"] == nil {
		t.Errorf("address.city missing")
	}

	tags := s.Properties["tags"]
	if !tags.hasType("array") || tags.Items == nil || !tags.Items.hasType("string") {
		t.Errorf("tags = %+v, want array of string", tags)
	}

	nick := s.Properties["nickname"]
	if !nick.hasType("null") || !nick.hasType("string") {
		t.Errorf("nickname type = %v, want [string,null]", nick.Type)
	}
}

func TestSchemaOf_OmitEmptyAndSkip(t *testing.T) {
	s := userSchema()
	if _, ok := s.Properties["Secret"]; ok {
		t.Errorf("json:\"-\" field Secret should be skipped")
	}
	if _, ok := s.Properties["-"]; ok {
		t.Errorf("skipped field leaked as key '-'")
	}
	zip := s.Properties["address"].Properties["zip"]
	if !zip.hasType("null") {
		t.Errorf("zip (omitempty) type = %v, want nullable", zip.Type)
	}
}

func TestSchemaJSON_Marshals31TypeUnion(t *testing.T) {
	s := userSchema()
	b, err := json.Marshal(s.Properties["nickname"])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	arr, ok := m["type"].([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("nullable type did not marshal as array union: %s", b)
	}
	// Single type marshals as a bare string.
	b2, _ := json.Marshal(s.Properties["email"])
	_ = json.Unmarshal(b2, &m)
	if _, ok := m["type"].(string); !ok {
		t.Errorf("single type did not marshal as string: %s", b2)
	}
}

func TestSchemaOf_EmbeddedFlattened(t *testing.T) {
	type base struct {
		ID int `json:"id" validate:"required"`
	}
	type derived struct {
		base
		Name string `json:"name"`
	}
	s := SchemaFor(reflect.TypeOf(derived{}))
	if s.Properties["id"] == nil {
		t.Errorf("embedded field id not promoted: %v", s.Properties)
	}
	if s.Properties["name"] == nil {
		t.Errorf("name missing")
	}
}
