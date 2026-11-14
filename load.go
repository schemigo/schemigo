package schemigo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Load compiles and executes a trusted Go schema directory in the current module.
// The directory must contain a main package that writes exactly one JSON object
// representing Schema to stdout. Diagnostics on stderr are kept separate.
// Compilation uses -mod=readonly. Context cancellation terminates the direct
// child process; schema programs must manage any children they start themselves.
// Load returns a normalized independent schema or no schema on failure.
func Load(ctx context.Context, dir string) (Schema, error) {
	location, err := schemaLocation(ctx, dir)
	if err != nil {
		return Schema{}, fmt.Errorf("load schema: %w", err)
	}
	temporary, err := os.MkdirTemp("", "schemigo-load-")
	if err != nil {
		return Schema{}, fmt.Errorf("load schema: %w", err)
	}
	defer os.RemoveAll(temporary)
	executable := filepath.Join(temporary, "schema.exe")
	if _, err := runSchemaCommand(ctx, location.dir, "go", "build", "-mod=readonly", "-o", executable, "."); err != nil {
		return Schema{}, fmt.Errorf("compile schema: %w", err)
	}
	output, err := runSchemaCommand(ctx, location.dir, executable)
	if err != nil {
		return Schema{}, fmt.Errorf("execute schema: %w", err)
	}
	return decodeSchema(output)
}

func decodeSchema(output []byte) (Schema, error) {
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return Schema{}, fmt.Errorf("load schema: stdout must contain one JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var schema Schema
	if err := decoder.Decode(&schema); err != nil {
		return Schema{}, fmt.Errorf("decode schema: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Schema{}, fmt.Errorf("load schema: multiple JSON values on stdout")
		}
		return Schema{}, fmt.Errorf("load schema: trailing stdout: %w", err)
	}
	normalized, err := Normalize(schema)
	if err != nil {
		return Schema{}, fmt.Errorf("load schema: %w", err)
	}
	return normalized, nil
}

func runSchemaCommand(ctx context.Context, dir, program string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, program, args...)
	command.Dir = dir
	command.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s: %w\n%s", filepath.Base(program), err, strings.TrimSpace(stderr.String()))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

type moduleLocation struct{ dir, importPath string }

// schemaLocation does not create directories or modify module files.
func schemaLocation(ctx context.Context, dir string) (moduleLocation, error) {
	if err := ctx.Err(); err != nil {
		return moduleLocation{}, err
	}
	if strings.TrimSpace(dir) == "" {
		return moduleLocation{}, fmt.Errorf("schema directory is empty")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return moduleLocation{}, err
	}
	data, err := runSchemaCommand(ctx, cwd, "go", "list", "-mod=readonly", "-m", "-json")
	if err != nil {
		return moduleLocation{}, fmt.Errorf("locate current Go module: %w", err)
	}
	var module struct{ Path, Dir, GoMod string }
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var candidate struct{ Path, Dir, GoMod string }
		if err := decoder.Decode(&candidate); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return moduleLocation{}, fmt.Errorf("locate current Go module: %w", err)
		}
		if candidate.Dir == "" {
			continue
		}
		relative, err := filepath.Rel(candidate.Dir, cwd)
		if err == nil && (relative == "." || filepath.IsLocal(relative)) && len(candidate.Dir) > len(module.Dir) {
			module = candidate
		}
	}
	if module.Path == "" || module.Dir == "" || module.GoMod == "" {
		return moduleLocation{}, fmt.Errorf("current directory must belong to a Go module")
	}
	root, err := filepath.Abs(module.Dir)
	if err != nil {
		return moduleLocation{}, err
	}
	target, err := filepath.Abs(dir)
	if err != nil {
		return moduleLocation{}, err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == "." || !filepath.IsLocal(relative) {
		return moduleLocation{}, fmt.Errorf("schema directory must be a subdirectory of the current Go module")
	}
	// Reject links and nested modules so the lexical boundary is also the actual
	// filesystem/import boundary. Export may create the missing tail afterwards.
	path := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return moduleLocation{}, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return moduleLocation{}, fmt.Errorf("schema path must contain ordinary directories: %s", path)
		}
		if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
			return moduleLocation{}, fmt.Errorf("schema directory belongs to a nested Go module: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return moduleLocation{}, err
		}
	}
	importPath := module.Path + "/" + filepath.ToSlash(relative)
	for _, c := range importPath {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("/._-+", c)) {
			return moduleLocation{}, fmt.Errorf("schema directory is not a valid Go import path: %q", importPath)
		}
	}
	for _, part := range strings.Split(importPath, "/") {
		base, _, _ := strings.Cut(part, ".")
		reserved := strings.ToUpper(base)
		if strings.HasSuffix(part, ".") || reserved == "CON" || reserved == "PRN" || reserved == "AUX" || reserved == "NUL" || len(reserved) == 4 && (strings.HasPrefix(reserved, "COM") || strings.HasPrefix(reserved, "LPT")) && reserved[3] >= '1' && reserved[3] <= '9' {
			return moduleLocation{}, fmt.Errorf("schema directory is not a portable Go import path: %q", importPath)
		}
	}
	return moduleLocation{dir: target, importPath: importPath}, nil
}
