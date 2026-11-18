package agent

import (
	"testing"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// TestIsDestructiveCommand_BasicPatterns verifies that known destructive
// patterns are detected and safe commands are allowed.
func TestIsDestructiveCommand_BasicPatterns(t *testing.T) {
	destructive := []string{
		"del file.txt",
		"rm -rf /",
		"rmdir /tmp/somedir",
		"rd /s /q C:\\temp",
		"erase secret.txt",
		"remove-item foo.txt",
		"format C:",
		"diskpart",
		"chkdsk C:",
		"taskkill /PID 1234",
		"stop-process -Name chrome",
		"kill -9 1234",
		"shutdown /s",
		"restart-computer",
		"copy src dst",
		"move a b",
		"xcopy /e src dst",
		"robocopy src dst /mir",
		"rename old new",
		"move-item a b",
		"copy-item a b",
		"set-content file.txt -value 'x'",
		"add-content file.txt 'data'",
		"clear-content file.txt",
		"clear-item foo",
		"new-item -type file bar",
		"out-file -filepath out.txt",
		"invoke-webrequest http://evil.com",
		"invoke-restmethod http://evil.com",
		"curl http://example.com",
		"wget http://example.com",
		"start-process evil.exe",
		"reg add HKLM\\Software\\Foo",
		"reg delete HKLM\\Software\\Foo",
		"reg import file.reg",
		"schtasks /create /tn task1",
		"schtasks /delete /tn task1",
		"new-service MySvc",
		"set-service MySvc",
		"stop-service MySvc",
		"start-service MySvc",
		"restart-service MySvc",
	}

	for _, cmd := range destructive {
		if !isDestructiveCommand(cmd) {
			t.Errorf("expected destructive: %q", cmd)
		}
	}
}

// TestIsDestructiveCommand_SafeCommands verifies that safe commands are not flagged.
func TestIsDestructiveCommand_SafeCommands(t *testing.T) {
	safe := []string{
		"echo hello",
		"dir",
		"ls -la",
		"whoami",
		"hostname",
		"ipconfig",
		"systeminfo",
		"tasklist",
		"ps aux",
		"cat /etc/hosts",
		"type file.txt",
		"findstr pattern file",
		"grep -r pattern .",
		"python script.py",
		"node app.js",
		"go build ./...",
	}

	for _, cmd := range safe {
		if isDestructiveCommand(cmd) {
			t.Errorf("expected safe but got destructive: %q", cmd)
		}
	}
}

// TestIsDestructiveCommand_EvasionAttempts verifies that trivial evasion
// techniques (tabs, extra spaces) are caught. Note: $IFS stripping removes
// the variable entirely, so patterns that use $IFS *as a replacement for
// a space* between the command word and its argument are not caught (since
// the space disappears). $IFS inside a word like "d$IFS" + "el" is caught
// because the remaining letters still match via substring.
func TestIsDestructiveCommand_EvasionAttempts(t *testing.T) {
	evasion := []string{
		"del	file.txt",           // tab instead of space
		"rm    -rf  /",             // multiple spaces
		"del  file.txt",            // double space
		"rmdir		/tmp",            // multiple tabs
		"taskkill	/PID 1234",      // tab in taskkill
		"format  C:",               // double space
		"shutdown  /s",             // double space
	}

	for _, cmd := range evasion {
		if !isDestructiveCommand(cmd) {
			t.Errorf("expected destructive (evasion): %q", cmd)
		}
	}
}

// TestIsDestructiveCommand_CaseInsensitive verifies case-insensitive matching.
func TestIsDestructiveCommand_CaseInsensitive(t *testing.T) {
	cases := []string{
		"DEL file.txt",
		"RM -rf /",
		"Format C:",
		"TASKKILL /PID 1234",
		"Shutdown /s",
		"RESTART-COMPUTER",
		"Remove-Item foo",
	}

	for _, cmd := range cases {
		if !isDestructiveCommand(cmd) {
			t.Errorf("expected destructive (case-insensitive): %q", cmd)
		}
	}
}

// TestIsPathWithinSandbox_Basic verifies the sandbox path containment check.
func TestIsPathWithinSandbox_Basic(t *testing.T) {
	sandbox := "/tmp/sandbox"

	// Path inside sandbox
	if !isPathWithinSandbox(sandbox, "/tmp/sandbox/file.txt") {
		t.Error("expected /tmp/sandbox/file.txt to be within sandbox")
	}
	if !isPathWithinSandbox(sandbox, "/tmp/sandbox/sub/file.txt") {
		t.Error("expected nested path to be within sandbox")
	}
	// The sandbox dir itself
	if !isPathWithinSandbox(sandbox, "/tmp/sandbox") {
		t.Error("expected sandbox dir itself to be within sandbox")
	}

	// Path outside sandbox
	if isPathWithinSandbox(sandbox, "/tmp/other/file.txt") {
		t.Error("expected /tmp/other to be outside sandbox")
	}
	if isPathWithinSandbox(sandbox, "/tmp/sandbox_evil/file.txt") {
		t.Error("expected /tmp/sandbox_evil to be outside (prefix sibling)")
	}
}

// TestIsPathWithinSandbox_TraversalAttack verifies that path traversal
// with .., absolute paths, and similar attacks are detected.
func TestIsPathWithinSandbox_TraversalAttack(t *testing.T) {
	sandbox := "/tmp/sandbox"

	// Path traversal with ..
	traversal := []string{
		"/tmp/sandbox/../other/file.txt",
		"/tmp/sandbox/../../etc/passwd",
		"/tmp/sandbox/sub/../../../etc/shadow",
	}

	for _, p := range traversal {
		if isPathWithinSandbox(sandbox, p) {
			t.Errorf("expected traversal path to be outside sandbox: %q", p)
		}
	}
}

// TestIsPathWithinSandbox_AbsolutePathOutside verifies absolute paths
// outside the sandbox are rejected.
func TestIsPathWithinSandbox_AbsolutePathOutside(t *testing.T) {
	sandbox := "/tmp/sandbox"

	outside := []string{
		"/etc/passwd",
		"/root/.ssh/id_rsa",
		"/var/log/syslog",
		"/home/user/file.txt",
		"/tmp/file.txt",
	}

	for _, p := range outside {
		if isPathWithinSandbox(sandbox, p) {
			t.Errorf("expected absolute path outside sandbox: %q", p)
		}
	}
}

// TestIsPathWithinSandbox_EmptySandbox verifies that an empty sandbox
// directory allows all paths.
func TestIsPathWithinSandbox_EmptySandbox(t *testing.T) {
	if !isPathWithinSandbox("", "/any/path/file.txt") {
		t.Error("empty sandbox should allow all paths")
	}
	if !isPathWithinSandbox("", "/etc/passwd") {
		t.Error("empty sandbox should allow all paths")
	}
}

// TestIsPathWithinSandbox_UNCPaths verifies UNC path handling.
// On Linux, UNC-style paths (\\server\share) are just paths starting with \\
// and should be treated as outside the sandbox.
func TestIsPathWithinSandbox_UNCPaths(t *testing.T) {
	sandbox := "/tmp/sandbox"

	unc := []string{
		"\\\\server\\share\\file.txt",
		"//server/share/file.txt",
	}

	for _, p := range unc {
		if isPathWithinSandbox(sandbox, p) {
			t.Errorf("expected UNC path to be outside sandbox: %q", p)
		}
	}
}

// TestIsAllowed_FullPermissions verifies that full permissions allow
// everything (when no sandbox is set).
func TestIsAllowed_FullPermissions(t *testing.T) {
	perm := PermFull
	sandbox := ""

	cmdTypes := []string{
		protocol.TypeExec, protocol.TypeFSRead, protocol.TypeFSList,
		protocol.TypeFileSave, protocol.TypeFileRemove, protocol.TypeFSMkdir,
		protocol.TypeFSMove, protocol.TypeFSHash, protocol.TypeCapture,
		protocol.TypeTextInput, protocol.TypeKeyPress, protocol.TypeTunnelOpen,
		protocol.TypeMitmStart, protocol.TypeDebugAttach, protocol.TypeClipboardWrite,
		protocol.TypeOpenLink, protocol.TypeNotify, protocol.TypeAgentUpdate,
	}

	for _, ct := range cmdTypes {
		if !isAllowed(perm, sandbox, ct, "", "") {
			t.Errorf("full permission should allow %s", ct)
		}
	}
}

// TestIsAllowed_FullWithSandbox verifies that full permissions still
// respect the sandbox for filesystem commands.
func TestIsAllowed_FullWithSandbox(t *testing.T) {
	perm := PermFull
	sandbox := "/tmp/sandbox"

	// FS command inside sandbox — allowed
	if !isAllowed(perm, sandbox, protocol.TypeFSRead, "", "/tmp/sandbox/file.txt") {
		t.Error("full+sandbox should allow fs_read inside sandbox")
	}
	// FS command outside sandbox — denied
	if isAllowed(perm, sandbox, protocol.TypeFSRead, "", "/etc/passwd") {
		t.Error("full+sandbox should deny fs_read outside sandbox")
	}
	// Non-FS command — allowed regardless of sandbox
	if !isAllowed(perm, sandbox, protocol.TypeExec, "echo hi", "") {
		t.Error("full+sandbox should allow exec (non-fs)")
	}
	// file_remove is an FS command, should be checked
	if isAllowed(perm, sandbox, protocol.TypeFileRemove, "", "/etc/passwd") {
		t.Error("full+sandbox should deny file_remove outside sandbox")
	}
	if !isAllowed(perm, sandbox, protocol.TypeFileRemove, "", "/tmp/sandbox/file.txt") {
		t.Error("full+sandbox should allow file_remove inside sandbox")
	}
}

// TestIsAllowed_ReadOnly verifies the read-only tier.
func TestIsAllowed_ReadOnly(t *testing.T) {
	perm := PermReadOnly
	sandbox := ""

	// Allowed in read-only
	allowed := []string{
		protocol.TypePing, protocol.TypePong, protocol.TypeHealth,
		protocol.TypeDisplayInfo, protocol.TypeTokenRotate, protocol.TypeTokenRefresh,
		protocol.TypeFSRead, protocol.TypeFSList, protocol.TypeFSStat, protocol.TypeFSHash,
		protocol.TypeProcList, protocol.TypeTaskList,
		protocol.TypeClipboardRead,
	}
	for _, ct := range allowed {
		if !isAllowed(perm, sandbox, ct, "", "") {
			t.Errorf("read-only should allow %s", ct)
		}
	}

	// Exec with safe command — allowed
	if !isAllowed(perm, sandbox, protocol.TypeExec, "echo hello", "") {
		t.Error("read-only should allow safe exec")
	}
	// Exec with destructive command — denied
	if isAllowed(perm, sandbox, protocol.TypeExec, "rm -rf /", "") {
		t.Error("read-only should deny destructive exec")
	}

	// Denied in read-only
	denied := []string{
		protocol.TypeFileSave, protocol.TypeFileRemove, protocol.TypeFSMkdir,
		protocol.TypeFSMove, protocol.TypeCapture, protocol.TypeTextInput,
		protocol.TypeKeyPress, protocol.TypeKeyCombo, protocol.TypeTunnelOpen,
		protocol.TypeMitmStart, protocol.TypeDebugAttach, protocol.TypeClipboardWrite,
		protocol.TypeOpenLink, protocol.TypeNotify, protocol.TypeAgentUpdate,
		protocol.TypeProcKill, protocol.TypeProcStart, protocol.TypeTaskStop,
	}
	for _, ct := range denied {
		if isAllowed(perm, sandbox, ct, "", "") {
			t.Errorf("read-only should deny %s", ct)
		}
	}
}

// TestIsAllowed_ReadOnly_WithSandbox verifies that read-only respects
// the sandbox for filesystem reads.
func TestIsAllowed_ReadOnly_WithSandbox(t *testing.T) {
	perm := PermReadOnly
	sandbox := "/tmp/sandbox"

	// Read inside sandbox — allowed
	if !isAllowed(perm, sandbox, protocol.TypeFSRead, "", "/tmp/sandbox/file.txt") {
		t.Error("read-only+sandbox should allow fs_read inside sandbox")
	}
	// Read outside sandbox — denied
	if isAllowed(perm, sandbox, protocol.TypeFSRead, "", "/etc/passwd") {
		t.Error("read-only+sandbox should deny fs_read outside sandbox")
	}
}

// TestIsAllowed_Standard verifies the standard tier.
func TestIsAllowed_Standard(t *testing.T) {
	perm := PermStandard
	sandbox := ""

	// Allowed in standard (same as sandboxed but no auto-sandbox)
	allowed := []string{
		protocol.TypePing, protocol.TypePong, protocol.TypeHealth,
		protocol.TypeDisplayInfo, protocol.TypeTokenRotate, protocol.TypeTokenRefresh,
		protocol.TypeFSRead, protocol.TypeFSList, protocol.TypeFSStat, protocol.TypeFSHash,
		protocol.TypeFileSave, protocol.TypeFSMkdir, protocol.TypeFSMove,
		protocol.TypeProcList, protocol.TypeTaskList,
		protocol.TypeClipboardRead,
		protocol.TypeProcStart, protocol.TypeProcKill, protocol.TypeTaskStop,
	}
	for _, ct := range allowed {
		if !isAllowed(perm, sandbox, ct, "", "") {
			t.Errorf("standard should allow %s", ct)
		}
	}

	// Exec — safe allowed, destructive denied
	if !isAllowed(perm, sandbox, protocol.TypeExec, "echo hi", "") {
		t.Error("standard should allow safe exec")
	}
	if isAllowed(perm, sandbox, protocol.TypeExec, "del file.txt", "") {
		t.Error("standard should deny destructive exec")
	}

	// file_remove — DENIED in standard (same as sandboxed)
	if isAllowed(perm, sandbox, protocol.TypeFileRemove, "", "") {
		t.Error("standard should deny file_remove")
	}

	// Denied in standard
	denied := []string{
		protocol.TypeCapture, protocol.TypeTextInput, protocol.TypeKeyPress,
		protocol.TypeKeyCombo, protocol.TypeTunnelOpen, protocol.TypeMitmStart,
		protocol.TypeDebugAttach, protocol.TypeClipboardWrite,
		protocol.TypeOpenLink, protocol.TypeNotify, protocol.TypeAgentUpdate,
	}
	for _, ct := range denied {
		if isAllowed(perm, sandbox, ct, "", "") {
			t.Errorf("standard should deny %s", ct)
		}
	}
}

// TestIsAllowed_Sandboxed verifies the sandboxed tier.
func TestIsAllowed_Sandboxed(t *testing.T) {
	perm := PermSandboxed
	sandbox := "/tmp/sandbox"

	// Allowed in sandboxed
	allowed := []string{
		protocol.TypePing, protocol.TypePong, protocol.TypeHealth,
		protocol.TypeDisplayInfo, protocol.TypeTokenRotate, protocol.TypeTokenRefresh,
		protocol.TypeProcList, protocol.TypeTaskList,
		protocol.TypeClipboardRead,
		protocol.TypeProcStart, protocol.TypeProcKill, protocol.TypeTaskStop,
	}
	for _, ct := range allowed {
		if !isAllowed(perm, sandbox, ct, "", "") {
			t.Errorf("sandboxed should allow %s", ct)
		}
	}

	// FS read inside sandbox — allowed
	if !isAllowed(perm, sandbox, protocol.TypeFSRead, "", "/tmp/sandbox/file.txt") {
		t.Error("sandboxed should allow fs_read inside sandbox")
	}
	// FS read outside sandbox — denied
	if isAllowed(perm, sandbox, protocol.TypeFSRead, "", "/etc/passwd") {
		t.Error("sandboxed should deny fs_read outside sandbox")
	}

	// FS write inside sandbox — allowed
	if !isAllowed(perm, sandbox, protocol.TypeFileSave, "", "/tmp/sandbox/file.txt") {
		t.Error("sandboxed should allow file_save inside sandbox")
	}
	// FS write outside sandbox — denied
	if isAllowed(perm, sandbox, protocol.TypeFileSave, "", "/etc/passwd") {
		t.Error("sandboxed should deny file_save outside sandbox")
	}

	// Exec safe — allowed
	if !isAllowed(perm, sandbox, protocol.TypeExec, "echo hi", "") {
		t.Error("sandboxed should allow safe exec")
	}
	// Exec destructive — denied
	if isAllowed(perm, sandbox, protocol.TypeExec, "rm -rf /", "") {
		t.Error("sandboxed should deny destructive exec")
	}

	// file_remove — always denied in sandboxed
	if isAllowed(perm, sandbox, protocol.TypeFileRemove, "", "/tmp/sandbox/file.txt") {
		t.Error("sandboxed should deny file_remove even inside sandbox")
	}

	// Denied in sandboxed
	denied := []string{
		protocol.TypeCapture, protocol.TypeTextInput, protocol.TypeKeyPress,
		protocol.TypeKeyCombo, protocol.TypeTunnelOpen, protocol.TypeMitmStart,
		protocol.TypeDebugAttach, protocol.TypeClipboardWrite,
		protocol.TypeOpenLink, protocol.TypeNotify, protocol.TypeAgentUpdate,
	}
	for _, ct := range denied {
		if isAllowed(perm, sandbox, ct, "", "") {
			t.Errorf("sandboxed should deny %s", ct)
		}
	}
}

// TestIsAllowed_UnknownTier verifies that unknown tiers get full access
// (backward compatibility).
func TestIsAllowed_UnknownTier(t *testing.T) {
	if !isAllowed("unknown-tier", "", protocol.TypeExec, "rm -rf /", "") {
		t.Error("unknown tier should default to full access")
	}
	if !isAllowed("unknown-tier", "", protocol.TypeFileRemove, "", "") {
		t.Error("unknown tier should default to full access")
	}
}

// TestIsAllowed_EmptyPermString verifies that an empty permission string
// is treated as full access.
func TestIsAllowed_EmptyPermString(t *testing.T) {
	if !isAllowed("", "", protocol.TypeExec, "echo hi", "") {
		t.Error("empty perm should be full access")
	}
	if !isAllowed("", "", protocol.TypeFileRemove, "", "") {
		t.Error("empty perm should be full access")
	}
}

// TestIsFSCommand verifies the isFSCommand helper.
func TestIsFSCommand(t *testing.T) {
	fsTypes := []string{
		protocol.TypeFSRead, protocol.TypeFSList, protocol.TypeFSStat,
		protocol.TypeFSHash, protocol.TypeFileSave, protocol.TypeFileRemove,
		protocol.TypeFSMkdir, protocol.TypeFSMove,
	}
	for _, ct := range fsTypes {
		if !isFSCommand(ct) {
			t.Errorf("expected %s to be an FS command", ct)
		}
	}

	nonFS := []string{
		protocol.TypeExec, protocol.TypeCapture, protocol.TypePing,
		protocol.TypeHealth, protocol.TypeProcList, protocol.TypeTunnelOpen,
	}
	for _, ct := range nonFS {
		if isFSCommand(ct) {
			t.Errorf("expected %s to NOT be an FS command", ct)
		}
	}
}

// TestIsAllowed_AllTiers_AllCommandTypes is a table-driven test that
// exercises all four permission tiers against key command types.
func TestIsAllowed_AllTiers_AllCommandTypes(t *testing.T) {
	sandbox := "/tmp/sandbox"
	safePath := "/tmp/sandbox/file.txt"
	outsidePath := "/etc/passwd"
	safeCmd := "echo hello"
	destructiveCmd := "rm -rf /"

	type tc struct {
		name    string
		perm    string
		sandbox string
		cmdType string
		cmd     string
		path    string
		want    bool
	}

	tests := []tc{
		// --- Full (no sandbox) ---
		{"full/no-sandbox/exec-safe", PermFull, "", protocol.TypeExec, safeCmd, "", true},
		{"full/no-sandbox/exec-destructive", PermFull, "", protocol.TypeExec, destructiveCmd, "", true},
		{"full/no-sandbox/fs-read", PermFull, "", protocol.TypeFSRead, "", outsidePath, true},
		{"full/no-sandbox/file-remove", PermFull, "", protocol.TypeFileRemove, "", outsidePath, true},
		{"full/no-sandbox/capture", PermFull, "", protocol.TypeCapture, "", "", true},
		{"full/no-sandbox/tunnel", PermFull, "", protocol.TypeTunnelOpen, "", "", true},
		{"full/no-sandbox/debug", PermFull, "", protocol.TypeDebugAttach, "", "", true},

		// --- Full + sandbox ---
		{"full/sandbox/exec", PermFull, sandbox, protocol.TypeExec, safeCmd, "", true},
		{"full/sandbox/fs-read-inside", PermFull, sandbox, protocol.TypeFSRead, "", safePath, true},
		{"full/sandbox/fs-read-outside", PermFull, sandbox, protocol.TypeFSRead, "", outsidePath, false},
		{"full/sandbox/file-remove-inside", PermFull, sandbox, protocol.TypeFileRemove, "", safePath, true},
		{"full/sandbox/file-remove-outside", PermFull, sandbox, protocol.TypeFileRemove, "", outsidePath, false},
		{"full/sandbox/capture", PermFull, sandbox, protocol.TypeCapture, "", "", true},

		// --- Read-only ---
		{"ro/exec-safe", PermReadOnly, sandbox, protocol.TypeExec, safeCmd, "", true},
		{"ro/exec-destructive", PermReadOnly, sandbox, protocol.TypeExec, destructiveCmd, "", false},
		{"ro/fs-read", PermReadOnly, sandbox, protocol.TypeFSRead, "", safePath, true},
		{"ro/fs-read-sandboxed", PermReadOnly, sandbox, protocol.TypeFSRead, "", safePath, true},
		{"ro/fs-read-outside-sandbox", PermReadOnly, sandbox, protocol.TypeFSRead, "", outsidePath, false},
		{"ro/file-save", PermReadOnly, sandbox, protocol.TypeFileSave, "", safePath, false},
		{"ro/file-remove", PermReadOnly, sandbox, protocol.TypeFileRemove, "", safePath, false},
		{"ro/capture", PermReadOnly, sandbox, protocol.TypeCapture, "", "", false},
		{"ro/tunnel", PermReadOnly, sandbox, protocol.TypeTunnelOpen, "", "", false},
		{"ro/debug", PermReadOnly, sandbox, protocol.TypeDebugAttach, "", "", false},
		{"ro/ping", PermReadOnly, sandbox, protocol.TypePing, "", "", true},
		{"ro/health", PermReadOnly, sandbox, protocol.TypeHealth, "", "", true},
		{"ro/proc-list", PermReadOnly, sandbox, protocol.TypeProcList, "", "", true},
		{"ro/clipboard-read", PermReadOnly, sandbox, protocol.TypeClipboardRead, "", "", true},
		{"ro/clipboard-write", PermReadOnly, sandbox, protocol.TypeClipboardWrite, "", "", false},

		// --- Standard ---
		{"std/exec-safe", PermStandard, sandbox, protocol.TypeExec, safeCmd, "", true},
		{"std/exec-destructive", PermStandard, sandbox, protocol.TypeExec, destructiveCmd, "", false},
		{"std/fs-read", PermStandard, sandbox, protocol.TypeFSRead, "", safePath, true},
		{"std/file-save", PermStandard, sandbox, protocol.TypeFileSave, "", safePath, true},
		{"std/file-remove", PermStandard, sandbox, protocol.TypeFileRemove, "", safePath, false},
		{"std/capture", PermStandard, sandbox, protocol.TypeCapture, "", "", false},
		{"std/tunnel", PermStandard, sandbox, protocol.TypeTunnelOpen, "", "", false},
		{"std/debug", PermStandard, sandbox, protocol.TypeDebugAttach, "", "", false},
		{"std/proc-kill", PermStandard, sandbox, protocol.TypeProcKill, "", "", true},
		{"std/proc-start", PermStandard, sandbox, protocol.TypeProcStart, "", "", true},

		// --- Sandboxed ---
		{"sb/exec-safe", PermSandboxed, sandbox, protocol.TypeExec, safeCmd, "", true},
		{"sb/exec-destructive", PermSandboxed, sandbox, protocol.TypeExec, destructiveCmd, "", false},
		{"sb/fs-read-inside", PermSandboxed, sandbox, protocol.TypeFSRead, "", safePath, true},
		{"sb/fs-read-outside", PermSandboxed, sandbox, protocol.TypeFSRead, "", outsidePath, false},
		{"sb/file-save-inside", PermSandboxed, sandbox, protocol.TypeFileSave, "", safePath, true},
		{"sb/file-save-outside", PermSandboxed, sandbox, protocol.TypeFileSave, "", outsidePath, false},
		{"sb/file-remove", PermSandboxed, sandbox, protocol.TypeFileRemove, "", safePath, false},
		{"sb/capture", PermSandboxed, sandbox, protocol.TypeCapture, "", "", false},
		{"sb/tunnel", PermSandboxed, sandbox, protocol.TypeTunnelOpen, "", "", false},
		{"sb/debug", PermSandboxed, sandbox, protocol.TypeDebugAttach, "", "", false},
		{"sb/proc-kill", PermSandboxed, sandbox, protocol.TypeProcKill, "", "", true},
		{"sb/proc-start", PermSandboxed, sandbox, protocol.TypeProcStart, "", "", true},
	}

	for _, tc := range tests {
		got := isAllowed(tc.perm, tc.sandbox, tc.cmdType, tc.cmd, tc.path)
		if got != tc.want {
			t.Errorf("%s: isAllowed(%q, %q, %q, %q, %q) = %v, want %v",
				tc.name, tc.perm, tc.sandbox, tc.cmdType, tc.cmd, tc.path, got, tc.want)
		}
	}
}
// T2 (A1): the exec quoting fix changes only how the command is passed. A
// quoted destructive command is still refused by the gate, which inspects the
// command string before any spawn happens.
func TestIsDestructiveCommand_QuotedPowerShell(t *testing.T) {
	cases := []string{
		`powershell -NoProfile -Command "Remove-Item C:\Windows\System32\drivers\etc\hosts"`,
		`powershell -Command "Stop-Process -Name probe-client"`,
		`cmd /c "del C:\Windows\System32\drivers\etc\hosts"`,
	}
	for _, cmd := range cases {
		if !isDestructiveCommand(cmd) {
			t.Errorf("expected destructive through quoting: %q", cmd)
		}
	}
}
