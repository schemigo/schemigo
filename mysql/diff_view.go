package mysql

import "strings"

func sameViewDefinition(current, desired, schema string) bool {
	return canonicalViewSQL(current, schema) == canonicalViewSQL(desired, schema)
}

func canonicalViewSQL(definition, schema string) string {
	definition = strings.TrimSuffix(strings.TrimSpace(definition), ";")
	var normalized strings.Builder
	inString, escaped := false, false
	for _, char := range definition {
		switch {
		case escaped:
			escaped = false
		case char == '\\' && inString:
			escaped = true
		case char == '\'':
			inString = !inString
		case char == '`' && !inString:
			continue
		}
		if inString || char == '\'' {
			normalized.WriteRune(char)
		} else {
			normalized.WriteRune(toLowerASCII(char))
		}
	}
	text := stripSchemaQualifier(normalized.String(), strings.ToLower(schema)+".")
	fields := sqlFields(text)
	from, count := -1, 0
	for i, field := range fields {
		if field == "from" {
			from, count = i, count+1
		}
		if field == "join" {
			count++
		}
	}
	if count == 1 && from+1 < len(fields) && from >= 0 && singleViewTable(fields[from+1:]) {
		table := fields[from+1]
		for i := range fields {
			fields[i] = strings.TrimPrefix(fields[i], table+".")
		}
		for i := 1; i+1 < from; i++ {
			if fields[i] == "as" && fields[i-1] == strings.TrimSuffix(fields[i+1], ",") {
				fields[i-1] += strings.TrimPrefix(fields[i+1], fields[i-1])
				fields = append(fields[:i], fields[i+2:]...)
				from -= 2
				i--
			}
		}
	}
	return strings.Join(fields, " ")
}

func singleViewTable(fields []string) bool {
	for _, field := range fields {
		switch field {
		case "where", "group", "order", "having", "limit":
			return true
		case ",":
			return false
		}
		if strings.HasSuffix(field, ",") {
			return false
		}
	}
	return true
}

func sqlFields(text string) []string {
	var fields []string
	var field strings.Builder
	inString, escaped := false, false
	for _, char := range text {
		if !inString && (char == ' ' || char == '\n' || char == '\t' || char == '\r') {
			if field.Len() > 0 {
				fields = append(fields, field.String())
				field.Reset()
			}
			continue
		}
		field.WriteRune(char)
		switch {
		case escaped:
			escaped = false
		case char == '\\' && inString:
			escaped = true
		case char == '\'':
			inString = !inString
		}
	}
	if field.Len() > 0 {
		fields = append(fields, field.String())
	}
	return fields
}

func stripSchemaQualifier(text, prefix string) string {
	var result strings.Builder
	inString, escaped := false, false
	for i := 0; i < len(text); i++ {
		char := text[i]
		switch {
		case escaped:
			escaped = false
		case char == '\\' && inString:
			escaped = true
		case char == '\'':
			inString = !inString
		}
		if !inString && char != '\'' && strings.HasPrefix(text[i:], prefix) &&
			(i == 0 || !isIdentifierByte(text[i-1])) {
			i += len(prefix) - 1
			continue
		}
		result.WriteByte(char)
	}
	return result.String()
}

func isIdentifierByte(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '_'
}

func toLowerASCII(char rune) rune {
	if char >= 'A' && char <= 'Z' {
		return char + ('a' - 'A')
	}
	return char
}
