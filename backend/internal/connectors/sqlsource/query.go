package sqlsource

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidateQuery checks statement boundaries; database transactions enforce read-only grammar.
func ValidateQuery(q, dialect string) error {
	if dialect == "" {
		return validateAnyDialect(q)
	}
	lexer := sqlLexer{query: q, dialect: dialect}
	first, previous := "", ""
	ended := false
	for {
		token, err := lexer.next()
		if err != nil {
			return err
		}
		if token == "" {
			break
		}
		if ended {
			return errors.New("only one SELECT or WITH statement is allowed")
		}
		if token == ";" {
			ended = true
			continue
		}
		if first == "" {
			first = token
			if token != "SELECT" && token != "WITH" {
				return errors.New("query must start with SELECT or WITH")
			}
		}
		if previous == "INTO" && (token == "OUTFILE" || token == "DUMPFILE") {
			return errors.New("SELECT INTO OUTFILE or DUMPFILE is not allowed")
		}
		previous = token
	}
	if first == "" {
		return errors.New("SQL query is required")
	}
	return nil
}

// Task configuration validation has no credential; execution validates its exact dialect.
func validateAnyDialect(q string) error {
	var firstError error
	for _, dialect := range []string{Postgres, MySQL, SQLite} {
		err := ValidateQuery(q, dialect)
		if err == nil {
			return nil
		}
		if firstError == nil {
			firstError = err
		}
	}
	return firstError
}

type sqlLexer struct {
	query, dialect string
	pos            int
}

func (l *sqlLexer) next() (string, error) {
	if err := l.skipTrivia(); err != nil {
		return "", err
	}
	if l.pos >= len(l.query) {
		return "", nil
	}
	ch := l.query[l.pos]
	if ch == '\'' || ch == '"' || ch == '`' || ch == '[' {
		return "\x00literal", l.skipQuoted()
	}
	if ch == '$' && (l.dialect == Postgres || l.dialect == "") {
		quoted, err := l.skipDollar()
		if quoted || err != nil {
			return "\x00literal", err
		}
	}
	if isWord(ch) || ch >= utf8.RuneSelf {
		return l.word(), nil
	}
	l.pos++
	return string(ch), nil
}

func (l *sqlLexer) word() string {
	start := l.pos
	for l.pos < len(l.query) {
		r, n := utf8.DecodeRuneInString(l.query[l.pos:])
		if !identifierRune(r) && r != '$' {
			break
		}
		l.pos += n
	}
	if l.pos == start {
		l.pos++
	} // A non-identifier Unicode symbol still consumes input.
	return strings.ToUpper(l.query[start:l.pos])
}

func (l *sqlLexer) skipTrivia() error {
	for l.pos < len(l.query) {
		r, n := utf8.DecodeRuneInString(l.query[l.pos:])
		switch {
		case unicode.IsSpace(r):
			l.pos += n
		case strings.HasPrefix(l.query[l.pos:], "--"), r == '#' && (l.dialect == MySQL || l.dialect == ""):
			for l.pos < len(l.query) && l.query[l.pos] != '\n' {
				l.pos++
			}
		case strings.HasPrefix(l.query[l.pos:], "/*"):
			if err := l.skipComment(); err != nil {
				return err
			}
		default:
			return nil
		}
	}
	return nil
}

func (l *sqlLexer) skipComment() error {
	l.pos += 2
	if strings.HasPrefix(l.query[l.pos:], "!") || strings.HasPrefix(strings.ToUpper(l.query[l.pos:]), "M!") {
		return errors.New("executable SQL comments are not allowed")
	}
	depth := 1
	for l.pos < len(l.query) && depth > 0 {
		switch {
		case strings.HasPrefix(l.query[l.pos:], "/*"):
			if l.dialect == MySQL || l.dialect == SQLite {
				return errors.New("nested comments are not supported for this database")
			}
			depth++
			l.pos += 2
		case strings.HasPrefix(l.query[l.pos:], "*/"):
			depth--
			l.pos += 2
		default:
			l.pos++
		}
	}
	if depth != 0 {
		return errors.New("unterminated SQL comment")
	}
	return nil
}

func (l *sqlLexer) escapeBackslash(quote byte) bool {
	if l.dialect == MySQL || l.dialect == "" {
		return quote != '['
	}
	if l.dialect != Postgres || quote != '\'' || l.pos == 0 {
		return false
	}
	prefix := l.query[l.pos-1]
	if prefix != 'e' && prefix != 'E' {
		return false
	}
	if l.pos == 1 {
		return true
	}
	previous, _ := utf8.DecodeLastRuneInString(l.query[:l.pos-1])
	return !identifierRune(previous) && previous != '$'
}

func (l *sqlLexer) skipQuoted() error {
	quote := l.query[l.pos]
	close := quote
	if quote == '[' {
		close = ']'
	}
	escaped := l.escapeBackslash(quote)
	l.pos++
	for l.pos < len(l.query) {
		if escaped && l.query[l.pos] == '\\' {
			l.pos += 2
			continue
		}
		if l.query[l.pos] == close {
			l.pos++
			if l.pos < len(l.query) && l.query[l.pos] == close {
				l.pos++
				continue
			}
			return nil
		}
		l.pos++
	}
	return errors.New("unterminated SQL string or identifier")
}

func (l *sqlLexer) skipDollar() (bool, error) {
	j := l.pos + 1
	for j < len(l.query) {
		r, n := utf8.DecodeRuneInString(l.query[j:])
		if !identifierRune(r) || j == l.pos+1 && unicode.IsDigit(r) {
			break
		}
		j += n
	}
	if j >= len(l.query) || l.query[j] != '$' {
		return false, nil
	}
	tag := l.query[l.pos : j+1]
	end := strings.Index(l.query[j+1:], tag)
	if end < 0 {
		return true, errors.New("unterminated dollar-quoted string")
	}
	l.pos = j + 1 + end + len(tag)
	return true, nil
}

func identifierRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }
func isWord(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}
