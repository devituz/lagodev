package graphql

import (
	"fmt"
	"strings"
)

// Limits bounds the cost of an incoming GraphQL document so a public,
// untrusted-input endpoint cannot be driven into stack/CPU/memory exhaustion by
// a hostile query. Every field is a hard ceiling; a document exceeding any one
// of them is rejected with a bounded error before resolvers run.
//
// The zero value disables every check. Use DefaultLimits for production-safe
// ceilings that pass normal queries; override individual fields as needed. A
// value <= 0 in any field means "no limit" for that dimension, so callers can
// relax a single dimension without losing the others.
type Limits struct {
	// MaxDocumentBytes caps the length, in bytes, of the query document string.
	// Guards the lexer against very long inputs before tokenisation begins.
	MaxDocumentBytes int

	// MaxTokens caps the number of lexical tokens the parser will accept. Guards
	// against token floods that pass the byte limit yet still cost CPU to parse.
	MaxTokens int

	// MaxDepth caps the nesting depth of the (fragment-expanded) selection tree.
	// Defends against query-depth bombs that exhaust the resolution stack.
	MaxDepth int

	// MaxSelectionNodes caps the total number of selected fields after fragment
	// expansion. This is the primary defence against fragment/alias width
	// amplification: a fragment referenced many times, or a field aliased many
	// times, multiplies the node count and trips this budget.
	MaxSelectionNodes int

	// MaxAliases caps the number of aliased fields in the document (counted on
	// the raw AST, before expansion). Aliases are the cheap multiplier in an
	// amplification attack; this stops thousands of distinct response keys.
	MaxAliases int

	// DisableIntrospection rejects any document that selects an introspection
	// meta-field (__typename, __schema, __type) when true. Use in production to
	// keep the schema shape private.
	DisableIntrospection bool
}

// DefaultLimits returns production-safe limits: generous enough that ordinary
// application queries pass unchanged, tight enough that the threat-model attacks
// (depth bombs, alias/fragment amplification, token floods, oversized
// documents) are rejected with a bounded error. Introspection stays enabled to
// preserve current behaviour; set DisableIntrospection explicitly to lock it.
func DefaultLimits() Limits {
	return Limits{
		MaxDocumentBytes:     1 << 20, // 1 MiB
		MaxTokens:            100_000,
		MaxDepth:             50,
		MaxSelectionNodes:    100_000,
		MaxAliases:           10_000,
		DisableIntrospection: false,
	}
}

// errLimitExceeded is returned by the validation pass; it carries a bounded,
// client-safe message and never leaks internal state.
type errLimitExceeded struct{ msg string }

func (e errLimitExceeded) Error() string { return e.msg }

// checkDocumentSize enforces the byte ceiling on the raw query string. It runs
// before parsing so an oversized document is rejected without tokenising.
func (l Limits) checkDocumentSize(query string) error {
	if l.MaxDocumentBytes > 0 && len(query) > l.MaxDocumentBytes {
		return errLimitExceeded{fmt.Sprintf(
			"graphql: query document exceeds max size (%d > %d bytes)",
			len(query), l.MaxDocumentBytes)}
	}
	return nil
}

// validate runs the structural cost checks over a parsed document: alias count,
// introspection gating, and the fragment-expanded depth/node budget. It must be
// called after parseDocument and after detectFragmentCycles (which guarantees
// the spread graph is acyclic, so expansion terminates). It bounds its own work:
// the expansion walk aborts the instant the node budget is exceeded, so a
// fragment/alias bomb can never materialise its full blow-up before being
// rejected.
func (l Limits) validate(doc *document) error {
	if l.MaxAliases > 0 || l.DisableIntrospection {
		if err := l.checkAliasesAndIntrospection(doc); err != nil {
			return err
		}
	}
	if l.MaxDepth <= 0 && l.MaxSelectionNodes <= 0 {
		return nil
	}
	v := &costWalker{
		doc:       doc,
		maxDepth:  l.MaxDepth,
		maxNodes:  l.MaxSelectionNodes,
		expanding: map[string]bool{},
	}
	for _, op := range doc.operations {
		if err := v.walk(op.selSet, 1); err != nil {
			return err
		}
	}
	return nil
}

// checkAliasesAndIntrospection makes a single linear pass over every selection
// set in the document (operations + fragment definitions, without expansion),
// counting aliases and rejecting introspection meta-fields when disabled. This
// pass is O(raw AST size) and independent of fragment fan-out.
func (l Limits) checkAliasesAndIntrospection(doc *document) error {
	aliases := 0
	var scan func(sels []selection) error
	scan = func(sels []selection) error {
		for _, sel := range sels {
			switch {
			case sel.field != nil:
				if l.DisableIntrospection && isIntrospectionField(sel.field.name) {
					return errLimitExceeded{fmt.Sprintf(
						"graphql: introspection is disabled (field %q)", sel.field.name)}
				}
				if l.MaxAliases > 0 && sel.field.alias != "" {
					aliases++
					if aliases > l.MaxAliases {
						return errLimitExceeded{fmt.Sprintf(
							"graphql: query exceeds max aliases (%d)", l.MaxAliases)}
					}
				}
				if err := scan(sel.field.selSet); err != nil {
					return err
				}
			case sel.inlineFrag != nil:
				if err := scan(sel.inlineFrag.selSet); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, op := range doc.operations {
		if err := scan(op.selSet); err != nil {
			return err
		}
	}
	for _, frag := range doc.fragments {
		if err := scan(frag.selSet); err != nil {
			return err
		}
	}
	return nil
}

// isIntrospectionField reports whether name is a GraphQL introspection
// meta-field (the double-underscore namespace).
func isIntrospectionField(name string) bool {
	return strings.HasPrefix(name, "__")
}

// costWalker performs the fragment-expanded depth + node-count walk. It carries
// a per-spread "expanding" set so that, even though detectFragmentCycles has
// already rejected true cycles, a re-entrant spread can never loop; the node
// budget is the real terminator and is checked on every field.
type costWalker struct {
	doc       *document
	maxDepth  int
	maxNodes  int
	nodes     int
	expanding map[string]bool
}

// walk descends one selection set at the given depth, expanding fragment spreads
// in place (so width amplification is counted), enforcing the depth ceiling and
// incrementing the node budget per field. It returns an error the moment either
// ceiling is crossed, bounding total work to maxNodes increments.
func (v *costWalker) walk(sels []selection, depth int) error {
	if v.maxDepth > 0 && depth > v.maxDepth {
		return errLimitExceeded{fmt.Sprintf("graphql: query exceeds max depth (%d)", v.maxDepth)}
	}
	for _, sel := range sels {
		switch {
		case sel.field != nil:
			v.nodes++
			if v.maxNodes > 0 && v.nodes > v.maxNodes {
				return errLimitExceeded{fmt.Sprintf(
					"graphql: query exceeds max selection nodes (%d)", v.maxNodes)}
			}
			if len(sel.field.selSet) > 0 {
				if err := v.walk(sel.field.selSet, depth+1); err != nil {
					return err
				}
			}
		case sel.inlineFrag != nil:
			// Inline fragments do not add a response level; they flatten into the
			// enclosing object, so keep the same depth.
			if err := v.walk(sel.inlineFrag.selSet, depth); err != nil {
				return err
			}
		case sel.fragmentSpread != "":
			frag, ok := v.doc.fragments[sel.fragmentSpread]
			if !ok {
				continue // unknown spread: handled at execution, no cost here
			}
			if v.expanding[sel.fragmentSpread] {
				// Defensive: acyclic by detectFragmentCycles, but never recurse
				// into an in-progress spread regardless.
				continue
			}
			v.expanding[sel.fragmentSpread] = true
			err := v.walk(frag.selSet, depth)
			v.expanding[sel.fragmentSpread] = false
			if err != nil {
				return err
			}
		}
	}
	return nil
}
