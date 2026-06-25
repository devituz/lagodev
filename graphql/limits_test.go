package graphql

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Adversarial limit tests: each constructs one of the threat-model attacks and
// asserts the engine rejects it with a bounded error instead of
// hanging/panicking/OOMing. All run under a deadline so a regression that
// reintroduces unbounded work fails as a timeout rather than wedging the suite.
// ---------------------------------------------------------------------------

// runBounded executes within a generous deadline and reports the response; a
// hang (unbounded recursion / amplification) surfaces as a test failure rather
// than freezing CI.
func runBounded(t *testing.T, cs *CompiledSchema, req Request, limits Limits) Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan Response, 1)
	go func() { done <- ExecuteWithLimits(ctx, cs, req, limits) }()
	select {
	case res := <-done:
		return res
	case <-ctx.Done():
		t.Fatal("execution did not complete within deadline (possible DoS regression)")
		return Response{}
	}
}

// hasErrContaining reports whether any response error message contains sub.
func hasErrContaining(res Response, sub string) bool {
	for _, e := range res.Errors {
		if strings.Contains(e.Message, sub) {
			return true
		}
	}
	return false
}

// TestDepthBomb: a deeply self-nesting selection set must be rejected by the
// depth ceiling, not drive resolution into stack exhaustion.
func TestDepthBomb(t *testing.T) {
	cs := buildExecSchema(t)
	// user { posts { ... } } is the only nestable path; to drive arbitrary depth
	// we nest a non-existent field so the cost walker sees the depth regardless
	// of schema shape (limits run before field validation).
	const depth = 5000
	q := "{" + strings.Repeat("a{", depth) + strings.Repeat("}", depth) + "}"

	limits := DefaultLimits()
	res := runBounded(t, cs, Request{Query: q}, limits)
	if !hasErrContaining(res, "max depth") {
		t.Fatalf("expected max-depth rejection, got: %+v", res.Errors)
	}
	if res.Data != nil {
		t.Fatalf("expected nil data on rejection, got %v", res.Data)
	}
}

// TestAliasAmplification: the same field aliased thousands of times must trip
// the alias ceiling (and the node ceiling), not be resolved one-by-one.
func TestAliasAmplification(t *testing.T) {
	cs := buildExecSchema(t)
	var b strings.Builder
	b.WriteString("{")
	const aliases = 20_000 // > default MaxAliases (10k), < default MaxTokens budget
	for i := 0; i < aliases; i++ {
		b.WriteString("a")
		b.WriteString(itoa(i))
		b.WriteString(":__typename ")
	}
	b.WriteString("}")

	res := runBounded(t, cs, Request{Query: b.String()}, DefaultLimits())
	if !hasErrContaining(res, "max aliases") && !hasErrContaining(res, "max selection nodes") {
		t.Fatalf("expected alias/node rejection, got: %+v", res.Errors)
	}
}

// TestFragmentBomb: a chain of fragments each spreading the previous one many
// times causes exponential width on expansion. The node ceiling must reject it
// quickly (the cost walker aborts mid-expansion), without ever materialising
// the full blow-up.
func TestFragmentBomb(t *testing.T) {
	cs := buildExecSchema(t)
	// Classic "billion laughs" GraphQL variant: f0 selects a leaf, f1 spreads f0
	// many times, ... fN spreads fN-1. Expanded size ~ width^N (here 10^12).
	var b strings.Builder
	const layers = 12
	const width = 10
	b.WriteString("query { ...f")
	b.WriteString(itoa(layers))
	b.WriteString(" }\n")
	b.WriteString("fragment f0 on Query { __typename }\n")
	for layer := 1; layer <= layers; layer++ {
		b.WriteString("fragment f")
		b.WriteString(itoa(layer))
		b.WriteString(" on Query {")
		for w := 0; w < width; w++ {
			b.WriteString(" ...f")
			b.WriteString(itoa(layer - 1))
		}
		b.WriteString(" }\n")
	}

	res := runBounded(t, cs, Request{Query: b.String()}, DefaultLimits())
	if !hasErrContaining(res, "max selection nodes") && !hasErrContaining(res, "max depth") {
		t.Fatalf("expected node/depth rejection of fragment bomb, got: %+v", res.Errors)
	}
}

// TestHugeDocumentBytes: an oversized document is rejected before parsing.
func TestHugeDocumentBytes(t *testing.T) {
	cs := buildExecSchema(t)
	limits := DefaultLimits()
	limits.MaxDocumentBytes = 1024
	q := "{ " + strings.Repeat("a ", 2000) + "}"
	res := runBounded(t, cs, Request{Query: q}, limits)
	if !hasErrContaining(res, "max size") {
		t.Fatalf("expected max-size rejection, got: %+v", res.Errors)
	}
}

// TestTokenFlood: a document under the byte cap but with many tokens trips the
// token ceiling in the lexer/parser.
func TestTokenFlood(t *testing.T) {
	cs := buildExecSchema(t)
	limits := DefaultLimits()
	limits.MaxTokens = 1000
	// ~6000 name tokens, well under 1 MiB.
	q := "{ " + strings.Repeat("a ", 3000) + "}"
	res := runBounded(t, cs, Request{Query: q}, limits)
	if !hasErrContaining(res, "max tokens") {
		t.Fatalf("expected max-tokens rejection, got: %+v", res.Errors)
	}
}

// TestIntrospectionDisabled: __typename / __schema / __type are rejected when
// introspection is disabled.
func TestIntrospectionDisabled(t *testing.T) {
	cs := buildExecSchema(t)
	limits := DefaultLimits()
	limits.DisableIntrospection = true
	for _, q := range []string{
		`{ __typename }`,
		`{ user(id:"1") { __typename } }`,
		`{ __schema { types { name } } }`,
		`{ __type(name:"User") { name } }`,
	} {
		res := runBounded(t, cs, Request{Query: q}, limits)
		if !hasErrContaining(res, "introspection is disabled") {
			t.Fatalf("query %q: expected introspection rejection, got: %+v", q, res.Errors)
		}
	}
}

// TestIntrospectionEnabledByDefault: __typename still works with default limits,
// preserving backward-compatible behaviour.
func TestIntrospectionEnabledByDefault(t *testing.T) {
	cs := buildExecSchema(t)
	res := runBounded(t, cs, Request{Query: `{ __typename }`}, DefaultLimits())
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors with introspection enabled: %+v", res.Errors)
	}
}

// TestZeroLimitsDisablesChecks: the zero Limits value applies no ceilings, so a
// document that would trip defaults executes (and fails normal validation, not a
// limit error). This guards the documented "zero = off" contract.
func TestZeroLimitsDisablesChecks(t *testing.T) {
	cs := buildExecSchema(t)
	q := "{ " + strings.Repeat("a:__typename ", 20000) + "}"
	res := runBounded(t, cs, Request{Query: q}, Limits{})
	if hasErrContaining(res, "max aliases") || hasErrContaining(res, "max selection nodes") {
		t.Fatalf("zero limits should not enforce ceilings, got: %+v", res.Errors)
	}
}

// TestNormalQueryUnderDefaults: a realistic query passes default limits cleanly,
// proving the defaults are generous enough for ordinary traffic.
func TestNormalQueryUnderDefaults(t *testing.T) {
	cs := buildExecSchema(t)
	q := `{ user(id:"1") { id name posts { id title } } }`
	res := runBounded(t, cs, Request{Query: q}, DefaultLimits())
	if len(res.Errors) != 0 {
		t.Fatalf("normal query rejected under defaults: %+v", res.Errors)
	}
	if res.Data == nil {
		t.Fatal("expected data for normal query")
	}
}

// itoa is a tiny base-10 formatter to avoid importing strconv in test builders.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
