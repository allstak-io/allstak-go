package allstak

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"unicode"
)

// NormalizeSQL collapses a SQL statement into a shape suitable for
// grouping identical queries across calls:
//
//   - string and number literals are replaced with "?"
//   - whitespace runs are collapsed to single spaces
//   - leading/trailing whitespace is trimmed
//
// This is intentionally a light-touch normalizer. It is NOT a full SQL
// parser — it's designed to be fast, allocation-conscious, and dialect
// agnostic. If a customer needs pgbouncer-level normalization they should
// pre-process the query themselves before passing it to CaptureDBQuery.
func NormalizeSQL(sql string) string {
	if sql == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(sql))

	inSingle := false
	inDouble := false
	inLineComment := false
	inBlockComment := false
	lastWasSpace := true // trim leading whitespace

	runes := []rune(sql)
	for i := 0; i < len(runes); i++ {
		r := runes[i]

		// Comment handling.
		if inLineComment {
			if r == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			if r == '*' && i+1 < len(runes) && runes[i+1] == '/' {
				inBlockComment = false
				i++
			}
			continue
		}
		if !inSingle && !inDouble {
			if r == '-' && i+1 < len(runes) && runes[i+1] == '-' {
				inLineComment = true
				i++
				continue
			}
			if r == '/' && i+1 < len(runes) && runes[i+1] == '*' {
				inBlockComment = true
				i++
				continue
			}
		}

		// String literal handling — replace with "?".
		if r == '\'' && !inDouble {
			if inSingle {
				// Handle doubled '' escape: if next rune is also ', stay in string.
				if i+1 < len(runes) && runes[i+1] == '\'' {
					i++
					continue
				}
				inSingle = false
			} else {
				inSingle = true
				b.WriteByte('?')
				lastWasSpace = false
			}
			continue
		}
		if inSingle {
			continue
		}

		// Double-quoted identifier handling — preserve but don't treat as literal.
		if r == '"' && !inSingle {
			inDouble = !inDouble
			b.WriteRune(r)
			lastWasSpace = false
			continue
		}
		if inDouble {
			b.WriteRune(r)
			continue
		}

		// Numeric literal handling — replace with "?".
		if isDigit(r) && (b.Len() == 0 || isBoundary(rune(b.String()[b.Len()-1]))) {
			// Consume the whole numeric run (including decimals).
			for i < len(runes) && (isDigit(runes[i]) || runes[i] == '.') {
				i++
			}
			i--
			b.WriteByte('?')
			lastWasSpace = false
			continue
		}

		// Whitespace collapsing.
		if unicode.IsSpace(r) {
			if !lastWasSpace {
				b.WriteByte(' ')
				lastWasSpace = true
			}
			continue
		}

		b.WriteRune(r)
		lastWasSpace = false
	}

	out := b.String()
	// Trim any trailing space left over from collapsed whitespace.
	return strings.TrimRight(out, " ")
}

// HashSQL returns a stable hex hash of a normalized SQL string. The
// dashboard groups queries by this hash so identical parameterized queries
// fold into a single entry.
func HashSQL(normalized string) string {
	sum := sha1.Sum([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

// ClassifySQL returns the query verb (SELECT, INSERT, UPDATE, DELETE, ...)
// for a raw or normalized SQL string, or "OTHER" if the first token
// isn't recognized. This is used for the dashboard's query type filter.
func ClassifySQL(sql string) string {
	s := strings.TrimLeft(sql, " \t\n\r;(")
	upper := strings.ToUpper(s)
	switch {
	case strings.HasPrefix(upper, "SELECT"):
		return "SELECT"
	case strings.HasPrefix(upper, "INSERT"):
		return "INSERT"
	case strings.HasPrefix(upper, "UPDATE"):
		return "UPDATE"
	case strings.HasPrefix(upper, "DELETE"):
		return "DELETE"
	case strings.HasPrefix(upper, "CREATE"):
		return "CREATE"
	case strings.HasPrefix(upper, "DROP"):
		return "DROP"
	case strings.HasPrefix(upper, "ALTER"):
		return "ALTER"
	case strings.HasPrefix(upper, "WITH"):
		return "SELECT" // CTE usually backs a SELECT
	case strings.HasPrefix(upper, "BEGIN"), strings.HasPrefix(upper, "COMMIT"), strings.HasPrefix(upper, "ROLLBACK"):
		return "TRANSACTION"
	default:
		return "OTHER"
	}
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// isBoundary reports whether a character can precede a numeric literal.
// Identifiers can't — we don't want to turn `col1` into `col?`.
func isBoundary(r rune) bool {
	return unicode.IsSpace(r) || r == '(' || r == ',' || r == '=' || r == '<' || r == '>' || r == '+' || r == '-' || r == '*' || r == '/'
}
