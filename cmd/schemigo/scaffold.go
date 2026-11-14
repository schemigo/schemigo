package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/schemigo/schemigo"
)

func runScaffold(ctx context.Context, args []string, out, diagnostics io.Writer) int {
	command := args[0]
	flags := flag.NewFlagSet("schemigo "+command, flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	dir := flags.String("dir", "./schema", "Go schema directory")
	schemaName := flags.String("schema", "public", "database schema for a new directory")
	var materialized *bool
	if command == "view" {
		materialized = flags.Bool("materialized", false, "create a materialized view")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return 1
	}
	if flags.NArg() != 2 {
		fmt.Fprintf(diagnostics, "schemigo %s: expected name and definition\n", command)
		return 1
	}
	name, definition := flags.Arg(0), flags.Arg(1)
	addition := schemigo.Schema{Name: *schemaName}
	var err error
	switch command {
	case "table":
		var columns []string
		columns, err = splitList(definition)
		if err == nil {
			table := schemigo.Table{Name: name}
			for _, item := range columns {
				columnName, columnType, ok := strings.Cut(item, ":")
				if !ok || strings.TrimSpace(columnName) == "" || strings.TrimSpace(columnType) == "" {
					err = fmt.Errorf("invalid column %q; expected name:type", item)
					break
				}
				table.Columns = append(table.Columns, schemigo.Column{Name: strings.TrimSpace(columnName), Type: strings.TrimSpace(columnType)})
			}
			addition.Tables = []schemigo.Table{table}
		}
	case "enum":
		var values []string
		values, err = splitList(definition)
		addition.Enums = []schemigo.Enum{{Name: name, Values: values}}
	case "view":
		var sql []byte
		sql, err = os.ReadFile(definition)
		if err == nil && strings.TrimSpace(string(sql)) == "" {
			err = fmt.Errorf("SQL definition is empty")
		}
		addition.Views = []schemigo.View{{Name: name, Definition: string(sql), Materialized: *materialized}}
	}
	if err == nil {
		err = schemigo.Scaffold(ctx, *dir, addition)
	}
	if err != nil {
		fmt.Fprintln(diagnostics, "schemigo:", err)
		return 1
	}
	fmt.Fprintf(out, "Created %s %s in %s\n", command, name, *dir)
	return 0
}

func splitList(input string) ([]string, error) {
	var result []string
	depth, start := 0, 0
	for i, char := range input {
		switch char {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("unexpected ')' in %q", input)
			}
		case ',':
			if depth == 0 {
				part := strings.TrimSpace(input[start:i])
				if part == "" {
					return nil, fmt.Errorf("empty list item in %q", input)
				}
				result = append(result, part)
				start = i + 1
			}
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("unclosed '(' in %q", input)
	}
	part := strings.TrimSpace(input[start:])
	if part == "" {
		return nil, fmt.Errorf("empty list item in %q", input)
	}
	return append(result, part), nil
}
