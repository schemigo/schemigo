package schemigo

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// Export inspects schemaName and writes editable Go definitions into dir.
// dir must be absent or empty and inside the current Go module; existing files
// are never overwritten. Each entity has its own file, with type aliases and
// an All slice in its package. main.go emits the schema as JSON for Load.
// Extension versions are intentionally unpinned. Order and other modeled fields
// are preserved. Export closes its acquired connection, not the caller's pool.
// Export reads the database but never applies a migration.
func Export(ctx context.Context, db *sql.DB, driver Driver, schemaName, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if db == nil || driver == nil {
		return fmt.Errorf("export schema: database and driver are required")
	}
	location, err := schemaLocation(ctx, dir)
	if err != nil {
		return fmt.Errorf("export schema: %w", err)
	}
	if err := emptyExportDirectory(location.dir); err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("export schema: acquire connection: %w", err)
	}
	schema, inspectErr := driver.Inspect(ctx, conn, schemaName)
	if err := errors.Join(inspectErr, conn.Close()); err != nil {
		return fmt.Errorf("export schema: inspect: %w", err)
	}
	if schema.Name != schemaName {
		return fmt.Errorf("export schema: inspected namespace %q differs from requested %q", schema.Name, schemaName)
	}
	return publishSchema(ctx, schema, location.dir, true)
}

// publishSchema writes an inspected schema without acquiring another connection.
func publishSchema(ctx context.Context, schema Schema, dir string, unpinVersions bool) error {
	location, err := schemaLocation(ctx, dir)
	if err != nil {
		return fmt.Errorf("export schema: %w", err)
	}
	schema, err = Normalize(schema)
	if err != nil {
		return fmt.Errorf("export schema: %w", err)
	}
	if unpinVersions {
		for i := range schema.Extensions {
			schema.Extensions[i].Version = ""
		}
	}
	files, err := schemaFiles(schema, location.importPath)
	if err != nil {
		return fmt.Errorf("export schema: %w", err)
	}
	return publishSchemaFiles(ctx, location.dir, files)
}

func publishSchemaFiles(ctx context.Context, dir string, files map[string][]byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return fmt.Errorf("export schema: %w", err)
	}
	staging, err := os.MkdirTemp(parent, ".schemigo-export-")
	if err != nil {
		return fmt.Errorf("export schema: %w", err)
	}
	defer os.RemoveAll(staging)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(staging, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return fmt.Errorf("export schema: %w", err)
		}
		if err := os.WriteFile(path, files[name], 0644); err != nil {
			return fmt.Errorf("export schema: write %s: %w", name, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := emptyExportDirectory(dir); err != nil {
		return err
	}
	// Remove only an empty directory, never a tree. Rename publishes the complete
	// output; if another writer filled the destination, removal/rename fails.
	if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("export schema: publish: %w", err)
	}
	if err := os.Rename(staging, dir); err != nil {
		return fmt.Errorf("export schema: publish: %w", err)
	}
	return nil
}

func emptyExportDirectory(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("export directory: %w", err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("export directory %q is not empty", dir)
	}
	return nil
}

type entityGroup struct {
	pkg, typ string
	values   any
	aliases  []string
}

func schemaFiles(schema Schema, importPath string) (map[string][]byte, error) {
	groups := []entityGroup{
		{"extensions", "Extension", schema.Extensions, []string{"Extension"}},
		{"enums", "Enum", schema.Enums, []string{"Enum"}},
		{"tables", "Table", schema.Tables, []string{"Table", "Column", "Index", "ForeignKey", "Check"}},
		{"functions", "Function", schema.Functions, []string{"Function"}},
		{"triggers", "Trigger", schema.Triggers, []string{"Trigger"}},
		{"views", "View", schema.Views, []string{"View"}},
	}
	files := map[string][]byte{}
	add := func(path, source string) error {
		data, err := format.Source([]byte(source))
		if err != nil {
			return fmt.Errorf("format %s: %w", path, err)
		}
		files[path] = data
		return nil
	}
	for _, group := range groups {
		var aliases strings.Builder
		fmt.Fprintf(&aliases, "// Package %s declares the desired schema %s.\npackage %s\nimport \"github.com/schemigo/schemigo\"\n", group.pkg, group.pkg, group.pkg)
		reserved := map[string]bool{"All": true}
		for _, name := range group.aliases {
			reserved[name] = true
			fmt.Fprintf(&aliases, "// %s is the schema model type.\ntype %s = schemigo.%s\n", name, name, name)
		}
		if err := add(filepath.Join(group.pkg, "types.go"), aliases.String()); err != nil {
			return nil, err
		}
		values := reflect.ValueOf(group.values)
		names := make([]string, values.Len())
		symbols := make([]string, values.Len())
		keys := make([]string, values.Len())
		counts := map[string]int{}
		for i := 0; i < values.Len(); i++ {
			value := values.Index(i)
			name := value.FieldByName("Name").String()
			names[i], symbols[i] = entityNames(name)
			if reserved[symbols[i]] {
				symbols[i] = "Entity" + symbols[i]
			}
			keys[i] = name
			for _, extra := range []string{"Parameters", "Table"} {
				if field := value.FieldByName(extra); field.IsValid() && field.Kind() == reflect.String {
					keys[i] += "\x00" + field.String()
				}
			}
			counts[strings.ToLower(names[i])]++
		}
		var all strings.Builder
		fmt.Fprintf(&all, "package %s\n\n// All contains every declared %s in schema order.\nvar All = []%s{\n", group.pkg, group.typ, group.typ)
		usedSymbols := map[string]bool{}
		for i := 0; i < values.Len(); i++ {
			if counts[strings.ToLower(names[i])] > 1 || usedSymbols[symbols[i]] {
				sum := sha256.Sum256([]byte(keys[i]))
				suffix := "_" + hex.EncodeToString(sum[:6])
				names[i] += suffix
				symbols[i] += suffix
			}
			if usedSymbols[symbols[i]] || reserved[symbols[i]] {
				return nil, fmt.Errorf("entity identifier collision: %q", keys[i])
			}
			usedSymbols[symbols[i]] = true
			var source strings.Builder
			fmt.Fprintf(&source, "package %s\n\n// %s declares %s.\nvar %s = ", group.pkg, symbols[i], strconv.Quote(values.Index(i).FieldByName("Name").String()), symbols[i])
			writeGoValue(&source, values.Index(i))
			source.WriteByte('\n')
			filename := filepath.Join(group.pkg, names[i]+"_schema.go")
			if _, exists := files[filename]; exists {
				return nil, fmt.Errorf("entity filename collision: %q", keys[i])
			}
			if err := add(filename, source.String()); err != nil {
				return nil, err
			}
			fmt.Fprintf(&all, "%s,\n", symbols[i])
		}
		all.WriteString("}\n")
		if err := add(filepath.Join(group.pkg, "all.go"), all.String()); err != nil {
			return nil, err
		}
	}
	var main strings.Builder
	main.WriteString("// Schema entry point generated by schemigo.\npackage main\nimport (\n\"encoding/json\"\n\"fmt\"\n\"os\"\n\"github.com/schemigo/schemigo\"\n")
	for _, group := range groups {
		fmt.Fprintf(&main, "%q\n", importPath+"/"+group.pkg)
	}
	main.WriteString(")\nfunc main(){\nschema := schemigo.Schema{\n")
	fmt.Fprintf(&main, "Name: %q,\nOrder: []schemigo.EntityKind{", schema.Name)
	for _, kind := range schema.Order {
		fmt.Fprintf(&main, "%q,", kind)
	}
	main.WriteString("},\n")
	for _, field := range []struct{ name, pkg string }{{"Extensions", "extensions"}, {"Enums", "enums"}, {"Tables", "tables"}, {"Functions", "functions"}, {"Triggers", "triggers"}, {"Views", "views"}} {
		fmt.Fprintf(&main, "%s: %s.All,\n", field.name, field.pkg)
	}
	main.WriteString("}\nif err:=json.NewEncoder(os.Stdout).Encode(schema);err!=nil{fmt.Fprintln(os.Stderr,err);os.Exit(1)}\n}\n")
	if err := add("main.go", main.String()); err != nil {
		return nil, err
	}
	return files, nil
}

func entityNames(name string) (filename, symbol string) {
	var slug strings.Builder
	for _, c := range strings.ToLower(name) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			slug.WriteRune(c)
		} else {
			slug.WriteByte('_')
		}
	}
	filename = strings.Trim(slug.String(), "_")
	if len(filename) > 60 {
		filename = filename[:60]
	}
	if filename == "" {
		filename = "entity"
	}
	for _, part := range strings.FieldsFunc(filename, func(c rune) bool { return c == '_' }) {
		symbol += strings.ToUpper(part[:1]) + part[1:]
	}
	if symbol[0] >= '0' && symbol[0] <= '9' {
		symbol = "Entity" + symbol
	}
	return filename, symbol
}

// The model contains only structs, slices, strings, booleans and integers.
// Rendering all exported fields keeps new model fields in future round trips.
func writeGoValue(out *strings.Builder, value reflect.Value) {
	switch value.Kind() {
	case reflect.Struct:
		out.WriteString(value.Type().Name())
		out.WriteString("{\n")
		for i := 0; i < value.NumField(); i++ {
			if !value.Field(i).IsZero() {
				out.WriteString(value.Type().Field(i).Name)
				out.WriteByte(':')
				writeGoValue(out, value.Field(i))
				out.WriteString(",\n")
			}
		}
		out.WriteByte('}')
	case reflect.Slice:
		out.WriteString("[]")
		out.WriteString(value.Type().Elem().Name())
		out.WriteString("{\n")
		for i := 0; i < value.Len(); i++ {
			writeGoValue(out, value.Index(i))
			out.WriteString(",\n")
		}
		out.WriteByte('}')
	case reflect.String:
		out.WriteString(strconv.Quote(value.String()))
	case reflect.Bool:
		out.WriteString(strconv.FormatBool(value.Bool()))
	case reflect.Int:
		out.WriteString(strconv.FormatInt(value.Int(), 10))
	}
}
