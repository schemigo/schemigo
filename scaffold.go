package schemigo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Scaffold adds one table, enum or view to an exported Go schema directory.
// A missing or empty directory is initialized with the same layout as Export.
// Existing entity files are never overwritten; view SQL remains editable.
func Scaffold(ctx context.Context, dir string, addition Schema) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	count := len(addition.Tables) + len(addition.Enums) + len(addition.Views)
	if count != 1 || len(addition.Extensions)+len(addition.Functions)+len(addition.Triggers) != 0 {
		return fmt.Errorf("scaffold: exactly one table, enum or view is required")
	}
	addition, err := Normalize(addition)
	if err != nil {
		return fmt.Errorf("scaffold: %w", err)
	}
	if len(addition.Views) == 1 && strings.TrimSpace(addition.Views[0].Definition) == "" {
		return fmt.Errorf("scaffold: SQL definition is empty")
	}
	location, err := schemaLocation(ctx, dir)
	if err != nil {
		return fmt.Errorf("scaffold: %w", err)
	}
	group := "tables"
	name := ""
	switch {
	case len(addition.Enums) == 1:
		group, name = "enums", addition.Enums[0].Name
	case len(addition.Tables) == 1:
		name = addition.Tables[0].Name
	case len(addition.Views) == 1:
		group, name = "views", addition.Views[0].Name
	}
	entries, err := os.ReadDir(location.dir)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(entries) == 0) {
		files, err := schemaFiles(addition, location.importPath)
		if err != nil {
			return fmt.Errorf("scaffold: %w", err)
		}
		if err := embedScaffoldView(files, addition.Views); err != nil {
			return err
		}
		return publishSchemaFiles(ctx, location.dir, files)
	} else if err != nil {
		return fmt.Errorf("scaffold: %w", err)
	}
	existing, err := Load(ctx, location.dir)
	if err != nil {
		return fmt.Errorf("scaffold: %w", err)
	}
	merged := existing
	merged.Enums = append(merged.Enums, addition.Enums...)
	merged.Tables = append(merged.Tables, addition.Tables...)
	merged.Views = append(merged.Views, addition.Views...)
	if _, err := Normalize(merged); err != nil {
		return fmt.Errorf("scaffold: %w", err)
	}
	single, err := schemaFiles(addition, location.importPath)
	if err != nil {
		return fmt.Errorf("scaffold: %w", err)
	}
	entityPath := filepath.Join(group, entityFilename(name))
	entityData := single[entityPath]
	allData, err := appendToAll(filepath.Join(location.dir, group, "all.go"), entityData)
	if err != nil {
		return err
	}
	if len(addition.Views) == 1 {
		if err := embedScaffoldView(single, addition.Views); err != nil {
			return err
		}
		entityData = single[entityPath]
	}
	if err := checkScaffoldNames(filepath.Join(location.dir, group), entityData); err != nil {
		return err
	}
	return appendScaffoldFiles(ctx, location.dir, group, entityPath, entityData, allData, single)
}

func entityFilename(name string) string {
	filename, _ := entityNames(name)
	return filename + "_schema.go"
}

func appendToAll(path string, entity []byte) ([]byte, error) {
	fileSet := token.NewFileSet()
	declaration, err := parser.ParseFile(fileSet, "entity.go", entity, 0)
	if err != nil {
		return nil, fmt.Errorf("scaffold: parse entity: %w", err)
	}
	symbol := ""
	for _, item := range declaration.Decls {
		if decl, ok := item.(*ast.GenDecl); ok && decl.Tok == token.VAR {
			if spec, ok := decl.Specs[0].(*ast.ValueSpec); ok {
				symbol = spec.Names[0].Name
				break
			}
		}
	}
	if symbol == "" {
		return nil, fmt.Errorf("scaffold: generated entity has no variable")
	}
	all, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("scaffold: parse all.go: %w", err)
	}
	for _, item := range all.Decls {
		decl, ok := item.(*ast.GenDecl)
		if !ok || decl.Tok != token.VAR {
			continue
		}
		for _, item := range decl.Specs {
			spec, ok := item.(*ast.ValueSpec)
			if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "All" || len(spec.Values) != 1 {
				continue
			}
			list, ok := spec.Values[0].(*ast.CompositeLit)
			if !ok {
				return nil, fmt.Errorf("scaffold: all.go has no editable All slice")
			}
			list.Elts = append(list.Elts, ast.NewIdent(symbol))
			var output bytes.Buffer
			if err := format.Node(&output, fileSet, all); err != nil {
				return nil, fmt.Errorf("scaffold: format all.go: %w", err)
			}
			return output.Bytes(), nil
		}
	}
	return nil, fmt.Errorf("scaffold: all.go has no editable All slice")
}

func checkScaffoldNames(dir string, entity []byte) error {
	fileSet := token.NewFileSet()
	newFile, err := parser.ParseFile(fileSet, "new_entity.go", entity, 0)
	if err != nil {
		return fmt.Errorf("scaffold: parse generated entity: %w", err)
	}
	newNames := declaredNames(newFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("scaffold: read %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, filepath.Join(dir, entry.Name()), nil, 0)
		if err != nil {
			return fmt.Errorf("scaffold: parse %s: %w", entry.Name(), err)
		}
		for name := range declaredNames(file) {
			if newNames[name] {
				return fmt.Errorf("scaffold: Go identifier %q already exists in %s", name, entry.Name())
			}
		}
	}
	return nil
}

func declaredNames(file *ast.File) map[string]bool {
	names := map[string]bool{}
	for _, item := range file.Decls {
		switch decl := item.(type) {
		case *ast.GenDecl:
			for _, item := range decl.Specs {
				switch spec := item.(type) {
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						names[name.Name] = true
					}
				case *ast.TypeSpec:
					names[spec.Name.Name] = true
				}
			}
		case *ast.FuncDecl:
			if decl.Recv == nil {
				names[decl.Name.Name] = true
			}
		}
	}
	return names
}

func embedScaffoldView(files map[string][]byte, views []View) error {
	if len(views) == 0 {
		return nil
	}
	view := views[0]
	filename, symbol := entityNames(view.Name)
	if symbol == "All" || symbol == "View" {
		symbol = "Entity" + symbol
	}
	var source strings.Builder
	fmt.Fprintf(&source, "package views\nimport _ \"embed\"\n//go:embed %s.sql\nvar %sSQL string\nvar %s = View{Name: %s, Definition: %sSQL, Materialized: %t}\n", filename, symbol, symbol, strconv.Quote(view.Name), symbol, view.Materialized)
	data, err := format.Source([]byte(source.String()))
	if err != nil {
		return fmt.Errorf("scaffold: format view: %w", err)
	}
	files[filepath.Join("views", filename+"_schema.go")] = data
	files[filepath.Join("views", filename+".sql")] = []byte(view.Definition)
	return nil
}

func appendScaffoldFiles(ctx context.Context, dir, group, entityPath string, entityData, allData []byte, files map[string][]byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	allPath := filepath.Join(dir, group, "all.go")
	info, err := os.Stat(allPath)
	if err != nil {
		return fmt.Errorf("scaffold: all.go: %w", err)
	}
	entity := filepath.Join(dir, entityPath)
	created := []string{}
	defer func() {
		for _, path := range created {
			os.Remove(path)
		}
	}()
	if sqlData, ok := files[filepath.Join(group, strings.TrimSuffix(filepath.Base(entityPath), "_schema.go")+".sql")]; ok {
		sqlPath := filepath.Join(dir, group, strings.TrimSuffix(filepath.Base(entityPath), "_schema.go")+".sql")
		if err := writeExclusive(sqlPath, sqlData); err != nil {
			return err
		}
		created = append(created, sqlPath)
	}
	if err := writeExclusive(entity, entityData); err != nil {
		return err
	}
	created = append(created, entity)
	temporary, err := os.CreateTemp(filepath.Dir(allPath), ".all-*.go")
	if err != nil {
		return fmt.Errorf("scaffold: %w", err)
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(allData); err != nil {
		temporary.Close()
		return fmt.Errorf("scaffold: %w", err)
	}
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		temporary.Close()
		return fmt.Errorf("scaffold: preserve all.go permissions: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("scaffold: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(temporary.Name(), allPath); err != nil {
		return fmt.Errorf("scaffold: update all.go: %w", err)
	}
	created = nil
	return nil
}

func writeExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return fmt.Errorf("scaffold: create %s: %w", path, err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(path)
		return fmt.Errorf("scaffold: write %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return fmt.Errorf("scaffold: close %s: %w", path, err)
	}
	return nil
}
