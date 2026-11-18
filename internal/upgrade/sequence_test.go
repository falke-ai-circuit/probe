package upgrade

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fakeTransport is an in-memory OLD client: same fs semantics as the agent
// handlers (create/truncate at mode=create, seek+write otherwise).
type fakeTransport struct {
	files      map[string][]byte
	reads      map[string][]byte
	writes     []WriteRequest
	execs      []ExecRequest
	writeCalls int
	writeHook  func(call int, req WriteRequest) ([]byte, error)
	execFn     func(ExecRequest) (ExecResult, error)
	statFn     func(string) (StatResult, error)
	// shortWritePath, when set, makes FsWrite report 0 bytes written for a path
	// with this suffix while still landing the bytes: A4's "Written is not
	// evidence" case, where only the read-back hash can tell.
	shortWritePath string
}

func newFake() *fakeTransport {
	return &fakeTransport{files: map[string][]byte{}, reads: map[string][]byte{}}
}

func (f *fakeTransport) Exec(_ context.Context, req ExecRequest) (ExecResult, error) {
	f.execs = append(f.execs, req)
	if f.execFn != nil {
		return f.execFn(req)
	}
	return ExecResult{ExitCode: 0}, nil
}

func (f *fakeTransport) FsWrite(_ context.Context, req WriteRequest) (WriteResult, error) {
	f.writeCalls++
	f.writes = append(f.writes, req)
	payload := req.Data
	if f.writeHook != nil {
		p, err := f.writeHook(f.writeCalls, req)
		if err != nil {
			return WriteResult{}, err
		}
		if p != nil {
			payload = p
		}
	}
	if req.Mode == "create" {
		f.files[req.Path] = append([]byte(nil), payload...)
	} else {
		cur := f.files[req.Path]
		need := req.Offset + len(payload)
		if need > len(cur) {
			grown := make([]byte, need)
			copy(grown, cur)
			cur = grown
		}
		copy(cur[req.Offset:], payload)
		f.files[req.Path] = cur
	}
	// The agent reports len(data) it was handed, not what landed (A4).
	written := len(req.Data)
	if f.shortWritePath != "" && strings.HasSuffix(req.Path, f.shortWritePath) {
		written = 0
	}
	return WriteResult{Written: written, Path: req.Path}, nil
}

func (f *fakeTransport) FsHash(_ context.Context, path string) (HashResult, error) {
	data, ok := f.files[path]
	if !ok {
		return HashResult{}, fmt.Errorf("fs-hash: %s: not found", path)
	}
	return HashResult{Path: path, SHA256: SHA256Hex(data), Size: int64(len(data))}, nil
}

func (f *fakeTransport) FsStat(_ context.Context, path string) (StatResult, error) {
	if f.statFn != nil {
		return f.statFn(path)
	}
	data, ok := f.files[path]
	if !ok {
		return StatResult{Exists: false}, nil
	}
	return StatResult{Exists: true, Size: int64(len(data))}, nil
}

func (f *fakeTransport) FsRead(_ context.Context, req ReadRequest) (ReadResult, error) {
	if data, ok := f.reads[req.Path]; ok {
		return ReadResult{Data: data, Size: int64(len(data))}, nil
	}
	if data, ok := f.files[req.Path]; ok {
		return ReadResult{Data: data, Size: int64(len(data))}, nil
	}
	return ReadResult{}, fmt.Errorf("fs-read: %s: not found", req.Path)
}

func stageRequest(payload []byte) StageRequest {
	return StageRequest{
		Dir: `C:\Probe`,
		Manifest: Manifest{
			Canonical:  `C:\Probe\probe-client.exe`,
			ServerHost: "10.0.0.5",
			ServerPort: 443,
			OldPID:     1234,
			Name:       "vegas",
		},
		Payload: payload,
	}
}

func TestStagePayloadChunksAndArms(t *testing.T) {
	payload := bytes.Repeat([]byte{0xA5}, 2*ChunkSize+10)
	fake := newFake()
	runner := NewRunner(fake)

	res, err := runner.StagePayload(context.Background(), stageRequest(payload))
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if res.Chunks != 3 {
		t.Fatalf("chunks = %d, want 3", res.Chunks)
	}
	if res.Canonical != `C:\Probe\probe-client.exe` || res.StagedPath != `C:\Probe\probe-client.exe.next` {
		t.Fatalf("paths = %+v", res)
	}
	// Chunk offsets are sequential and ordering is part of correctness.
	wantOffsets := []int{0, ChunkSize, 2 * ChunkSize}
	for i, w := range fake.writes[:3] {
		if w.Offset != wantOffsets[i] {
			t.Errorf("write %d offset = %d, want %d", i, w.Offset, wantOffsets[i])
		}
		if w.Path != res.StagedPath {
			t.Errorf("write %d path = %q", i, w.Path)
		}
	}
	if fake.writes[0].Mode != "create" {
		t.Errorf("first chunk mode = %q, want create", fake.writes[0].Mode)
	}
	if fake.writes[1].Mode != "0644" {
		t.Errorf("subsequent chunk mode = %q, want 0644", fake.writes[1].Mode)
	}
	if !bytes.Equal(fake.files[res.StagedPath], payload) {
		t.Fatal("assembled payload differs from the source bytes")
	}
	if res.PayloadSHA256 != SHA256Hex(payload) {
		t.Fatalf("payload digest = %s", res.PayloadSHA256)
	}
	// SW-05/SW-06: the script and manifest are beside the payload and pinned.
	if !bytes.Equal(fake.files[res.BootPath], BootScript()) {
		t.Fatal("p2boot.ps1 was not written verbatim")
	}
	if res.BootSHA256 != BootScriptSHA256() {
		t.Fatalf("boot digest = %s", res.BootSHA256)
	}
	if !IsASCII(fake.files[res.ManifestPath]) {
		t.Fatal("manifest on host is not ASCII")
	}
	m, err := ParseManifest(fake.files[res.ManifestPath])
	if err != nil {
		t.Fatalf("staged manifest does not parse: %v", err)
	}
	if m.SHA256Payload != res.PayloadSHA256 || m.SizePayload != int64(len(payload)) {
		t.Fatalf("manifest disagrees with staged bytes: %+v", m)
	}
	if m.OldPID != 1234 || m.NRollbackS != DefaultNRollbackS {
		t.Fatalf("manifest lost discovery data: %+v", m)
	}
	if res.ManifestSHA256 != SHA256Hex(fake.files[res.ManifestPath]) {
		t.Fatal("V4 manifest digest is not of the staged bytes")
	}
}

func TestStagePayloadRefusesBeforeAnyWrite(t *testing.T) {
	fake := newFake()
	req := stageRequest([]byte("payload"))
	req.SandboxDir = `C:\Restricted`
	_, err := NewRunner(fake).StagePayload(context.Background(), req)
	var se *StepError
	if !errors.As(err, &se) || se.Code != CodeSandboxRefused {
		t.Fatalf("expected SANDBOX_REFUSED, got %v", err)
	}
	if len(fake.writes) != 0 {
		t.Fatalf("G-SANDBOX must refuse before the first write, got %d writes", len(fake.writes))
	}
}

func TestStagePayloadPermissionDeniedOnWrite(t *testing.T) {
	fake := newFake()
	fake.writeHook = func(_ int, _ WriteRequest) ([]byte, error) {
		return nil, ErrPermissionDenied
	}
	_, err := NewRunner(fake).StagePayload(context.Background(), stageRequest([]byte("payload")))
	var se *StepError
	if !errors.As(err, &se) || se.Code != CodeSandboxRefused {
		t.Fatalf("expected SANDBOX_REFUSED, got %v", err)
	}
}

func TestStagePayloadReplaysCorruptChunk(t *testing.T) {
	payload := bytes.Repeat([]byte{0x5A}, ChunkSize+100)
	fake := newFake()
	// First chunk lands with a flipped byte: {written=N} looks healthy (F2).
	fake.writeHook = func(call int, req WriteRequest) ([]byte, error) {
		if call == 1 {
			bad := append([]byte(nil), req.Data...)
			bad[0] ^= 0xFF
			return bad, nil
		}
		return nil, nil
	}
	res, err := NewRunner(fake).StagePayload(context.Background(), stageRequest(payload))
	if err != nil {
		t.Fatalf("stage should recover by replaying from offset 0: %v", err)
	}
	if !bytes.Equal(fake.files[res.StagedPath], payload) {
		t.Fatal("replay did not repair the staged file")
	}
	// 2 chunks, then a full replay of 2 chunks, then SW-05's two arming writes.
	stagedWrites := 0
	for _, w := range fake.writes {
		if w.Path == res.StagedPath {
			stagedWrites++
		}
	}
	if stagedWrites != 4 {
		t.Fatalf("staged writes = %d, want 4 (2 chunks + replay)", stagedWrites)
	}
	var replay []WriteRequest
	for _, w := range fake.writes {
		if w.Path == res.StagedPath {
			replay = append(replay, w)
		}
	}
	if replay[2].Offset != 0 || replay[2].Mode != "create" {
		t.Fatalf("replay must restart at offset 0 with create: %+v", replay[2])
	}
}

func TestStagePayloadSecondDigestMismatchAborts(t *testing.T) {
	fake := newFake()
	fake.writeHook = func(call int, req WriteRequest) ([]byte, error) {
		bad := append([]byte(nil), req.Data...)
		bad[0] ^= 0xFF
		return bad, nil
	}
	_, err := NewRunner(fake).StagePayload(context.Background(), stageRequest([]byte("payload")))
	var se *StepError
	if !errors.As(err, &se) || se.Code != CodeDigestMismatch {
		t.Fatalf("expected DIGEST_MISMATCH, got %v", err)
	}
	if se.Step != "SW-03" {
		t.Fatalf("step = %s", se.Step)
	}
}

func TestStagePayloadManifestDigestDisagreement(t *testing.T) {
	fake := newFake()
	req := stageRequest([]byte("payload"))
	req.Manifest.SHA256Payload = strings.Repeat("c", 64)
	_, err := NewRunner(fake).StagePayload(context.Background(), req)
	var se *StepError
	if !errors.As(err, &se) || se.Code != CodeDigestMismatch || se.Step != "SW-01" {
		t.Fatalf("expected SW-01 DIGEST_MISMATCH, got %v", err)
	}
	if len(fake.writes) != 0 {
		t.Fatal("nothing may be written when the caller's digest disagrees")
	}
}

func TestPreflightRefusalCarriesReport(t *testing.T) {
	fake := newFake()
	fake.execFn = func(req ExecRequest) (ExecResult, error) {
		if !strings.Contains(req.Command, "-Preflight") {
			t.Fatalf("unexpected command %q", req.Command)
		}
		return ExecResult{
			ExitCode: 2,
			Stdout:   `{"phase":"PREFLIGHT","ok":false,"checks":{"payload_hash":false},"old_pid":99}`,
		}, nil
	}
	report, err := NewRunner(fake).Preflight(context.Background(), `C:\Probe`)
	var se *StepError
	if !errors.As(err, &se) || se.Code != CodePreflightRefused {
		t.Fatalf("expected PREFLIGHT_REFUSED, got %v", err)
	}
	if report.OldPID != 99 || report.OK {
		t.Fatalf("report = %+v", report)
	}
	if fake.execs[0].WorkDir != `C:\Probe` || fake.execs[0].Timeout != PreflightTimeoutS {
		t.Fatalf("exec request = %+v", fake.execs[0])
	}
}

func TestPreflightSuccess(t *testing.T) {
	fake := newFake()
	fake.execFn = func(ExecRequest) (ExecResult, error) {
		return ExecResult{ExitCode: 0, Stdout: "Windows PowerShell banner\r\n{\"phase\":\"PREFLIGHT\",\"ok\":true,\"checks\":{\"old_alive\":true},\"old_pid\":1234}\r\n"}, nil
	}
	report, err := NewRunner(fake).Preflight(context.Background(), `C:\Probe`)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if !report.OK || report.OldPID != 1234 {
		t.Fatalf("report = %+v", report)
	}
}

func TestLaunch(t *testing.T) {
	fake := newFake()
	fake.execFn = func(req ExecRequest) (ExecResult, error) {
		if !strings.Contains(req.Command, "-Launch") {
			t.Fatalf("unexpected command %q", req.Command)
		}
		return ExecResult{ExitCode: 0, Stdout: `{"phase":"LAUNCH","commit_pid":4242}`}, nil
	}
	report, err := NewRunner(fake).Launch(context.Background(), `C:\Probe`)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if report.CommitPID != 4242 {
		t.Fatalf("commit pid = %d", report.CommitPID)
	}
	if fake.execs[0].Timeout != LaunchTimeoutS {
		t.Fatalf("launch timeout = %d", fake.execs[0].Timeout)
	}
}

func TestLaunchReportsNothingMoved(t *testing.T) {
	fake := newFake()
	fake.execFn = func(ExecRequest) (ExecResult, error) {
		return ExecResult{ExitCode: 4, Stdout: "LAUNCH_FAIL"}, nil
	}
	_, err := NewRunner(fake).Launch(context.Background(), `C:\Probe`)
	var se *StepError
	if !errors.As(err, &se) || se.Code != CodeLaunchFailed {
		t.Fatalf("expected LAUNCH_FAILED, got %v", err)
	}
}

func TestReadJournalAndAnalyze(t *testing.T) {
	fake := newFake()
	path := `C:\Probe\p2boot.journal`
	fake.reads[path] = []byte(strings.Join([]string{
		"ts=1 COMMIT_START pid=10 mode=swap",
		"ts=2 PARKED",
		"ts=3 PLACED",
		"ts=4 NEW_STARTED pid=20",
		"ts=5 NEW_ESTABLISHED endpoint=10.0.0.5:443",
		"ts=6 OLD_KILLED pid=10",
		"ts=7 DONE sha256=abcdef0123456789 old_sha256=0123456789abcdef",
	}, "\r\n"))
	events, err := NewRunner(fake).ReadJournal(context.Background(), `C:\Probe`)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	st := Analyze(events)
	if st.Outcome != OutcomeDone || st.Events != 7 {
		t.Fatalf("status = %+v", st)
	}
	if st.Last.Field("sha256") != "abcdef0123456789" {
		t.Fatalf("last fields = %v", st.Last.Fields)
	}
}

func TestVerifyCanonical(t *testing.T) {
	fake := newFake()
	path := `C:\Probe\probe-client.exe`
	fake.files[path] = []byte("new-binary")
	runner := NewRunner(fake)
	if err := runner.VerifyCanonical(context.Background(), path, SHA256Hex([]byte("new-binary"))); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := runner.VerifyCanonical(context.Background(), path, SHA256Hex([]byte("other"))); err == nil {
		t.Fatal("mismatched digest must fail")
	}
}

func TestResolveCanonicalThroughTransport(t *testing.T) {
	fake := newFake()
	fake.files[`C:\Probe\probe-client.exe`] = []byte("bin")
	res, err := NewRunner(fake).ResolveCanonical(context.Background(), `C:\Probe\probe-client.exe.next`, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Canonical != `C:\Probe\probe-client.exe` || res.Source != SourceDiscovered {
		t.Fatalf("resolution = %+v", res)
	}
}
