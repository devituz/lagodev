package graphql

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// tokenKind enumerates the lexical token categories of the GraphQL query
// language subset this package understands.
type tokenKind int

const (
	tEOF tokenKind = iota
	tName
	tInt
	tFloat
	tString
	tBraceL   // {
	tBraceR   // }
	tParenL   // (
	tParenR   // )
	tBracketL // [
	tBracketR // ]
	tColon    // :
	tDollar   // $
	tEquals   // =
	tBang     // !
	tSpread   // ...
)

// token is a single lexical unit with its source position (for error paths).
type token struct {
	kind  tokenKind
	value string
	line  int
	col   int
}

// lexer turns a GraphQL source string into a stream of tokens. It is a simple
// hand-written scanner: GraphQL ignores commas and whitespace and uses # for
// line comments, which keeps this tractable without a generated lexer.
type lexer struct {
	src  string
	pos  int
	line int
	col  int
}

func newLexer(src string) *lexer { return &lexer{src: src, line: 1, col: 1} }

// advance consumes one rune, tracking line/column.
func (l *lexer) advance() rune {
	r, w := utf8.DecodeRuneInString(l.src[l.pos:])
	l.pos += w
	if r == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return r
}

// peek returns the next rune without consuming it (0 at EOF).
func (l *lexer) peek() rune {
	if l.pos >= len(l.src) {
		return 0
	}
	r, _ := utf8.DecodeRuneInString(l.src[l.pos:])
	return r
}

// skipIgnored consumes whitespace, commas, BOM and # comments.
func (l *lexer) skipIgnored() {
	for l.pos < len(l.src) {
		r := l.peek()
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == ',' || r == '\uFEFF':
			l.advance()
		case r == '#':
			for l.pos < len(l.src) && l.peek() != '\n' {
				l.advance()
			}
		default:
			return
		}
	}
}

// next returns the next token, or an error for malformed input.
func (l *lexer) next() (token, error) {
	l.skipIgnored()
	line, col := l.line, l.col
	if l.pos >= len(l.src) {
		return token{kind: tEOF, line: line, col: col}, nil
	}
	r := l.peek()
	switch {
	case r == '{':
		l.advance()
		return token{tBraceL, "{", line, col}, nil
	case r == '}':
		l.advance()
		return token{tBraceR, "}", line, col}, nil
	case r == '(':
		l.advance()
		return token{tParenL, "(", line, col}, nil
	case r == ')':
		l.advance()
		return token{tParenR, ")", line, col}, nil
	case r == '[':
		l.advance()
		return token{tBracketL, "[", line, col}, nil
	case r == ']':
		l.advance()
		return token{tBracketR, "]", line, col}, nil
	case r == ':':
		l.advance()
		return token{tColon, ":", line, col}, nil
	case r == '$':
		l.advance()
		return token{tDollar, "$", line, col}, nil
	case r == '=':
		l.advance()
		return token{tEquals, "=", line, col}, nil
	case r == '!':
		l.advance()
		return token{tBang, "!", line, col}, nil
	case r == '.':
		if strings.HasPrefix(l.src[l.pos:], "...") {
			l.advance()
			l.advance()
			l.advance()
			return token{tSpread, "...", line, col}, nil
		}
		return token{}, fmt.Errorf("graphql: unexpected '.' at %d:%d (expected '...')", line, col)
	case r == '"':
		return l.lexString(line, col)
	case r == '-' || (r >= '0' && r <= '9'):
		return l.lexNumber(line, col)
	case unicode.IsLetter(r) || r == '_':
		return l.lexName(line, col)
	}
	return token{}, fmt.Errorf("graphql: unexpected character %q at %d:%d", r, line, col)
}

// lexName scans a Name: /[_A-Za-z][_0-9A-Za-z]*/.
func (l *lexer) lexName(line, col int) (token, error) {
	start := l.pos
	for l.pos < len(l.src) {
		r := l.peek()
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			l.advance()
		} else {
			break
		}
	}
	return token{tName, l.src[start:l.pos], line, col}, nil
}

// lexNumber scans an Int or Float literal.
func (l *lexer) lexNumber(line, col int) (token, error) {
	start := l.pos
	isFloat := false
	if l.peek() == '-' {
		l.advance()
	}
	for l.pos < len(l.src) && l.peek() >= '0' && l.peek() <= '9' {
		l.advance()
	}
	if l.peek() == '.' {
		isFloat = true
		l.advance()
		for l.pos < len(l.src) && l.peek() >= '0' && l.peek() <= '9' {
			l.advance()
		}
	}
	if r := l.peek(); r == 'e' || r == 'E' {
		isFloat = true
		l.advance()
		if r := l.peek(); r == '+' || r == '-' {
			l.advance()
		}
		for l.pos < len(l.src) && l.peek() >= '0' && l.peek() <= '9' {
			l.advance()
		}
	}
	text := l.src[start:l.pos]
	if isFloat {
		return token{tFloat, text, line, col}, nil
	}
	return token{tInt, text, line, col}, nil
}

// lexString scans a double-quoted string, supporting the standard JSON escape
// sequences and \uXXXX. Block strings (""") are not supported.
func (l *lexer) lexString(line, col int) (token, error) {
	l.advance() // opening quote
	var b strings.Builder
	for l.pos < len(l.src) {
		r := l.advance()
		switch r {
		case '"':
			return token{tString, b.String(), line, col}, nil
		case '\n':
			return token{}, fmt.Errorf("graphql: unterminated string at %d:%d", line, col)
		case '\\':
			e := l.advance()
			switch e {
			case '"':
				b.WriteRune('"')
			case '\\':
				b.WriteRune('\\')
			case '/':
				b.WriteRune('/')
			case 'n':
				b.WriteRune('\n')
			case 't':
				b.WriteRune('\t')
			case 'r':
				b.WriteRune('\r')
			case 'b':
				b.WriteRune('\b')
			case 'f':
				b.WriteRune('\f')
			case 'u':
				if l.pos+4 > len(l.src) {
					return token{}, fmt.Errorf("graphql: bad unicode escape at %d:%d", line, col)
				}
				hex := l.src[l.pos : l.pos+4]
				var cp int
				if _, err := fmt.Sscanf(hex, "%04x", &cp); err != nil {
					return token{}, fmt.Errorf("graphql: bad unicode escape %q at %d:%d", hex, line, col)
				}
				for i := 0; i < 4; i++ {
					l.advance()
				}
				b.WriteRune(rune(cp))
			default:
				return token{}, fmt.Errorf("graphql: invalid escape \\%c at %d:%d", e, line, col)
			}
		default:
			b.WriteRune(r)
		}
	}
	return token{}, fmt.Errorf("graphql: unterminated string at %d:%d", line, col)
}
