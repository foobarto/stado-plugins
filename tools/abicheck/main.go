// abicheck validates the load-bearing Go guest declarations that cannot be
// checked by searching import names alone. The reproducible-build gate then
// proves the committed WASM came from these checked sources.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var removedImports = map[string]string{
	"stado_pty_attach": "removed by EP-0043; PTY handles no longer require attach ownership",
	"stado_pty_detach": "removed by EP-0043; PTY handles no longer require attach ownership",
}

type importSignature struct {
	params  []string
	results []string
}

var checkedImports = map[string]importSignature{
	"stado_net_dial": {
		params:  []string{"uint32", "uint32", "uint32", "uint32", "int32", "int32"},
		results: []string{"int64"},
	},
	"stado_net_read": {
		params:  []string{"uint32", "uint32", "uint32", "uint32"},
		results: []string{"int32"},
	},
	"stado_net_write": {
		params:  []string{"uint32", "uint32", "uint32"},
		results: []string{"int32"},
	},
	"stado_net_close": {
		params:  []string{"uint32"},
		results: []string{"int32"},
	},
}

func main() {
	root := "."
	if len(os.Args) > 2 {
		fatalf("usage: go run ./tools/abicheck/main.go [repo-root]")
	}
	if len(os.Args) == 2 {
		root = os.Args[1]
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		fatalf("resolve repository root: %v", err)
	}

	var failures []string
	err = filepath.WalkDir(absRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		fileSet := token.NewFileSet()
		file, parseErr := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Doc == nil {
				continue
			}
			importName := wasmImportName(fn.Doc)
			if importName == "" {
				continue
			}
			rel, _ := filepath.Rel(absRoot, path)
			if reason, removed := removedImports[importName]; removed {
				line := fileSet.Position(fn.Pos()).Line
				failures = append(failures, fmt.Sprintf("%s:%d: %s imports removed %s: %s", rel, line, fn.Name.Name, importName, reason))
				continue
			}
			if signature, checked := checkedImports[importName]; checked {
				if problem := validateSignature(fn, signature); problem != "" {
					failures = append(failures, fmt.Sprintf("%s: %s declaration: %s", rel, importName, problem))
				}
			}
		}
		return nil
	})
	if err != nil {
		fatalf("walk source: %v", err)
	}
	if len(failures) != 0 {
		for _, failure := range failures {
			fmt.Fprintln(os.Stderr, "abicheck:", failure)
		}
		os.Exit(1)
	}
	fmt.Println("Go guest host-import ABI check passed")
}

func wasmImportName(doc *ast.CommentGroup) string {
	for _, comment := range doc.List {
		fields := strings.Fields(strings.TrimPrefix(comment.Text, "//"))
		if len(fields) == 3 && fields[0] == "go:wasmimport" && fields[1] == "stado" {
			return fields[2]
		}
	}
	return ""
}

func validateSignature(fn *ast.FuncDecl, want importSignature) string {
	params := expandFieldTypes(fn.Type.Params)
	if len(params) != len(want.params) {
		return fmt.Sprintf("has %d parameters, want %d", len(params), len(want.params))
	}
	for index := range want.params {
		if params[index] != want.params[index] {
			return fmt.Sprintf("parameter %d has type %s, want %s", index+1, params[index], want.params[index])
		}
	}
	results := expandFieldTypes(fn.Type.Results)
	if !equalTypes(results, want.results) {
		return fmt.Sprintf("returns %v, want %v", results, want.results)
	}
	return ""
}

func equalTypes(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func expandFieldTypes(fields *ast.FieldList) []string {
	if fields == nil {
		return nil
	}
	var out []string
	for _, field := range fields.List {
		ident, ok := field.Type.(*ast.Ident)
		typeName := "non-identifier"
		if ok {
			typeName = ident.Name
		}
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		for range count {
			out = append(out, typeName)
		}
	}
	return out
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "abicheck: "+format+"\n", args...)
	os.Exit(1)
}
