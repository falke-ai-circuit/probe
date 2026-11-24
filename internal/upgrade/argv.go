package upgrade

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

// Forbidden client tokens. `connect` terminates flag parsing in
// cmd/probe-client (P1-02 C10): every flag after it is ignored, CanaryMode
// stays false and the swap silently does nothing. The commit must never
// re-create that defect in the successor.
var forbiddenClientTokens = []string{"connect", "--canary", "--canary-old-pid"}

// ConfigPathExists reports whether path names an existing regular file. It is a
// variable because the derivation is exercised on a Linux host with Windows
// paths that cannot exist there: the existence oracle is the test seam.
//
// A Windows path on a non-Windows host is "unknowable", not "missing": os.Stat
// cannot see C:\..., so the pair is kept rather than dropped on a guess. The
// check does its job on the host that runs the swap (Windows), which is the host
// the synthetic path was always breaking.
var ConfigPathExists = func(path string) bool {
	if IsAbsWinPath(path) && runtime.GOOS != "windows" {
		return true
	}
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// NewClientArgs is NewClientArgsChecked against the real filesystem.
func NewClientArgs(oldCmdline string) (args []string, configPath string, err error) {
	args, configPath, _, err = NewClientArgsChecked(oldCmdline, ConfigPathExists)
	return args, configPath, err
}

// NewClientArgsChecked derives the successor argv from the OLD process's command
// line (A4 discovers it; no other read path exposes it) and drops a --config
// that names no file.
//
// The rule is: extract only the "--config <p>" pair, or use zero arguments.
// Copying the old command line verbatim is forbidden - if the old process was
// started through the broken K9 path its command line begins with `connect`,
// and copying it would reproduce the defect in the successor.
//
// P12-01: P9-19/P9-20 taught the READING side (ResolveConfigPath) to fall
// through when an explicit --config names no file. Nothing stopped the WRITING
// side from propagating such a path: the old client synthesises
// <dir>\probe-client.json, which never exists on an agent-named install, so
// pinning it into the successor argv hands the new process a dead end it has to
// rescue itself from. Dropping the pair instead lets the successor resolve
// beside its own executable - the same order ResolveConfigPath already uses -
// and the dropped path is returned so the caller can journal exactly which
// synthetic value was discarded.
func NewClientArgsChecked(oldCmdline string, exists func(string) bool) (args []string, configPath, droppedConfig string, err error) {
	tokens, err := splitWindowsCmdline(oldCmdline)
	if err != nil {
		return nil, "", "", stepErr("SW-16", "ARGV_INVALID", err)
	}
	if exists == nil {
		exists = ConfigPathExists
	}

	found := ""
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		var value string
		switch {
		case tok == "--config" || tok == "-config":
			if i+1 >= len(tokens) {
				return nil, "", "", stepErr("SW-16", "ARGV_INVALID", fmt.Errorf("--config without a value"))
			}
			value = tokens[i+1]
			i++
		case strings.HasPrefix(tok, "--config="):
			value = strings.TrimPrefix(tok, "--config=")
		case strings.HasPrefix(tok, "-config="):
			value = strings.TrimPrefix(tok, "-config=")
		default:
			continue
		}
		if found != "" && !EqualWinPath(found, value) {
			return nil, "", "", stepErr("SW-16", "ARGV_INVALID", fmt.Errorf("multiple --config values"))
		}
		found = value
	}

	if found == "" {
		// Embedded-config build: zero arguments (config arrives via
		// -X main.configB64), which is the normal field case.
		return nil, "", "", nil
	}
	if !IsAbsWinPath(found) {
		// O10: a relative --config resolves against the process CWD, and the
		// old process's CWD is not readable through any available primitive.
		return nil, "", "", stepErr("SW-16", "ARGV_INVALID",
			fmt.Errorf("config path %q is relative (O10)", found))
	}
	if !exists(found) {
		// Synthetic path: drop the pair so the successor resolves its own
		// config beside the executable instead of being pinned to a dead end.
		return nil, "", CleanWinPath(found), nil
	}
	return []string{"--config", CleanWinPath(found)}, CleanWinPath(found), "", nil
}

// ValidateClientArgs asserts the argv handed to the successor is one
// cmd/probe-client actually parses: flag-first, no subcommand, no canary flag.
func ValidateClientArgs(args []string) error {
	for i, a := range args {
		for _, forbidden := range forbiddenClientTokens {
			if strings.EqualFold(a, forbidden) || strings.HasPrefix(strings.ToLower(a), forbidden+"=") {
				return stepErr("SW-16", "ARGV_INVALID", fmt.Errorf("token %q at index %d is forbidden", a, i))
			}
		}
		if !strings.HasPrefix(a, "-") && i == 0 {
			return stepErr("SW-16", "ARGV_INVALID",
				fmt.Errorf("first token %q is positional; flag parsing would stop there", a))
		}
	}
	return nil
}

// splitWindowsCmdline splits a command line using the Windows quoting rules
// that matter here: double quotes group, backslash-quote escapes a quote.
func splitWindowsCmdline(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inQuotes := false
	started := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && s[i+1] == '"':
			cur.WriteByte('"')
			i++
			started = true
		case c == '"':
			inQuotes = !inQuotes
			started = true
		case (c == ' ' || c == '\t') && !inQuotes:
			if started {
				out = append(out, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	if inQuotes {
		return nil, fmt.Errorf("unterminated quote")
	}
	if started {
		out = append(out, cur.String())
	}
	return out, nil
}
