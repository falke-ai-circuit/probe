package upgrade

import (
	"context"
	"errors"
	"strings"
)

// ErrPermissionDenied is returned by a Transport when the agent refused an
// operation on permission grounds. On this path it means G-SANDBOX refused the
// staged path (P2-02 section 2.1), which is a stop condition, not a retry.
var ErrPermissionDenied = errors.New("permission denied")

// IsPermissionDenied reports whether err is a permission refusal.
func IsPermissionDenied(err error) bool {
	return errors.Is(err, ErrPermissionDenied)
}

// ExecRequest mirrors protocol.ExecParams: one command string, an optional
// typed working directory, and a timeout in seconds.
type ExecRequest struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
	WorkDir string `json:"workdir,omitempty"`
}

// ExecResult mirrors protocol.ExecResult. Stderr is always empty on Windows;
// everything the command prints lands in Stdout.
type ExecResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out"`
}

// WriteRequest is one fs-write. Data is raw bytes here; the wire form is
// base64 (protocol.FSParams.Data).
type WriteRequest struct {
	Path   string
	Data   []byte
	Offset int
	Mode   string
}

// WriteResult mirrors protocol.FSWriteResult. Written is len(Data); it is not
// a proof of the bytes on disk.
type WriteResult struct {
	Written int    `json:"written"`
	Path    string `json:"path"`
}

// HashResult mirrors protocol.FSHashResult.
type HashResult struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// StatResult mirrors protocol.FSStatResult.
type StatResult struct {
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime int64  `json:"mod_time"`
	IsDir   bool   `json:"is_dir"`
	Exists  bool   `json:"exists"`
}

// ReadRequest is one fs-read.
type ReadRequest struct {
	Path   string
	Offset int
	Limit  int
}

// ReadResult carries decoded bytes; the wire form is base64.
type ReadResult struct {
	Data []byte
	Size int64
}

// Transport is the caller's view of the OLD client. It is exactly the set of
// primitives K1 allows: exec, fs-write, fs-read, fs-hash, fs-stat.
type Transport interface {
	Exec(ctx context.Context, req ExecRequest) (ExecResult, error)
	FsWrite(ctx context.Context, req WriteRequest) (WriteResult, error)
	FsHash(ctx context.Context, path string) (HashResult, error)
	FsStat(ctx context.Context, path string) (StatResult, error)
	FsRead(ctx context.Context, req ReadRequest) (ReadResult, error)
}

// normalizeAgentError maps the agent's free-prose error text onto the single
// sentinel this path branches on. A3: causes are not machine-distinguishable
// beyond this.
func normalizeAgentError(code, message string) error {
	lower := strings.ToLower(message)
	if code == "AGENT_ERROR" || code == "INTERNAL_ERROR" || code == "FORBIDDEN" {
		if strings.Contains(lower, "permission denied") || strings.Contains(lower, "denied") {
			return ErrPermissionDenied
		}
	}
	return errors.New(code + ": " + message)
}
