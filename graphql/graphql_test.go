package graphql

import (
	"context"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Lexer tests
// ---------------------------------------------------------------------------

// lexAll drives the lexer to completion, returning every token up to and
// including the final tEOF, or the first error encountered.
func lexAll(src string) ([]token, error) {
	l := newLexer(src)
	var out []token
	for {
		tok, err := l.next()
		if err != nil {
			return out, err
		}
		out = append(out, tok)
		if tok.kind == tEOF {
			return out, nil
		}
	}
}

// kinds projects a token slice to its kinds for compact comparison.
func kinds(toks []token) []tokenKind {
	out := make([]tokenKind, len(toks))
	for i, t := range toks {
		out[i] = t.kind
	}
	return out
}

func TestLexerPunctuators(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []tokenKind
	}{
		{"braces", "{}", []tokenKind{tBraceL, tBraceR, tEOF}},
		{"parens", "()", []tokenKind{tParenL, tParenR, tEOF}},
		{"brackets", "[]", []tokenKind{tBracketL, tBracketR, tEOF}},
		{"colon", ":", []tokenKind{tColon, tEOF}},
		{"dollar", "$", []tokenKind{tDollar, tEOF}},
		{"equals", "=", []tokenKind{tEquals, tEOF}},
		{"bang", "!", []tokenKind{tBang, tEOF}},
		{"spread", "...", []tokenKind{tSpread, tEOF}},
		{"empty", "", []tokenKind{tEOF}},
		{"mixed", "{ a(b:$c=[1]!) }", []tokenKind{
			tBraceL, tName, tParenL, tName, tColon, tDollar, tName, tEquals,
			tBracketL, tInt, tBracketR, tBang, tParenR, tBraceR, tEOF,
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toks, err := lexAll(tc.src)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := kinds(toks)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d (%v), want %d (%v)", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("token %d = %v, want %v (all: %v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

func TestLexerNames(t *testing.T) {
	tests := []struct {
		src  string
		want string
	}{
		{"name", "name"},
		{"_under", "_under"},
		{"a1b2", "a1b2"},
		{"_", "_"},
		{"CamelCase", "CamelCase"},
		{"with123", "with123"},
	}
	for _, tc := range tests {
		t.Run(tc.src, func(t *testing.T) {
			toks, err := lexAll(tc.src)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(toks) != 2 || toks[0].kind != tName {
				t.Fatalf("got %v, want single tName + EOF", toks)
			}
			if toks[0].value != tc.want {
				t.Fatalf("value = %q, want %q", toks[0].value, tc.want)
			}
		})
	}
}

func TestLexerNumbers(t *testing.T) {
	tests := []struct {
		src  string
		kind tokenKind
		val  string
	}{
		{"0", tInt, "0"},
		{"123", tInt, "123"},
		{"-5", tInt, "-5"},
		{"-0", tInt, "-0"},
		{"1.5", tFloat, "1.5"},
		{"-1.5", tFloat, "-1.5"},
		{"3.14159", tFloat, "3.14159"},
		{"1e10", tFloat, "1e10"},
		{"1E10", tFloat, "1E10"},
		{"1.5e+3", tFloat, "1.5e+3"},
		{"2e-4", tFloat, "2e-4"},
		{"-2.0e-4", tFloat, "-2.0e-4"},
	}
	for _, tc := range tests {
		t.Run(tc.src, func(t *testing.T) {
			toks, err := lexAll(tc.src)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(toks) != 2 {
				t.Fatalf("got %d tokens %v, want value + EOF", len(toks), toks)
			}
			if toks[0].kind != tc.kind {
				t.Fatalf("kind = %v, want %v", toks[0].kind, tc.kind)
			}
			if toks[0].value != tc.val {
				t.Fatalf("value = %q, want %q", toks[0].value, tc.val)
			}
		})
	}
}

func TestLexerStrings(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"plain", `"hello"`, "hello"},
		{"empty", `""`, ""},
		{"escaped quote", `"a\"b"`, `a"b`},
		{"backslash", `"a\\b"`, `a\b`},
		{"slash", `"a\/b"`, "a/b"},
		{"newline esc", `"a\nb"`, "a\nb"},
		{"tab esc", `"a\tb"`, "a\tb"},
		{"cr esc", `"a\rb"`, "a\rb"},
		{"backspace esc", `"a\bb"`, "a\bb"},
		{"formfeed esc", `"a\fb"`, "a\fb"},
		{"unicode esc", `"A"`, "A"},
		{"unicode esc mid", `"xéy"`, "xéy"},
		{"utf8 literal", `"héllo"`, "héllo"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toks, err := lexAll(tc.src)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(toks) != 2 || toks[0].kind != tString {
				t.Fatalf("got %v, want tString + EOF", toks)
			}
			if toks[0].value != tc.want {
				t.Fatalf("value = %q, want %q", toks[0].value, tc.want)
			}
		})
	}
}

func TestLexerSkipsIgnored(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{"spaces", "  {  }  "},
		{"tabs", "\t{\t}\t"},
		{"newlines", "\n{\n}\n"},
		{"crlf", "\r\n{\r\n}\r\n"},
		{"commas", "{,,,},,"},
		{"comment line", "{ # this is a comment\n }"},
		{"comment to eof", "{} # trailing comment no newline"},
		{"bom", "\uFEFF{}"},
		{"bom mid", "{\uFEFF}"},
		{"all mixed", "\uFEFF \t\n,# comment\n { } "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toks, err := lexAll(tc.src)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Every case above tokenizes to exactly { } EOF.
			got := kinds(toks)
			want := []tokenKind{tBraceL, tBraceR, tEOF}
			if len(got) != len(want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("got %v, want %v", got, want)
				}
			}
		})
	}
}

func TestLexerComment(t *testing.T) {
	// A comment must not swallow the next line's tokens.
	toks, err := lexAll("a # comment\nb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := kinds(toks)
	want := []tokenKind{tName, tName, tEOF}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
	if toks[0].value != "a" || toks[1].value != "b" {
		t.Fatalf("values = %q,%q want a,b", toks[0].value, toks[1].value)
	}
}

func TestLexerPositions(t *testing.T) {
	// Line/column tracking across a newline.
	toks, err := lexAll("a\n  b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if toks[0].line != 1 || toks[0].col != 1 {
		t.Fatalf("token a at %d:%d, want 1:1", toks[0].line, toks[0].col)
	}
	if toks[1].line != 2 || toks[1].col != 3 {
		t.Fatalf("token b at %d:%d, want 2:3", toks[1].line, toks[1].col)
	}
}

func TestLexerErrors(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		wantSub string
	}{
		{"lone dot", ".", "expected '...'"},
		{"two dots", "..", "expected '...'"},
		{"unexpected char", "%", "unexpected character"},
		{"at sign (directive)", "@skip", "unexpected character"},
		{"unterminated string", `"abc`, "unterminated string"},
		{"newline in string", "\"ab\ncd\"", "unterminated string"},
		{"bad escape", `"a\xb"`, "invalid escape"},
		{"bad unicode hex", `"\uZZZZ"`, "bad unicode escape"},
		{"short unicode", `"\u12"`, "bad unicode escape"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := lexAll(tc.src)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestLexerEOFIsStable(t *testing.T) {
	// Once at EOF, repeated next() keeps returning tEOF without error.
	l := newLexer("")
	for i := 0; i < 3; i++ {
		tok, err := l.next()
		if err != nil {
			t.Fatalf("call %d: unexpected error %v", i, err)
		}
		if tok.kind != tEOF {
			t.Fatalf("call %d: kind = %v, want tEOF", i, tok.kind)
		}
	}
}

func TestLexerSpreadVsName(t *testing.T) {
	toks, err := lexAll("...Frag")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := kinds(toks)
	want := []tokenKind{tSpread, tName, tEOF}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
	if toks[1].value != "Frag" {
		t.Fatalf("name value = %q, want Frag", toks[1].value)
	}
}

// ---------------------------------------------------------------------------
// Type system tests
// ---------------------------------------------------------------------------

func TestTypeNames(t *testing.T) {
	tests := []struct {
		typ  Type
		want string
	}{
		{Int, "Int"},
		{Float, "Float"},
		{String, "String"},
		{Boolean, "Boolean"},
		{ID, "ID"},
		{&Object{Name: "User"}, "User"},
		{&Enum{Name: "Color"}, "Color"},
		{&InputObject{Name: "Filter"}, "Filter"},
		{NewList(Int), "[Int]"},
		{NewNonNull(Int), "Int!"},
		{NewNonNull(NewList(NewNonNull(&Object{Name: "User"}))), "[User!]!"},
		{NewList(NewNonNull(String)), "[String!]"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.typ.TypeName(); got != tc.want {
				t.Fatalf("TypeName = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNamedTypeUnwraps(t *testing.T) {
	user := &Object{Name: "User"}
	tests := []struct {
		name string
		typ  Type
		want Type
	}{
		{"plain", user, user},
		{"nonnull", NewNonNull(user), user},
		{"list", NewList(user), user},
		{"list of nonnull", NewList(NewNonNull(user)), user},
		{"nonnull list nonnull", NewNonNull(NewList(NewNonNull(user))), user},
		{"scalar", NewNonNull(Int), Int},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := namedType(tc.typ); got != tc.want {
				t.Fatalf("namedType = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEnumValue(t *testing.T) {
	e := &Enum{
		Name: "Color",
		Values: map[string]*EnumValue{
			"RED":   {Value: 1},
			"GREEN": {Value: nil}, // falls back to the name
			"BLUE":  nil,          // nil member also falls back to the name
		},
	}
	tests := []struct {
		name   string
		key    string
		want   any
		wantOK bool
	}{
		{"explicit value", "RED", 1, true},
		{"nil value falls back to name", "GREEN", "GREEN", true},
		{"nil member falls back to name", "BLUE", "BLUE", true},
		{"missing", "PURPLE", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := e.value(tc.key)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if got != tc.want {
				t.Fatalf("value = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ArgValues tests
// ---------------------------------------------------------------------------

func TestArgValues(t *testing.T) {
	a := ArgValues{
		"s":     "hello",
		"i64":   int64(42),
		"i":     7,
		"f":     3.5,
		"b":     true,
		"list":  []any{1, 2},
		"input": map[string]any{"k": "v"},
	}

	t.Run("Get present/absent", func(t *testing.T) {
		if v, ok := a.Get("s"); !ok || v != "hello" {
			t.Fatalf("Get(s) = %v,%v", v, ok)
		}
		if _, ok := a.Get("missing"); ok {
			t.Fatalf("Get(missing) ok = true, want false")
		}
	})

	t.Run("String", func(t *testing.T) {
		if a.String("s") != "hello" {
			t.Fatalf("String(s) = %q", a.String("s"))
		}
		if a.String("i64") != "" {
			t.Fatalf("String(i64) = %q, want empty", a.String("i64"))
		}
		if a.String("missing") != "" {
			t.Fatalf("String(missing) = %q, want empty", a.String("missing"))
		}
	})

	t.Run("Int coercion", func(t *testing.T) {
		if a.Int("i64") != 42 {
			t.Fatalf("Int(i64) = %d", a.Int("i64"))
		}
		if a.Int("i") != 7 {
			t.Fatalf("Int(i) = %d", a.Int("i"))
		}
		if a.Int("f") != 3 {
			t.Fatalf("Int(f) = %d, want 3 (float64 truncated)", a.Int("f"))
		}
		if a.Int("s") != 0 {
			t.Fatalf("Int(s) = %d, want 0", a.Int("s"))
		}
	})

	t.Run("Float coercion", func(t *testing.T) {
		if a.Float("f") != 3.5 {
			t.Fatalf("Float(f) = %v", a.Float("f"))
		}
		if a.Float("i64") != 42.0 {
			t.Fatalf("Float(i64) = %v, want 42", a.Float("i64"))
		}
		if a.Float("s") != 0 {
			t.Fatalf("Float(s) = %v, want 0", a.Float("s"))
		}
	})

	t.Run("Bool", func(t *testing.T) {
		if !a.Bool("b") {
			t.Fatalf("Bool(b) = false, want true")
		}
		if a.Bool("s") {
			t.Fatalf("Bool(s) = true, want false")
		}
	})

	t.Run("List", func(t *testing.T) {
		if got := a.List("list"); len(got) != 2 {
			t.Fatalf("List(list) = %v", got)
		}
		if got := a.List("s"); got != nil {
			t.Fatalf("List(s) = %v, want nil", got)
		}
	})

	t.Run("Input", func(t *testing.T) {
		if got := a.Input("input"); got["k"] != "v" {
			t.Fatalf("Input(input) = %v", got)
		}
		if got := a.Input("s"); got != nil {
			t.Fatalf("Input(s) = %v, want nil", got)
		}
	})
}

// ---------------------------------------------------------------------------
// Schema builder tests
// ---------------------------------------------------------------------------

func TestNewSchemaValid(t *testing.T) {
	postType := &Object{
		Name: "Post",
		Fields: Fields{
			"title": &Field{Type: NewNonNull(String)},
		},
	}
	userType := &Object{
		Name: "User",
		Fields: Fields{
			"id":    &Field{Type: NewNonNull(ID)},
			"name":  &Field{Type: String},
			"posts": &Field{Type: NewList(postType)},
		},
	}
	statusEnum := &Enum{
		Name:   "Status",
		Values: map[string]*EnumValue{"ACTIVE": {Value: "active"}},
	}
	filterInput := &InputObject{
		Name:   "Filter",
		Fields: Args{"status": {Type: statusEnum}},
	}
	query := &Object{
		Name: "Query",
		Fields: Fields{
			"user": &Field{
				Type: userType,
				Args: Args{
					"id":     {Type: NewNonNull(ID)},
					"filter": {Type: filterInput},
				},
				Resolve: func(ctx context.Context, p any, a ArgValues) (any, error) {
					return nil, nil
				},
			},
		},
	}
	mutation := &Object{
		Name: "Mutation",
		Fields: Fields{
			"ping": &Field{Type: String},
		},
	}

	cs, err := NewSchema(Schema{Query: query, Mutation: mutation})
	if err != nil {
		t.Fatalf("NewSchema error: %v", err)
	}
	if cs == nil {
		t.Fatal("NewSchema returned nil schema")
	}
	if cs.query != query || cs.mutation != mutation {
		t.Fatal("roots not wired through")
	}

	// Every reachable named type must be indexed, transitively.
	wantTypes := []string{
		"Query", "Mutation", "User", "Post", "Status", "Filter",
		"ID", "String",
	}
	for _, name := range wantTypes {
		if _, ok := cs.types[name]; !ok {
			t.Errorf("type %q not collected; have %v", name, keysOf(cs.types))
		}
	}
}

func keysOf(m map[string]Type) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestNewSchemaErrors(t *testing.T) {
	tests := []struct {
		name    string
		schema  Schema
		wantSub string
	}{
		{
			name:    "nil query",
			schema:  Schema{Query: nil},
			wantSub: "requires a Query object",
		},
		{
			name: "nil field type",
			schema: Schema{Query: &Object{
				Name:   "Query",
				Fields: Fields{"x": &Field{Type: nil}},
			}},
			wantSub: "field Query.x has nil type",
		},
		{
			name: "nil field def",
			schema: Schema{Query: &Object{
				Name:   "Query",
				Fields: Fields{"x": nil},
			}},
			wantSub: "field Query.x has nil type",
		},
		{
			name: "nil arg type",
			schema: Schema{Query: &Object{
				Name: "Query",
				Fields: Fields{"x": &Field{
					Type: String,
					Args: Args{"a": {Type: nil}},
				}},
			}},
			wantSub: "arg Query.x(a) has nil type",
		},
		{
			name: "nil arg def",
			schema: Schema{Query: &Object{
				Name: "Query",
				Fields: Fields{"x": &Field{
					Type: String,
					Args: Args{"a": nil},
				}},
			}},
			wantSub: "arg Query.x(a) has nil type",
		},
		{
			name: "nil input field type",
			schema: Schema{Query: &Object{
				Name: "Query",
				Fields: Fields{"x": &Field{
					Type: String,
					Args: Args{"in": {Type: &InputObject{
						Name:   "In",
						Fields: Args{"f": {Type: nil}},
					}}},
				}},
			}},
			wantSub: "input In.f has nil type",
		},
		{
			name: "nested nil through list/nonnull",
			schema: Schema{Query: &Object{
				Name:   "Query",
				Fields: Fields{"x": &Field{Type: NewList(NewNonNull(nil))}},
			}},
			wantSub: "nil type in schema",
		},
		{
			name: "malformed type reached via mutation",
			schema: Schema{
				Query: &Object{
					Name:   "Query",
					Fields: Fields{"ok": &Field{Type: String}},
				},
				Mutation: &Object{
					Name:   "Mutation",
					Fields: Fields{"bad": &Field{Type: nil}},
				},
			},
			wantSub: "field Mutation.bad has nil type",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cs, err := NewSchema(tc.schema)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil (schema=%v)", tc.wantSub, cs)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestNewSchemaHandlesRecursiveTypes(t *testing.T) {
	// A self-referential object must not loop forever in collect().
	node := &Object{Name: "Node"}
	node.Fields = Fields{
		"id":   &Field{Type: ID},
		"next": &Field{Type: node},
	}
	query := &Object{
		Name:   "Query",
		Fields: Fields{"root": &Field{Type: node}},
	}
	cs, err := NewSchema(Schema{Query: query})
	if err != nil {
		t.Fatalf("NewSchema error on recursive type: %v", err)
	}
	if _, ok := cs.types["Node"]; !ok {
		t.Fatal("recursive Node type not collected")
	}
}

func TestNewSchemaHandlesMutuallyRecursiveTypes(t *testing.T) {
	a := &Object{Name: "A"}
	b := &Object{Name: "B"}
	a.Fields = Fields{"b": &Field{Type: b}}
	b.Fields = Fields{"a": &Field{Type: a}}
	query := &Object{
		Name:   "Query",
		Fields: Fields{"a": &Field{Type: a}},
	}
	if _, err := NewSchema(Schema{Query: query}); err != nil {
		t.Fatalf("NewSchema error on mutually recursive types: %v", err)
	}
}
