package main

import (
	"errors"
	"flag"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/moby/term"
	"github.com/schemigo/schemigo"
	"github.com/schemigo/schemigo/postgres"
)

const sqlWhitespace = " \t\r\n\f\v"

func useColor() bool { return os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" }

func migrationPrompt(allowCheck, color bool) string {
	yes := colorize("y", "\x1b[32m", color)
	no := colorize("n", "\x1b[31m", color)
	options := yes + " = apply, " + no + " = cancel"
	if allowCheck {
		options += ", " + colorize("c", "\x1b[35m", color) + " = check"
	}
	return "Apply migration? (" + options + "): "
}

func printNoChanges(out io.Writer) error {
	return printMessage(out, "No changes.", "\x1b[32m")
}

func reportError(out io.Writer, message string) int {
	_ = printMessage(out, message, "\x1b[31m")
	return 1
}

func parseFlags(flags *flag.FlagSet, args []string, diagnostics io.Writer) error {
	flags.SetOutput(diagnostics)
	if _, terminal := term.GetFdInfo(diagnostics); !terminal {
		return flags.Parse(args)
	}

	var messages strings.Builder
	flags.SetOutput(&messages)
	err := flags.Parse(args)
	flags.SetOutput(diagnostics)
	if errors.Is(err, flag.ErrHelp) {
		_, _ = io.WriteString(diagnostics, messages.String())
	} else if err != nil {
		reportError(diagnostics, strings.TrimSuffix(messages.String(), "\n"))
	}
	return err
}

func printMessage(out io.Writer, message, color string) error {
	if fd, terminal := term.GetFdInfo(out); terminal {
		columns := 80
		if size, err := term.GetWinsize(fd); err == nil && size.Width > 4 {
			columns = int(size.Width)
		}
		message = colorize(formatBox(message, columns-4), color, useColor())
	}
	_, err := io.WriteString(out, message+"\n")
	return err
}

// shortcut: count runes for alignment; use terminal cell widths for wide glyphs.
func formatBox(message string, maxWidth int) string {
	message = strings.ReplaceAll(message, "\r\n", "\n")
	message = strings.ReplaceAll(message, "\t", "    ")
	message = strings.Map(func(r rune) rune {
		if r != '\n' && unicode.IsControl(r) {
			return '�'
		}
		return r
	}, message)

	var lines [][]rune
	var width int
	for line := range strings.SplitSeq(message, "\n") {
		text := []rune(line)
		for len(text) > maxWidth {
			end := maxWidth
			for i := maxWidth; i > 0; i-- {
				if text[i] == ' ' {
					end = i
					break
				}
			}
			lines = append(lines, text[:end])
			width = max(width, end)
			text = text[end:]
			if text[0] == ' ' {
				text = text[1:]
			}
		}
		lines = append(lines, text)
		width = max(width, len(text))
	}

	border := strings.Repeat("─", width+2)
	var box strings.Builder
	box.WriteString("╭")
	box.WriteString(border)
	box.WriteString("╮\n")
	for _, line := range lines {
		box.WriteString("│ ")
		box.WriteString(string(line))
		box.WriteString(strings.Repeat(" ", width-len(line)))
		box.WriteString(" │\n")
	}
	box.WriteString("╰")
	box.WriteString(border)
	box.WriteString("╯")
	return box.String()
}

func printSQL(out io.Writer, plan schemigo.Plan, dialect string) error {
	if _, terminal := term.GetFdInfo(out); !terminal {
		_, err := io.WriteString(out, plan.SQL())
		return err
	}

	var preview strings.Builder
	color := useColor()
	for _, statement := range plan.Statements {
		command := strings.TrimSpace(statement.SQL)
		if command == "" {
			continue
		}
		if preview.Len() > 0 {
			preview.WriteByte('\n')
		}
		command = strings.TrimSuffix(command, ";")
		preview.WriteString(formatStatement(command, dialect, color))
		preview.WriteString(";\n")
	}
	_, err := io.WriteString(out, preview.String())
	return err
}

func formatStatement(sql, dialect string, color bool) string {
	return formatSQL(sql, dialect, 0, false, color)
}

func writeDOBody(out *strings.Builder, sql string, indent int, color bool) string {
	rest := strings.TrimLeft(sql, sqlWhitespace)
	if rest == "" {
		return sql
	}
	token := sqlToken(rest, "postgres")
	quoted, escaped := strings.CutPrefix(token, "E")
	if len(quoted) < 2 || quoted[0] != '\'' || !strings.HasSuffix(quoted, "'") {
		return sql
	}
	body := strings.ReplaceAll(quoted[1:len(quoted)-1], "''", "'")
	if escaped {
		body = strings.ReplaceAll(body, `\\`, `\`)
	}
	// Decode only the literal form emitted by the PostgreSQL driver.
	if postgres.QuoteLiteral(body) != token {
		return sql
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return sql
	}
	switch sqlToken(body, "postgres") {
	case "BEGIN", "DECLARE":
	default:
		return sql
	}
	formatted := formatSQL(body, "postgres", indent, true, color)
	tag := "$$"
	if indent > 0 {
		tag = "$body$"
	}
	for i := 1; strings.Contains(formatted, tag); i++ {
		tag = "$body_" + strconv.Itoa(i) + "$"
	}
	out.WriteByte(' ')
	out.WriteString(tag)
	out.WriteByte('\n')
	out.WriteString(formatted)
	out.WriteByte('\n')
	out.WriteString(strings.Repeat("  ", indent))
	out.WriteString(tag)
	return rest[len(token):]
}

// shortcut: lay out generated DDL and guard blocks with lexical rules; use a dialect parser for full SQL formatting.
func formatSQL(sql, dialect string, indent int, block, color bool) string {
	type subquery struct{ depth, indent int }
	var out strings.Builder
	var depth, cases, currentIndent int
	var table, declaring, condition bool
	var previous string
	var queries []subquery
	start, lineBreak := true, block
	for sql != "" {
		rest := strings.TrimLeft(sql, sqlWhitespace)
		if rest == "" {
			break
		}
		gap := sql[:len(sql)-len(rest)]
		token := sqlToken(rest, dialect)
		word := strings.ToUpper(token)
		nextStart := depth == 0 && token == ";"
		indentAfter := 0
		if block && depth == 0 && start {
			switch word {
			case "DECLARE":
				declaring = true
				indentAfter, nextStart = 1, true
			case "BEGIN":
				if declaring {
					indent--
					declaring = false
				}
				indentAfter, nextStart = 1, true
			case "IF":
				condition = true
			case "ELSIF":
				indent--
				condition = true
			case "ELSE":
				indent--
				indentAfter, nextStart = 1, true
			case "END":
				indent--
			}
		}
		if block && depth == 0 && condition {
			switch word {
			case "CASE":
				cases++
			case "END":
				cases--
			case "THEN":
				if cases == 0 {
					condition = false
					indentAfter, nextStart = 1, true
				}
			}
		}

		table = table || depth == 0 && strings.HasPrefix(rest, "CREATE TABLE ")
		if table && depth == 1 && token == ")" {
			lineBreak = true
			table = false
		}
		lineIndent := indent
		if table && depth == 1 {
			lineIndent++
		}
		if block && word == "SELECT" && previous == "(" {
			queries = append(queries, subquery{depth: depth, indent: currentIndent})
		}
		if block && !start && guardClause(word, previous) {
			lineBreak = true
			lineIndent = indent + depth
			if len(queries) > 0 {
				query := queries[len(queries)-1]
				lineIndent = query.indent + 1 + depth - query.depth
			}
			if word == "AND" || word == "OR" {
				lineIndent++
			}
		}
		if token == ")" && len(queries) > 0 && queries[len(queries)-1].depth == depth {
			lineBreak, lineIndent = true, queries[len(queries)-1].indent
			queries = queries[:len(queries)-1]
		}
		if block && depth == 0 && !start && guardDDLClause(rest) {
			lineBreak, lineIndent = true, indent+1
		}
		if lineBreak || block && strings.ContainsAny(gap, "\r\n") {
			if out.Len() > 0 {
				out.WriteByte('\n')
			}
			currentIndent = max(0, lineIndent)
			out.WriteString(strings.Repeat("  ", currentIndent))
		} else {
			out.WriteString(gap)
		}

		out.WriteString(colorize(token, tokenColor(token, word, dialect), color))
		sql = rest[len(token):]
		if start && depth == 0 && word == "DO" && dialect == "postgres" {
			sql = writeDOBody(&out, sql, indent, color)
		}
		switch token {
		case "(", "[":
			depth++
		case ")", "]":
			depth--
		}
		lineBreak = nextStart || table && depth == 1 && (token == "(" || token == ",")
		indent += indentAfter
		start, previous = nextStart, word
	}
	return out.String()
}

func guardClause(word, previous string) bool {
	switch word {
	case "SELECT":
		return previous == "("
	case "FROM":
		return previous != "DISTINCT"
	case "CROSS", "LEFT", "RIGHT", "INNER", "FULL", "WHERE", "AND", "OR":
		return true
	case "JOIN":
		switch previous {
		case "CROSS", "LEFT", "RIGHT", "INNER", "FULL", "OUTER":
			return false
		}
		return true
	}
	return false
}

func guardDDLClause(sql string) bool {
	for _, clause := range []string{"ADD COLUMN ", "ADD CONSTRAINT ", "ALTER COLUMN ", "DROP COLUMN ",
		"DROP CONSTRAINT ", "VALIDATE CONSTRAINT ", "REFERENCES ", "ON DELETE ", "ON UPDATE "} {
		if strings.HasPrefix(sql, clause) {
			return true
		}
	}
	return false
}

var dollarDelimiter = regexp.MustCompile(`^\$(?:[a-zA-Z_\x{0080}-\x{10FFFF}][a-zA-Z_0-9\x{0080}-\x{10FFFF}]*)?\$`)

func sqlToken(sql, dialect string) string {
	if sqlLineComment(sql, dialect) {
		end := strings.IndexAny(sql, "\r\n")
		if end >= 0 {
			return sql[:end]
		}
		return sql
	}
	if strings.HasPrefix(sql, "/*") {
		if dialect == "mysql" {
			if end := strings.Index(sql[2:], "*/"); end >= 0 {
				return sql[:end+4]
			}
			return sql
		}
		depth := 1
		for i := 2; i+1 < len(sql); i++ {
			switch sql[i : i+2] {
			case "/*":
				depth++
				i++
			case "*/":
				depth--
				if depth == 0 {
					return sql[:i+2]
				}
				i++
			}
		}
		return sql
	}
	if sql[0] == '$' && dialect != "mysql" {
		if delimiter := dollarDelimiter.FindString(sql); delimiter != "" {
			if end := strings.Index(sql[len(delimiter):], delimiter); end >= 0 {
				return sql[:end+2*len(delimiter)]
			}
			return sql
		}
	}
	if strings.ContainsRune("'\x22\x60", rune(sql[0])) {
		backslash := dialect == "clickhouse" || dialect == "mysql" && sql[0] != '`'
		return sql[:quotedEnd(sql, backslash)]
	}
	if dialect == "postgres" && (strings.HasPrefix(sql, "E'") || strings.HasPrefix(sql, "e'")) {
		return sql[:1+quotedEnd(sql[1:], true)]
	}
	end := 1
	if sqlWordByte(sql[0]) {
		for end < len(sql) && sqlWordByte(sql[end]) {
			end++
		}
	}
	return sql[:end]
}

func sqlLineComment(sql, dialect string) bool {
	if strings.HasPrefix(sql, "--") {
		return dialect != "mysql" || len(sql) == 2 || sql[2] <= ' '
	}
	return sql[0] == '#' && dialect != "postgres"
}

func quotedEnd(sql string, backslash bool) int {
	for i := 1; i < len(sql); i++ {
		if backslash && sql[i] == '\\' {
			i++
			continue
		}
		if sql[i] == sql[0] {
			if i+1 == len(sql) || sql[i+1] != sql[0] {
				return i + 1
			}
			i++
		}
	}
	return len(sql)
}

func sqlWordByte(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '$' || c >= 128
}

func tokenColor(token, word, dialect string) string {
	switch word {
	case "CREATE", "ADD":
		return "\x1b[32m"
	case "ALTER", "RENAME", "MODIFY":
		return "\x1b[33m"
	case "DROP", "TRUNCATE", "DELETE":
		return "\x1b[31m"
	case "ACTION", "AND", "AS", "BEGIN", "BY", "CASCADE", "CASE", "CHECK", "COLUMN", "COMMIT",
		"CONSTRAINT", "CROSS", "DECLARE", "DEFAULT", "DISTINCT", "DO", "ELSE", "ELSIF", "END",
		"ENGINE", "EXCEPTION", "EXECUTE", "EXISTS", "FALSE", "FOREIGN", "FROM", "FUNCTION", "GROUP",
		"IF", "INDEX", "INNER", "INSERT", "INTO", "IS", "JOIN", "KEY", "LANGUAGE", "LATERAL",
		"LEFT", "NO", "NOT", "NULL", "ON", "ONLY", "OR", "ORDER", "PARTITION", "PERFORM", "PREPARE",
		"PRIMARY", "RAISE", "REFERENCES", "REPLACE", "RETURN", "RETURNS", "RIGHT", "SELECT", "SET",
		"TABLE", "THEN", "TO", "TRUE", "TYPE", "UNIQUE", "UPDATE", "USING", "VALIDATE", "VALUES", "VIEW", "WHERE":
		return "\x1b[36m"
	}
	switch {
	case token[0] == '\'' || strings.HasPrefix(word, "E'"):
		return "\x1b[35m"
	case strings.HasPrefix(token, "/*") || sqlLineComment(token, dialect):
		return "\x1b[90m"
	}
	return ""
}

func colorize(text, code string, enabled bool) string {
	if !enabled || code == "" {
		return text
	}
	return code + text + "\x1b[0m"
}
