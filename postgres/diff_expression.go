package postgres

import (
	"regexp"
	"strings"
)

var dollarTag = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func sqlWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c >= 128
}

func sqlOperatorByte(c byte) bool { return strings.ContainsRune("+-*/<>=~!@#%^&|?:", rune(c)) }

// shortcut: normalize SQL tokens, not implicit casts; use Inspect spelling for complex expressions.
func expressionTokens(sql string) []string {
	var tokens []string
	for i := 0; i < len(sql); {
		if strings.ContainsRune(" \t\r\n\f", rune(sql[i])) {
			i++
			continue
		}
		start := i
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			continue
		case strings.HasPrefix(sql[i:], "/*"):
			i += 2
			depth := 1
			for i < len(sql) && depth > 0 {
				switch {
				case strings.HasPrefix(sql[i:], "/*"):
					depth++
					i += 2
				case strings.HasPrefix(sql[i:], "*/"):
					depth--
					i += 2
				default:
					i++
				}
			}
			continue
		case sql[i] == '\'' || sql[i] == '"':
			quote := sql[i]
			escaped := quote == '\'' && start > 0 && (sql[start-1] == 'E' || sql[start-1] == 'e')
			i++
			for i < len(sql) {
				if escaped && sql[i] == '\\' && i+1 < len(sql) {
					i += 2
					continue
				}
				if sql[i] == quote {
					i++
					if i < len(sql) && sql[i] == quote {
						i++
						continue
					}
					break
				}
				i++
			}
		case sql[i] == '$':
			if end := strings.IndexByte(sql[i+1:], '$'); end >= 0 {
				tag := sql[i : i+end+2]
				if end == 0 || dollarTag.MatchString(tag[1:len(tag)-1]) {
					if close := strings.Index(sql[i+len(tag):], tag); close >= 0 {
						i += len(tag)*2 + close
						break
					}
				}
			}
			i++
		default:
			i++
			if sqlWordByte(sql[start]) {
				for i < len(sql) && (sqlWordByte(sql[i]) || sql[i] == '$') {
					i++
				}
			} else if sqlOperatorByte(sql[start]) {
				for i < len(sql) && sqlOperatorByte(sql[i]) {
					if strings.HasPrefix(sql[i:], "--") || strings.HasPrefix(sql[i:], "/*") {
						break
					}
					i++
				}
			}
		}
		tokens = append(tokens, sql[start:i])
	}
	for len(tokens) >= 2 && tokens[0] == "(" && tokens[len(tokens)-1] == ")" {
		depth, wraps := 0, true
		for i, token := range tokens {
			if token == "(" {
				depth++
			}
			if token == ")" {
				depth--
			}
			if depth == 0 && i != len(tokens)-1 {
				wraps = false
				break
			}
		}
		if !wraps || depth != 0 {
			break
		}
		tokens = tokens[1 : len(tokens)-1]
	}
	return tokens
}

func expressionKey(sql string) string { return strings.Join(expressionTokens(sql), "\x00") }

func expressionPattern(sql string) string {
	tokens := expressionTokens(sql)
	var pattern strings.Builder
	pattern.WriteString("^[[:space:]]*[(]*[[:space:]]*")
	for i, token := range tokens {
		if i > 0 {
			prev := tokens[i-1][len(tokens[i-1])-1]
			separateWords := sqlWordByte(prev) && sqlWordByte(token[0])
			separateOperators := sqlOperatorByte(prev) && sqlOperatorByte(token[0])
			if separateWords || separateOperators {
				pattern.WriteString("[[:space:]]+")
			} else {
				pattern.WriteString("[[:space:]]*")
			}
		}
		pattern.WriteString(regexp.QuoteMeta(token))
	}
	pattern.WriteString("[[:space:]]*[)]*[[:space:]]*$")
	return pattern.String()
}
