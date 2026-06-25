package graphql

import "testing"

// fuzzSeeds are GraphQL fragments fed to both the lexer and parser fuzzers as a
// starting corpus: valid documents, malformed punctuation, unterminated
// strings/escapes, deep nesting and amplification shapes. The goal is to drive
// the lexer and parser into panics/hangs on hostile input; both must always
// terminate with a value-or-error, never crash.
var fuzzSeeds = []string{
	``,
	`{}`,
	`{ a }`,
	`{ a b c }`,
	`{ user(id:"1") { name posts { id title } } }`,
	`query Q($x: Int = 1) { add(a: $x, b: 2) }`,
	`mutation { rename(id:"1", name:"x") }`,
	`{ ...F } fragment F on Query { a }`,
	`{ ... on Query { a } }`,
	`{ a:b c:d }`,
	`{ "unterminated`,
	`{ a(x: "esc é \n \t") }`,
	`{ a(x: "\q") }`,
	`{ a(x: [1, 2.5, true, null, EN, "s"]) }`,
	`{ a(x: { k: 1, n: { m: 2 } }) }`,
	`...`,
	`.`,
	`..`,
	`{ a( }`,
	`{ a: }`,
	`{{{{{{{{{{`,
	`query`,
	`subscription { x }`,
	`fragment on on On { x }`,
	`{ a(b: $) }`,
	"\x00\x01\x02",
	`{ a(x: 1e) }`,
	`{ a(x: -) }`,
	"{ \uFEFF a }",
	`# comment only`,
}

// FuzzLexer drives the standalone lexer until EOF or error on arbitrary input.
// It must never panic or loop forever; every call must make progress and stop.
func FuzzLexer(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		l := newLexer(src)
		// Bound the loop independently of correctness: a non-advancing lexer
		// would otherwise spin. len(src)+2 tokens is a safe upper bound since
		// every token consumes at least one rune.
		for i := 0; i <= len(src)+2; i++ {
			tok, err := l.next()
			if err != nil {
				return
			}
			if tok.kind == tEOF {
				return
			}
		}
		t.Fatalf("lexer did not reach EOF/error within bound for input %q", src)
	})
}

// FuzzParse drives the full parser (with a token cap so amplification inputs
// cannot wedge the fuzzer) on arbitrary input. It must always return a document
// or an error, never panic. When parsing succeeds the document is additionally
// run through the cycle check and default-limit validation, exercising those
// passes against fuzzer-discovered shapes.
func FuzzParse(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		doc, err := parseDocumentLimited(src, 200_000)
		if err != nil {
			return
		}
		if doc == nil {
			t.Fatalf("nil document and nil error for input %q", src)
		}
		if err := detectFragmentCycles(doc); err != nil {
			return
		}
		// Validation must terminate (bounded) on any parsed, acyclic document.
		_ = DefaultLimits().validate(doc)
	})
}
