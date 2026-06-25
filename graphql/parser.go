package graphql

import "fmt"

// document is the parsed AST of a GraphQL request: a set of operation
// definitions plus any named fragment definitions referenced by spreads.
type document struct {
	operations []*operation
	fragments  map[string]*fragmentDef
}

// operationKind distinguishes a query from a mutation. Subscriptions are not
// supported and are rejected at parse time.
type operationKind int

const (
	opQuery operationKind = iota
	opMutation
)

// operation is one query/mutation definition: its kind, optional name, the
// variable definitions it declares, and its top-level selection set.
type operation struct {
	kind      operationKind
	name      string
	variables []*variableDef
	selSet    []selection
}

// variableDef is a single $name: Type [= default] declaration on an operation.
type variableDef struct {
	name       string
	typ        typeRef
	defaultVal value
	hasDefault bool
}

// fragmentDef is a named fragment definition (fragment Name on Type { ... }).
type fragmentDef struct {
	name   string
	typeOn string
	selSet []selection
}

// selection is one entry in a selection set: a field, an inline fragment, or a
// fragment spread. Exactly one of the embedded pointers is non-nil.
type selection struct {
	field          *fieldNode
	inlineFrag     *inlineFragment
	fragmentSpread string
}

// fieldNode is a selected field: alias (optional), name, arguments and its own
// nested selection set (empty for leaf fields).
type fieldNode struct {
	alias  string
	name   string
	args   map[string]value
	selSet []selection
}

// inlineFragment is ... on Type { ... }; typeOn may be empty for an untyped
// inline fragment (... { ... }), which always applies.
type inlineFragment struct {
	typeOn string
	selSet []selection
}

// typeRef is a parsed type reference from a variable definition: a named type
// optionally wrapped in any number of list/non-null layers.
type typeRef struct {
	name    string // named type, empty when list is set
	list    *typeRef
	nonNull bool
}

// valueKind tags the parsed-but-uncoerced literal value carried by value.
type valueKind int

const (
	valNull valueKind = iota
	valInt
	valFloat
	valString
	valBool
	valEnum
	valVariable
	valList
	valObject
)

// value is a parsed argument/default literal. Concrete payloads live in the
// typed fields selected by kind; coercion against the schema happens later in
// the executor (coerceValue / coerceScalar).
type value struct {
	kind     valueKind
	intVal   string // raw int literal text (parsed to int64 on coercion)
	floatVal string // raw float literal text
	strVal   string
	boolVal  bool
	enumVal  string // enum member name, or variable name for valVariable
	listVal  []value
	objVal   map[string]value
}

// parser is a recursive-descent parser over the lexer's token stream. It keeps
// one token of lookahead and surfaces the source position in every error.
type parser struct {
	lex *lexer
	tok token

	// maxTokens, when > 0, caps the number of tokens lexed for one document.
	// Crossing it aborts parsing with a bounded error, defending the parser
	// against token-flood inputs that pass the byte limit. tokens counts tokens
	// produced by the underlying lexer.
	maxTokens int
	tokens    int
}

// parseDocumentLimited lexes+parses src into a document, rejecting unsupported
// constructs (directives, subscriptions) with a positioned error, and aborts if
// the lexer produces more than maxTokens tokens (maxTokens <= 0 means
// unlimited). It is the package entry point for untrusted input.
func parseDocumentLimited(src string, maxTokens int) (*document, error) {
	p := &parser{lex: newLexer(src), maxTokens: maxTokens}
	if err := p.next(); err != nil {
		return nil, err
	}
	return p.parseDoc()
}

// lexNext pulls one token from the lexer, enforcing the token cap. All token
// production routes through here so the cap is honoured on every code path.
func (p *parser) lexNext() (token, error) {
	tok, err := p.lex.next()
	if err != nil {
		return token{}, err
	}
	if tok.kind != tEOF {
		p.tokens++
		if p.maxTokens > 0 && p.tokens > p.maxTokens {
			return token{}, errLimitExceeded{fmt.Sprintf(
				"graphql: query exceeds max tokens (%d)", p.maxTokens)}
		}
	}
	return tok, nil
}

// next advances the parser by one token.
func (p *parser) next() error {
	tok, err := p.lexNext()
	if err != nil {
		return err
	}
	p.tok = tok
	return nil
}

// expect consumes the current token, requiring it to be of kind k.
func (p *parser) expect(k tokenKind, what string) error {
	if p.tok.kind != k {
		return p.errf("expected %s", what)
	}
	return p.next()
}

// errf builds a positioned parse error.
func (p *parser) errf(format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	return fmt.Errorf("graphql: %s at %d:%d (got %q)", msg, p.tok.line, p.tok.col, p.tok.value)
}

// parseDoc parses the top-level list of operation/fragment definitions.
func (p *parser) parseDoc() (*document, error) {
	doc := &document{fragments: map[string]*fragmentDef{}}
	for p.tok.kind != tEOF {
		switch {
		case p.tok.kind == tBraceL:
			// Anonymous shorthand query: { ... }.
			op, err := p.parseSelectionOperation()
			if err != nil {
				return nil, err
			}
			doc.operations = append(doc.operations, op)
		case p.tok.kind == tName && p.tok.value == "query":
			op, err := p.parseNamedOperation(opQuery)
			if err != nil {
				return nil, err
			}
			doc.operations = append(doc.operations, op)
		case p.tok.kind == tName && p.tok.value == "mutation":
			op, err := p.parseNamedOperation(opMutation)
			if err != nil {
				return nil, err
			}
			doc.operations = append(doc.operations, op)
		case p.tok.kind == tName && p.tok.value == "subscription":
			return nil, p.errf("subscriptions are not supported")
		case p.tok.kind == tName && p.tok.value == "fragment":
			frag, err := p.parseFragmentDef()
			if err != nil {
				return nil, err
			}
			doc.fragments[frag.name] = frag
		default:
			return nil, p.errf("unexpected token at top level")
		}
	}
	return doc, nil
}

// parseSelectionOperation parses the anonymous shorthand { ... } query form.
func (p *parser) parseSelectionOperation() (*operation, error) {
	sel, err := p.parseSelectionSet()
	if err != nil {
		return nil, err
	}
	return &operation{kind: opQuery, selSet: sel}, nil
}

// parseNamedOperation parses `query`/`mutation` [Name] [vars] selectionSet.
func (p *parser) parseNamedOperation(kind operationKind) (*operation, error) {
	if err := p.next(); err != nil { // consume keyword
		return nil, err
	}
	op := &operation{kind: kind}
	if p.tok.kind == tName {
		op.name = p.tok.value
		if err := p.next(); err != nil {
			return nil, err
		}
	}
	if p.tok.kind == tParenL {
		vars, err := p.parseVariableDefs()
		if err != nil {
			return nil, err
		}
		op.variables = vars
	}
	if p.tok.kind != tBraceL {
		return nil, p.errf("expected selection set '{'")
	}
	sel, err := p.parseSelectionSet()
	if err != nil {
		return nil, err
	}
	op.selSet = sel
	return op, nil
}

// parseVariableDefs parses ( $name: Type [= default], ... ).
func (p *parser) parseVariableDefs() ([]*variableDef, error) {
	if err := p.next(); err != nil { // consume (
		return nil, err
	}
	var defs []*variableDef
	for p.tok.kind != tParenR {
		if p.tok.kind != tDollar {
			return nil, p.errf("expected variable '$'")
		}
		if err := p.next(); err != nil {
			return nil, err
		}
		if p.tok.kind != tName {
			return nil, p.errf("expected variable name")
		}
		def := &variableDef{name: p.tok.value}
		if err := p.next(); err != nil {
			return nil, err
		}
		if err := p.expect(tColon, "':'"); err != nil {
			return nil, err
		}
		tr, err := p.parseTypeRef()
		if err != nil {
			return nil, err
		}
		def.typ = tr
		if p.tok.kind == tEquals {
			if err := p.next(); err != nil {
				return nil, err
			}
			dv, err := p.parseValue(false)
			if err != nil {
				return nil, err
			}
			def.defaultVal = dv
		}
		defs = append(defs, def)
	}
	if err := p.next(); err != nil { // consume )
		return nil, err
	}
	return defs, nil
}

// parseTypeRef parses a (possibly wrapped) type reference: Name, [Type], T!.
func (p *parser) parseTypeRef() (typeRef, error) {
	var tr typeRef
	switch p.tok.kind {
	case tBracketL:
		if err := p.next(); err != nil {
			return tr, err
		}
		inner, err := p.parseTypeRef()
		if err != nil {
			return tr, err
		}
		if err := p.expect(tBracketR, "']'"); err != nil {
			return tr, err
		}
		tr.list = &inner
	case tName:
		tr.name = p.tok.value
		if err := p.next(); err != nil {
			return tr, err
		}
	default:
		return tr, p.errf("expected type reference")
	}
	if p.tok.kind == tBang {
		tr.nonNull = true
		if err := p.next(); err != nil {
			return tr, err
		}
	}
	return tr, nil
}

// parseFragmentDef parses `fragment Name on Type { ... }`.
func (p *parser) parseFragmentDef() (*fragmentDef, error) {
	if err := p.next(); err != nil { // consume "fragment"
		return nil, err
	}
	if p.tok.kind != tName {
		return nil, p.errf("expected fragment name")
	}
	name := p.tok.value
	if name == "on" {
		return nil, p.errf("fragment name must not be 'on'")
	}
	if err := p.next(); err != nil {
		return nil, err
	}
	if p.tok.kind != tName || p.tok.value != "on" {
		return nil, p.errf("expected 'on' in fragment definition")
	}
	if err := p.next(); err != nil {
		return nil, err
	}
	if p.tok.kind != tName {
		return nil, p.errf("expected type condition")
	}
	typeOn := p.tok.value
	if err := p.next(); err != nil {
		return nil, err
	}
	if p.tok.kind != tBraceL {
		return nil, p.errf("expected fragment selection set '{'")
	}
	sel, err := p.parseSelectionSet()
	if err != nil {
		return nil, err
	}
	return &fragmentDef{name: name, typeOn: typeOn, selSet: sel}, nil
}

// parseSelectionSet parses { ... } yielding fields, inline fragments and
// fragment spreads. Directives are rejected.
func (p *parser) parseSelectionSet() ([]selection, error) {
	if err := p.next(); err != nil { // consume {
		return nil, err
	}
	var sels []selection
	for p.tok.kind != tBraceR {
		if p.tok.kind == tEOF {
			return nil, p.errf("unexpected EOF in selection set")
		}
		if p.tok.kind == tSpread {
			s, err := p.parseFragment()
			if err != nil {
				return nil, err
			}
			sels = append(sels, s)
			continue
		}
		f, err := p.parseField()
		if err != nil {
			return nil, err
		}
		sels = append(sels, selection{field: f})
	}
	if err := p.next(); err != nil { // consume }
		return nil, err
	}
	return sels, nil
}

// parseFragment parses either an inline fragment (... on T { } / ... { }) or a
// named fragment spread (...Name) after the leading spread token.
func (p *parser) parseFragment() (selection, error) {
	if err := p.next(); err != nil { // consume ...
		return selection{}, err
	}
	if p.tok.kind == tName && p.tok.value == "on" {
		if err := p.next(); err != nil {
			return selection{}, err
		}
		if p.tok.kind != tName {
			return selection{}, p.errf("expected type condition after 'on'")
		}
		typeOn := p.tok.value
		if err := p.next(); err != nil {
			return selection{}, err
		}
		sel, err := p.parseSelectionSet()
		if err != nil {
			return selection{}, err
		}
		return selection{inlineFrag: &inlineFragment{typeOn: typeOn, selSet: sel}}, nil
	}
	if p.tok.kind == tBraceL {
		// Untyped inline fragment: ... { ... }.
		sel, err := p.parseSelectionSet()
		if err != nil {
			return selection{}, err
		}
		return selection{inlineFrag: &inlineFragment{selSet: sel}}, nil
	}
	if p.tok.kind != tName {
		return selection{}, p.errf("expected fragment name or 'on'")
	}
	name := p.tok.value
	if err := p.next(); err != nil {
		return selection{}, err
	}
	return selection{fragmentSpread: name}, nil
}

// parseField parses [alias :] name [args] [selectionSet].
func (p *parser) parseField() (*fieldNode, error) {
	if p.tok.kind != tName {
		return nil, p.errf("expected field name")
	}
	f := &fieldNode{name: p.tok.value}
	if err := p.next(); err != nil {
		return nil, err
	}
	if p.tok.kind == tColon {
		// The first name was an alias; the real field name follows.
		f.alias = f.name
		if err := p.next(); err != nil {
			return nil, err
		}
		if p.tok.kind != tName {
			return nil, p.errf("expected field name after alias")
		}
		f.name = p.tok.value
		if err := p.next(); err != nil {
			return nil, err
		}
	}
	if p.tok.kind == tParenL {
		args, err := p.parseArguments()
		if err != nil {
			return nil, err
		}
		f.args = args
	}
	if p.tok.kind == tBraceL {
		sel, err := p.parseSelectionSet()
		if err != nil {
			return nil, err
		}
		f.selSet = sel
	}
	return f, nil
}

// parseArguments parses ( name: value, ... ).
func (p *parser) parseArguments() (map[string]value, error) {
	if err := p.next(); err != nil { // consume (
		return nil, err
	}
	args := map[string]value{}
	for p.tok.kind != tParenR {
		if p.tok.kind != tName {
			return nil, p.errf("expected argument name")
		}
		name := p.tok.value
		if err := p.next(); err != nil {
			return nil, err
		}
		if err := p.expect(tColon, "':'"); err != nil {
			return nil, err
		}
		v, err := p.parseValue(false)
		if err != nil {
			return nil, err
		}
		args[name] = v
	}
	if err := p.next(); err != nil { // consume )
		return nil, err
	}
	return args, nil
}

// parseValue parses a single input value literal. When constOnly is true,
// variable references ($x) are rejected (used for default values).
func (p *parser) parseValue(constOnly bool) (value, error) {
	switch p.tok.kind {
	case tDollar:
		if constOnly {
			return value{}, p.errf("variable not allowed in const value")
		}
		if err := p.next(); err != nil {
			return value{}, err
		}
		if p.tok.kind != tName {
			return value{}, p.errf("expected variable name")
		}
		v := value{kind: valVariable, enumVal: p.tok.value}
		if err := p.next(); err != nil {
			return value{}, err
		}
		return v, nil
	case tInt:
		v := value{kind: valInt, intVal: p.tok.value}
		if err := p.next(); err != nil {
			return value{}, err
		}
		return v, nil
	case tFloat:
		v := value{kind: valFloat, floatVal: p.tok.value}
		if err := p.next(); err != nil {
			return value{}, err
		}
		return v, nil
	case tString:
		v := value{kind: valString, strVal: p.tok.value}
		if err := p.next(); err != nil {
			return value{}, err
		}
		return v, nil
	case tName:
		switch p.tok.value {
		case "true", "false":
			v := value{kind: valBool, boolVal: p.tok.value == "true"}
			if err := p.next(); err != nil {
				return value{}, err
			}
			return v, nil
		case "null":
			if err := p.next(); err != nil {
				return value{}, err
			}
			return value{kind: valNull}, nil
		default:
			v := value{kind: valEnum, enumVal: p.tok.value}
			if err := p.next(); err != nil {
				return value{}, err
			}
			return v, nil
		}
	case tBracketL:
		return p.parseListValue(constOnly)
	case tBraceL:
		return p.parseObjectValue(constOnly)
	}
	return value{}, p.errf("expected value")
}

// parseListValue parses [ value, ... ].
func (p *parser) parseListValue(constOnly bool) (value, error) {
	if err := p.next(); err != nil { // consume [
		return value{}, err
	}
	v := value{kind: valList}
	for p.tok.kind != tBracketR {
		if p.tok.kind == tEOF {
			return value{}, p.errf("unexpected EOF in list value")
		}
		item, err := p.parseValue(constOnly)
		if err != nil {
			return value{}, err
		}
		v.listVal = append(v.listVal, item)
	}
	if err := p.next(); err != nil { // consume ]
		return value{}, err
	}
	return v, nil
}

// parseObjectValue parses { name: value, ... } (input object literal).
func (p *parser) parseObjectValue(constOnly bool) (value, error) {
	if err := p.next(); err != nil { // consume {
		return value{}, err
	}
	v := value{kind: valObject, objVal: map[string]value{}}
	for p.tok.kind != tBraceR {
		if p.tok.kind != tName {
			return value{}, p.errf("expected input object field name")
		}
		name := p.tok.value
		if err := p.next(); err != nil {
			return value{}, err
		}
		if err := p.expect(tColon, "':'"); err != nil {
			return value{}, err
		}
		fv, err := p.parseValue(constOnly)
		if err != nil {
			return value{}, err
		}
		v.objVal[name] = fv
	}
	if err := p.next(); err != nil { // consume }
		return value{}, err
	}
	return v, nil
}
