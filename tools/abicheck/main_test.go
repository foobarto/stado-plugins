package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestValidateSignature(t *testing.T) {
	tests := []struct {
		name       string
		importName string
		decl       string
		ok         bool
	}{
		{
			name:       "current dial ABI",
			importName: "stado_net_dial",
			decl:       "package p\nfunc dial(a, b, c, d uint32, port, timeout int32) int64",
			ok:         true,
		},
		{
			name:       "legacy four argument dial ABI",
			importName: "stado_net_dial",
			decl:       "package p\nfunc dial(a, b, c, d uint32) uint32",
		},
		{
			name:       "unsigned dial result loses error sentinel",
			importName: "stado_net_dial",
			decl:       "package p\nfunc dial(a, b, c, d uint32, port, timeout int32) uint64",
		},
		{
			name:       "current read ABI",
			importName: "stado_net_read",
			decl:       "package p\nfunc read(handle, outPtr, outMax, timeout uint32) int32",
			ok:         true,
		},
		{
			name:       "legacy five argument read ABI",
			importName: "stado_net_read",
			decl:       "package p\nfunc read(handle, max, timeout, outPtr, outCap uint32) int32",
		},
		{
			name:       "current write ABI",
			importName: "stado_net_write",
			decl:       "package p\nfunc write(handle, dataPtr, dataLen uint32) int32",
			ok:         true,
		},
		{
			name:       "current close ABI",
			importName: "stado_net_close",
			decl:       "package p\nfunc close(handle uint32) int32",
			ok:         true,
		},
		{
			name:       "close result omitted",
			importName: "stado_net_close",
			decl:       "package p\nfunc close(handle uint32)",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", test.decl, 0)
			if err != nil {
				t.Fatal(err)
			}
			fn := file.Decls[0].(*ast.FuncDecl)
			problem := validateSignature(fn, checkedImports[test.importName])
			if test.ok && problem != "" {
				t.Fatalf("valid declaration rejected: %s", problem)
			}
			if !test.ok && problem == "" {
				t.Fatal("invalid declaration accepted")
			}
		})
	}
}

func TestWasmImportName(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", `package p
//go:wasmimport stado stado_net_dial
func dial(a, b, c, d uint32, port, timeout int32) int64
`, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	fn := file.Decls[0].(*ast.FuncDecl)
	if got := wasmImportName(fn.Doc); got != "stado_net_dial" {
		t.Fatalf("import name = %q", got)
	}
}
