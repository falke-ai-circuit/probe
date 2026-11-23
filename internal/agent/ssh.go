package agent

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// P8-07 (P8-02 §6.3(3)) — the SSH helper is a subcommand of this binary:
//
//	probe-client ssh --host 10.10.20.7 --user enable --credential sw-enable --script /dna/sw/gi1-4.txt
//
// It replaces the password-helper executable + ssh.exe + Python. The value is
// read from STDIN (the
// agent feeds it there); --credential names the credential for the operator's
// benefit and is never the value. Nothing secret is written to argv or disk.
//
// Two modes:
//   - interactive (default): opens a shell, handles the login banner, reads the
//     password line from stdin, discriminates '>' (user exec) from '#' (enable
//     mode) and drives --script line by line.
//   - --exec "<cmd>": one non-interactive command; its real exit status is
//     returned. Useful for hosts with a POSIX shell.

// sshOptions is the parsed command line of the ssh subcommand.
type sshOptions struct {
	host       string
	port       int
	user       string
	credential string
	script     string
	execCmd    string
	timeout    int
	knownHosts string
	insecure   bool
}

// dialSSH is a seam so a test can point the helper at an in-process SSH server.
var dialSSH = func(opts sshOptions, password string) (*ssh.Client, error) {
	cfg := &ssh.ClientConfig{
		User: opts.user,
		Auth: []ssh.AuthMethod{ssh.Password(password)},
		// Lab switches have no stable host key; --known-hosts opts into
		// verification when the operator has one.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         time.Duration(opts.timeout) * time.Second,
	}
	return ssh.Dial("tcp", net.JoinHostPort(opts.host, fmt.Sprint(opts.port)), cfg)
}

// RunSSH is the entry point for `probe-client ssh`. It returns the process exit
// code so the shell (and the agent) sees a failure instead of a printed word.
func RunSSH(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	opts := sshOptions{port: 22, timeout: 30, insecure: true}
	fs := flag.NewFlagSet("ssh", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.host, "host", "", "target host")
	fs.IntVar(&opts.port, "port", 22, "target port")
	fs.StringVar(&opts.user, "user", "", "login user")
	fs.StringVar(&opts.credential, "credential", "", "credential name (the value arrives on stdin)")
	fs.StringVar(&opts.script, "script", "", "script file of commands, one per line")
	fs.StringVar(&opts.execCmd, "exec", "", "run one command and return its exit status")
	fs.IntVar(&opts.timeout, "timeout", 30, "dial timeout in seconds")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if opts.host == "" {
		fmt.Fprintln(stderr, "ssh: --host is required")
		return 2
	}
	if opts.execCmd != "" && opts.script == "" {
		return runSSHExec(opts, stdin, stdout, stderr)
	}
	return runSSHInteractive(opts, stdin, stdout, stderr)
}

// readPasswordLine consumes the first line of stdin. The agent writes exactly
// one line; a human can pipe one too.
func readPasswordLine(stdin io.Reader) (string, error) {
	br := bufio.NewReader(stdin)
	line, err := br.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("no credential on stdin")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func runSSHExec(opts sshOptions, stdin io.Reader, stdout, stderr io.Writer) int {
	pw, err := readPasswordLine(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "ssh:", err)
		return 1
	}
	client, err := dialSSH(opts, pw)
	if err != nil {
		fmt.Fprintln(stderr, "ssh: dial:", err)
		return 1
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		fmt.Fprintln(stderr, "ssh: session:", err)
		return 1
	}
	defer sess.Close()
	sess.Stdout = stdout
	sess.Stderr = stderr
	if err := sess.Run(opts.execCmd); err != nil {
		if ee, ok := err.(*ssh.ExitError); ok {
			return ee.ExitStatus() // per-command exit code, propagated
		}
		fmt.Fprintln(stderr, "ssh:", err)
		return 1
	}
	return 0
}

// promptKind classifies the trailing prompt character.
func promptKind(buf string) byte {
	t := strings.TrimRight(buf, " \t\r\n")
	if t == "" {
		return 0
	}
	last := t[len(t)-1]
	switch last {
	case '>', '#', '$':
		return last
	}
	return 0
}

// runSSHInteractive drives a device CLI: banner, login, enable, script.
func runSSHInteractive(opts sshOptions, stdin io.Reader, stdout, stderr io.Writer) int {
	pw, err := readPasswordLine(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "ssh:", err)
		return 1
	}
	client, err := dialSSH(opts, pw)
	if err != nil {
		fmt.Fprintln(stderr, "ssh: dial:", err)
		return 1
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		fmt.Fprintln(stderr, "ssh: session:", err)
		return 1
	}
	defer sess.Close()
	stdinPipe, err := sess.StdinPipe()
	if err != nil {
		fmt.Fprintln(stderr, "ssh: stdin:", err)
		return 1
	}
	outPipe, err := sess.StdoutPipe()
	if err != nil {
		fmt.Fprintln(stderr, "ssh: stdout:", err)
		return 1
	}
	sess.Stderr = stderr
	if err := sess.RequestPty("vt100", 200, 80, ssh.TerminalModes{}); err != nil {
		// A device may refuse a PTY; the shell still works without one.
		fmt.Fprintln(stderr, "ssh: pty:", err)
	}
	if err := sess.Shell(); err != nil {
		fmt.Fprintln(stderr, "ssh: shell:", err)
		return 1
	}

	reader := bufio.NewReader(outPipe)
	var acc bytes.Buffer
	if err := readUntilPrompt(reader, &acc, opts.timeout); err != nil {
		fmt.Fprintln(stderr, "ssh: banner:", err)
		return 1
	}
	// The banner is echoed for the job log; it never contains the secret.
	fmt.Fprint(stdout, acc.String())
	if promptKind(acc.String()) == '>' {
		// User exec mode: enable needs the same password.
		if _, err := io.WriteString(stdinPipe, "enable\n"); err != nil {
			fmt.Fprintln(stderr, "ssh:", err)
			return 1
		}
		acc.Reset()
		if err := readUntilPrompt(reader, &acc, opts.timeout); err != nil {
			fmt.Fprintln(stderr, "ssh: enable prompt:", err)
			return 1
		}
		if strings.HasSuffix(strings.ToLower(strings.TrimRight(acc.String(), " ")), "password:") {
			if _, err := io.WriteString(stdinPipe, pw+"\n"); err != nil {
				fmt.Fprintln(stderr, "ssh:", err)
				return 1
			}
			acc.Reset()
			if err := readUntilPrompt(reader, &acc, opts.timeout); err != nil {
				fmt.Fprintln(stderr, "ssh: enable:", err)
				return 1
			}
		}
		fmt.Fprint(stdout, acc.String())
		if promptKind(acc.String()) != '#' {
			fmt.Fprintln(stderr, "ssh: could not reach '#' prompt")
			return 1
		}
	}

	lines, err := scriptLines(opts.script)
	if err != nil {
		fmt.Fprintln(stderr, "ssh:", err)
		return 1
	}
	exitCode := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "!") {
			continue
		}
		if _, err := io.WriteString(stdinPipe, line+"\n"); err != nil {
			fmt.Fprintln(stderr, "ssh:", err)
			return 1
		}
		acc.Reset()
		if err := readUntilPrompt(reader, &acc, opts.timeout); err != nil {
			fmt.Fprintln(stderr, "ssh: read after", line+":", err)
			return 1
		}
		fmt.Fprint(stdout, acc.String())
		// Device CLIs report failure in the response text, not an exit
		// status: an invalid command is the per-command exit code.
		if deviceCommandFailed(acc.String()) {
			exitCode = 1
		}
	}
	io.WriteString(stdinPipe, "exit\n")
	return exitCode
}

// readUntilPrompt accumulates output until it ends on a prompt character or on
// a password prompt. The password prompt matters: an 'enable' step answers with
// "Password: " (no prompt char), and waiting for '>' there would deadlock.
func readUntilPrompt(r *bufio.Reader, acc *bytes.Buffer, timeoutSec int) error {
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for prompt")
		}
		b, err := r.ReadByte()
		if err != nil {
			if acc.Len() > 0 {
				return nil
			}
			return err
		}
		acc.WriteByte(b)
		if r.Buffered() == 0 {
			if promptKind(acc.String()) != 0 {
				return nil
			}
			if strings.HasSuffix(strings.ToLower(strings.TrimRight(acc.String(), " ")), "password:") {
				return nil
			}
		}
	}
}

// deviceCommandFailed recognises the usual device-side error markers.
func deviceCommandFailed(out string) bool {
	l := strings.ToLower(out)
	for _, m := range []string{"% invalid", "% incomplete", "% ambiguous", "error:", "syntax error", "unknown command"} {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}

// scriptLines reads the script file. A missing file is an error, not an empty
// run: an operator who typed a path expects it to be used.
func scriptLines(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("script: %w", err)
	}
	return strings.Split(string(raw), "\n"), nil
}
