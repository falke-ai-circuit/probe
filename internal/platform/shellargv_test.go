package platform

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A1: the command string is one field. A quoted command must reach cmd.exe with
// its quotes intact instead of being re-escaped by os/exec and re-parsed.
func TestShellArgvPreservesQuoting(t *testing.T) {
	command := `powershell -NoProfile -Command "Get-Content C:\mods\a.xml | ForEach-Object { $_ -replace "a","b" }"`

	args, raw := shellArgv(command)
	if !raw {
		t.Fatal("a command containing quotes must use the raw command-line form")
	}
	if len(args) != 3 || args[0] != "/s" || args[1] != "/c" {
		t.Fatalf("args = %q, want /s /c \"<command>\"", args)
	}
	line := shellCmdLine("cmd", args)
	want := `cmd /s /c "` + command + `"`
	if line != want {
		t.Fatalf("command line = %q, want %q", line, want)
	}
	// The defect was backslash-escaping introduced by argv escaping.
	if strings.Contains(line, `\"`) {
		t.Fatalf("command line carries backslash-escaped quotes: %q", line)
	}
}

func TestShellArgvKeepsArgvFormWhenNothingNeedsQuoting(t *testing.T) {
	args, raw := shellArgv(`dir C:\ /b`)
	if raw {
		t.Fatal("an unquoted command needs no raw command line")
	}
	if len(args) != 2 || args[0] != "/c" || args[1] != `dir C:\ /b` {
		t.Fatalf("args = %q, want [/c <command>]", args)
	}
	if got := shellCmdLine("cmd", args); got != `cmd /c dir C:\ /b` {
		t.Fatalf("command line = %q", got)
	}
}

// T4 wire contract: Offset/Limit are honoured at read time and Size stays the
// length of the returned window, encoded base64.
func TestGenericPlatformReadFileRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.xml")
	payload := []byte(strings.Repeat("0123456789", 1000)) // 10 KB
	if err := os.WriteFile(path, payload, 0644); err != nil {
		t.Fatal(err)
	}
	p := &genericPlatform{}

	res, err := p.ReadFile(path, 5000, 100)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(res.Data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(raw) != string(payload[5000:5100]) {
		t.Fatal("range bytes mismatch")
	}
	if res.Size != 100 || res.Encoding != "base64" {
		t.Fatalf("size=%d encoding=%q", res.Size, res.Encoding)
	}

	res, err = p.ReadFile(path, 0, 0)
	if err != nil {
		t.Fatalf("ReadFile whole: %v", err)
	}
	if res.Size != int64(len(payload)) {
		t.Fatalf("whole-file size = %d", res.Size)
	}
}
