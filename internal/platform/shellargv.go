package platform

import "strings"

// Windows shell command construction (A1).
//
// The OLD client spawns every exec as a fixed two-token prefix plus the whole
// command string as ONE argv element: exec.Command("cmd", "/c", command). Go
// escapes that single argument for CreateProcess (backslash-quote rules) and
// cmd.exe then re-parses the result under its own quoting rules, so embedded
// quotes do not survive: `-replace "a","b"` arrives as `-replace \"a\",\"b\"`
// and PowerShell answers "You must provide a value expression following the
// '-replace' operator".
//
// shellArgv keeps the argv form when the command carries no quote (nothing can
// be mangled) and switches to the raw command-line form when it does:
// `cmd /s /c "<command>"`. With /s, cmd.exe strips exactly the outer pair of
// quotes and executes the remainder verbatim, so the command's own quoting
// reaches the shell unchanged.
//
// This changes how the bytes are passed, never what is allowed: the
// destructive-command gate (internal/agent/permissions.go) inspects the same
// command string before the spawn, so no command becomes permitted that was
// refused before.

// shellArgv returns the arguments to hand the shell and whether they must be
// joined into one raw command line (see the file comment).
func shellArgv(command string) (args []string, raw bool) {
	if !strings.ContainsRune(command, '"') {
		return []string{"/c", command}, false
	}
	// /s makes cmd strip the first and last quote and run the rest as written.
	return []string{"/s", "/c", `"` + command + `"`}, true
}

// shellCmdLine joins the program token and arguments into the raw command line
// form used for syscall.SysProcAttr.CmdLine. argv0 is cosmetic once CmdLine is
// set: CreateProcess takes the executable from lpApplicationName, which os/exec
// fills from cmd.Path.
func shellCmdLine(argv0 string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, argv0)
	parts = append(parts, args...)
	return strings.Join(parts, " ")
}
