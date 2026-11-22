package agent

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/platform"
	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// P8-05: "never an ok without proof". These tests read the real filesystem in a
// temp dir, because the incident they encode was a handler that reported an ok
// while the disk held different bytes.

func fsAgentOnDisk(t *testing.T) *Agent {
	t.Helper()
	return &Agent{plat: platform.New("p805-test")}
}

func writeEnvelope(t *testing.T, fields map[string]interface{}) protocol.Envelope {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return protocol.Envelope{ID: "env-1", Type: protocol.TypeFileSave, Params: raw}
}

func decodeWrite(t *testing.T, resp protocol.Envelope) protocol.FSWriteResult {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s (%s)", resp.Error.Code, resp.Error.Message)
	}
	var res protocol.FSWriteResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return res
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}

// T5a: data empty with no expected_size is a refusal, and nothing is created.
func TestFSWriteRefusesZeroByteWithoutExpectedSize(t *testing.T) {
	a := fsAgentOnDisk(t)
	path := filepath.Join(t.TempDir(), "zero.bin")

	resp := a.handleFSWrite(writeEnvelope(t, map[string]interface{}{"path": path, "data": ""}))

	if resp.Error == nil {
		t.Fatalf("0-byte write returned ok: %s", string(mustJSON(t, resp.Result)))
	}
	if resp.Error.Code != protocol.ErrFSWriteEmptyData {
		t.Fatalf("code = %q, want %q", resp.Error.Code, protocol.ErrFSWriteEmptyData)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("refused write left a file behind: %v", err)
	}
}

// expected_size=0 is the legitimate way to create an empty file.
func TestFSWriteEmptyFileWhenExpectedSizeZero(t *testing.T) {
	a := fsAgentOnDisk(t)
	path := filepath.Join(t.TempDir(), "empty.bin")

	resp := a.handleFSWrite(writeEnvelope(t, map[string]interface{}{
		"path": path, "data": "", "expected_size": 0,
	}))
	res := decodeWrite(t, resp)

	if res.Size != 0 {
		t.Fatalf("size = %d, want 0", res.Size)
	}
	if res.Sha256 != sha256Hex(nil) {
		t.Fatalf("sha256 = %q, want sha256 of empty string", res.Sha256)
	}
	if !res.Verified {
		t.Fatal("verified = false on a successful empty-file create")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("empty file was not created: %v", err)
	}
}

// A normal write reports the bytes on disk, not the bytes in the request.
func TestFSWriteReportsDiskProof(t *testing.T) {
	a := fsAgentOnDisk(t)
	path := filepath.Join(t.TempDir(), "ticket.txt")
	payload := []byte("line one\nline two\n")

	res := decodeWrite(t, a.handleFSWrite(writeEnvelope(t, map[string]interface{}{
		"path": path, "data": base64.StdEncoding.EncodeToString(payload),
	})))

	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if res.Written != len(payload) {
		t.Fatalf("written = %d, want %d", res.Written, len(payload))
	}
	if res.Size != int64(len(onDisk)) {
		t.Fatalf("size = %d, want %d (bytes on disk)", res.Size, len(onDisk))
	}
	if res.Sha256 != sha256Hex(onDisk) {
		t.Fatalf("sha256 = %q, want %q", res.Sha256, sha256Hex(onDisk))
	}
	if !res.Verified {
		t.Fatal("verified = false after a stat+digest of the written file")
	}
}

func TestFSWriteSizeMismatch(t *testing.T) {
	a := fsAgentOnDisk(t)
	path := filepath.Join(t.TempDir(), "short.bin")
	payload := []byte("0123456789")

	resp := a.handleFSWrite(writeEnvelope(t, map[string]interface{}{
		"path": path, "data": base64.StdEncoding.EncodeToString(payload), "expected_size": 186,
	}))
	if resp.Error == nil || resp.Error.Code != protocol.ErrFSWriteSizeMismatch {
		t.Fatalf("error = %+v, want %s", resp.Error, protocol.ErrFSWriteSizeMismatch)
	}
}

func TestFSWriteDigestMismatch(t *testing.T) {
	a := fsAgentOnDisk(t)
	path := filepath.Join(t.TempDir(), "digest.bin")
	payload := []byte("payload")

	resp := a.handleFSWrite(writeEnvelope(t, map[string]interface{}{
		"path": path, "data": base64.StdEncoding.EncodeToString(payload),
		"expected_sha256": strings.Repeat("a", 64),
	}))
	if resp.Error == nil || resp.Error.Code != protocol.ErrFSWriteDigestMismatch {
		t.Fatalf("error = %+v, want %s", resp.Error, protocol.ErrFSWriteDigestMismatch)
	}
}

// Matching expectations are verified, not trusted: the same write passes when
// the caller quotes the true size and digest.
func TestFSWriteAcceptsMatchingExpectations(t *testing.T) {
	a := fsAgentOnDisk(t)
	path := filepath.Join(t.TempDir(), "match.bin")
	payload := []byte("verified payload")

	res := decodeWrite(t, a.handleFSWrite(writeEnvelope(t, map[string]interface{}{
		"path": path, "data": base64.StdEncoding.EncodeToString(payload),
		"expected_size": len(payload), "expected_sha256": sha256Hex(payload),
	})))
	if !res.Verified || res.Size != int64(len(payload)) {
		t.Fatalf("result = %+v", res)
	}
}

// T5b: a limited read declares the file's total size and that it was cut.
func TestFSReadDeclaresTruncation(t *testing.T) {
	a := fsAgentOnDisk(t)
	path := filepath.Join(t.TempDir(), "big.txt")
	content := []byte(strings.Repeat("A", 5000))
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	resp := a.handleFSRead(fsEnvelope(t, protocol.TypeFSRead,
		fmt.Sprintf(`{"path":%q,"offset":0,"limit":100}`, path)))
	if resp.Error != nil {
		t.Fatalf("read error: %s", resp.Error.Message)
	}
	var res protocol.FSReadResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Size != 100 {
		t.Fatalf("size = %d, want 100", res.Size)
	}
	if res.TotalSize != 5000 {
		t.Fatalf("total_size = %d, want 5000", res.TotalSize)
	}
	if !res.Truncated {
		t.Fatal("truncated = false on a window that ends before EOF")
	}
	if res.Sha256 != sha256Hex(content[:100]) {
		t.Fatalf("sha256 = %q, want digest of the returned window", res.Sha256)
	}
}

func TestFSReadFullWindowIsNotTruncated(t *testing.T) {
	a := fsAgentOnDisk(t)
	path := filepath.Join(t.TempDir(), "small.txt")
	content := []byte("complete")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	resp := a.handleFSRead(fsEnvelope(t, protocol.TypeFSRead, fmt.Sprintf(`{"path":%q}`, path)))
	if resp.Error != nil {
		t.Fatalf("read error: %s", resp.Error.Message)
	}
	var res protocol.FSReadResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Truncated || res.TotalSize != int64(len(content)) || res.Size != int64(len(content)) {
		t.Fatalf("result = %+v, want full untruncated read", res)
	}
}

func TestFSReadOffsetTruncation(t *testing.T) {
	a := fsAgentOnDisk(t)
	path := filepath.Join(t.TempDir(), "tail.txt")
	content := []byte("0123456789")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	resp := a.handleFSRead(fsEnvelope(t, protocol.TypeFSRead,
		fmt.Sprintf(`{"path":%q,"offset":4,"limit":3}`, path)))
	var res protocol.FSReadResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Offset != 4 || res.Size != 3 || res.TotalSize != 10 || !res.Truncated {
		t.Fatalf("result = %+v, want offset 4, size 3, total 10, truncated", res)
	}
	if res.Sha256 != sha256Hex(content[4:7]) {
		t.Fatalf("sha256 does not cover the returned window")
	}
}

// fs-list reports its own length, so a caller does not have to trust the array.
func TestFSListReportsCount(t *testing.T) {
	a := &Agent{plat: &fsStubPlatform{listFn: func(string) ([]protocol.FSEntry, error) {
		return []protocol.FSEntry{{Name: "a"}, {Name: "b"}}, nil
	}}}
	resp := a.handleFSList(fsEnvelope(t, protocol.TypeFSList, `{"path":"/tmp"}`))
	if resp.Error != nil {
		t.Fatalf("list error: %s", resp.Error.Message)
	}
	var res protocol.FSListResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Count != 2 || len(res.Entries) != 2 {
		t.Fatalf("count = %d, entries = %d, want 2/2", res.Count, len(res.Entries))
	}
}

func TestFSDeadlineResolution(t *testing.T) {
	cases := []struct {
		ms   int
		want time.Duration
	}{
		{0, 30 * time.Second},
		{5000, 5 * time.Second},
		{500000, 120 * time.Second},
	}
	for _, c := range cases {
		got := fsDeadline(protocol.FSParams{DeadlineMs: c.ms}, 30*time.Second)
		if got != c.want {
			t.Errorf("deadline_ms %d -> %s, want %s", c.ms, got, c.want)
		}
	}
}

// Every fs-* handler answers a stalled platform with a typed TIMEOUT that names
// the op, the path and the elapsed time.
func TestFSWriteDeadlineNamesOpPathAndElapsed(t *testing.T) {
	old := fsWriteDeadline
	fsWriteDeadline = 20 * time.Millisecond
	defer func() { fsWriteDeadline = old }()

	// boundedFS is the single choke point every fs-* handler goes through; the
	// wrapper is asserted directly so the message contract is tested without a
	// 200 ms disk stall per handler.
	_, err := boundedFS(fsWriteDeadline, "fs-write", "C:/fileserver/share/ticket.pdf", func() (int, error) {
		time.Sleep(200 * time.Millisecond)
		return 0, nil
	})
	if err == nil {
		t.Fatal("no error from a stalled write")
	}
	msg := err.Error()
	for _, want := range []string{"fs-write", "C:/fileserver/share/ticket.pdf", "elapsed", "ms"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q does not contain %q", msg, want)
		}
	}
}

func mustJSON(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	if len(raw) == 0 {
		return []byte("<empty>")
	}
	return raw
}
