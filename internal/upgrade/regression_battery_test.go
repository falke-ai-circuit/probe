package upgrade

// P3-03 regression battery. Four properties, checked by structural anchors
// inside the embedded script: removing an anchored step turns the test red.
// Semantic rewrites that leave the anchors intact are out of scope, with two
// named exceptions that are guarded explicitly, because each was a live way to
// keep the anchors and lose the behaviour:
//
//	(a) InstanceOf: $OldSha is never assigned inside Invoke-Rollback, and the
//	    restore check compares against $OldSha (not a copy of $restoredHash).
//	(b) Reset: the park loop assigns $races exactly once, as `$races = $races + 1`.
//
//	R1 SW-00..SW-08 run end-to-end on the OLD client's five primitives only.
//	R2 a failed post-rename check rolls back (quarantine, verify OLD_SHA256,
//	   no successor start, rollback outcome) instead of starting anything.
//	R3 the successor argv is `--config <abs>` or zero-arg, never `connect`.
//	R4 the 183 / OLD_RACE ladder is bounded and does not burn the delay ladder.
//
// The bootstrap and commit steps live in the embedded p2boot.ps1, so the R2/R3/
// R4 assertions are structural (ordered anchors inside a brace-matched function
// body) plus, where the Go side owns the behaviour, executed.

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// psFunction returns the body of `function <name>` from an embedded script,
// brace matched so a mutation inside the function shows up here.
func psFunction(t *testing.T, script, name string) string {
	t.Helper()
	marker := "function " + name
	start := strings.Index(script, marker)
	if start < 0 {
		t.Fatalf("p2boot.ps1 has no %q (the function was removed)", marker)
	}
	rest := script[start+len(marker):]
	open := strings.Index(rest, "{")
	if open < 0 {
		t.Fatalf("function %s has no body", name)
	}
	depth := 0
	for i := open; i < len(rest); i++ {
		switch rest[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return rest[open : i+1]
			}
		}
	}
	t.Fatalf("function %s body is unbalanced", name)
	return ""
}

// requireOrder asserts every anchor is present and appears in the given order.
func requireOrder(t *testing.T, body, label string, anchors []string) {
	t.Helper()
	prev := -1
	for _, a := range anchors {
		idx := strings.Index(body, a)
		if idx < 0 {
			t.Errorf("%s is missing %q (the step was removed or renamed)", label, a)
			return
		}
		if idx < prev {
			t.Errorf("%s runs %q out of order", label, a)
			return
		}
		prev = idx
	}
}

// braceBlock returns the text from `open` to its matching closing brace, so a
// mutation inside a loop body can be inspected on its own rather than through
// the whole function. `open` must end at the construct's opening brace.
func braceBlock(t *testing.T, body, open string) string {
	t.Helper()
	start := strings.Index(body, open)
	if start < 0 {
		t.Fatalf("no %q (the construct was removed or renamed)", open)
	}
	depth := 0
	for i := start; i < len(body); i++ {
		switch body[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return body[start : i+1]
			}
		}
	}
	t.Fatalf("%q body is unbalanced", open)
	return ""
}

// ---------------------------------------------------------------------------
// R1 - bootstrap on the OLD protocol
// ---------------------------------------------------------------------------

// transportMethodCensus is the whole of what the old agent contract offers
// (P2-02 K1). A new primitive added to Transport must fail this test rather
// than sneak through as "just another endpoint".
var transportMethodCensus = []string{"Exec", "FsHash", "FsRead", "FsStat", "FsWrite"}

// callName maps a Transport method onto the label recordingTransport records,
// so the allowed set is derived from the census rather than restated.
func callName(method string) string {
	if strings.HasPrefix(method, "Fs") {
		return "fs-" + strings.ToLower(method[2:])
	}
	return strings.ToLower(method)
}

// recordingTransport is an OLD client double: it implements the five primitives
// and nothing else, and records which of them each step reached for.
type recordingTransport struct {
	*fakeTransport
	order []string
	reads []ReadRequest
}

func (r *recordingTransport) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	r.order = append(r.order, "exec")
	return r.fakeTransport.Exec(ctx, req)
}

func (r *recordingTransport) FsWrite(ctx context.Context, req WriteRequest) (WriteResult, error) {
	r.order = append(r.order, "fs-write")
	return r.fakeTransport.FsWrite(ctx, req)
}

func (r *recordingTransport) FsHash(ctx context.Context, path string) (HashResult, error) {
	r.order = append(r.order, "fs-hash")
	return r.fakeTransport.FsHash(ctx, path)
}

func (r *recordingTransport) FsStat(ctx context.Context, path string) (StatResult, error) {
	r.order = append(r.order, "fs-stat")
	return r.fakeTransport.FsStat(ctx, path)
}

func (r *recordingTransport) FsRead(ctx context.Context, req ReadRequest) (ReadResult, error) {
	r.order = append(r.order, "fs-read")
	r.reads = append(r.reads, req)
	return r.fakeTransport.FsRead(ctx, req)
}

func TestBootstrapOldProtocolSequences(t *testing.T) {
	// The interface itself must stay at five methods. Anything else is a new
	// primitive the old client does not have.
	iface := reflect.TypeOf((*Transport)(nil)).Elem()
	var names []string
	for i := 0; i < iface.NumMethod(); i++ {
		names = append(names, iface.Method(i).Name)
	}
	if iface.NumMethod() != len(transportMethodCensus) {
		t.Fatalf("Transport exposes %d methods %v; the old client offers only %v",
			iface.NumMethod(), names, transportMethodCensus)
	}
	for _, want := range transportMethodCensus {
		if _, ok := iface.MethodByName(want); !ok {
			t.Fatalf("Transport lost %q: %v", want, names)
		}
	}

	// The production transport must still satisfy exactly that contract.
	var _ Transport = (*HTTPTransport)(nil)

	const (
		canonical = `C:\Probe\probe-client.exe`
		dir       = `C:\Probe`
	)
	payload := make([]byte, 2*ChunkSize+10)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	fake := newFake()
	fake.files[canonical] = []byte("old-image-bytes")
	fake.statFn = func(path string) (StatResult, error) {
		if data, ok := fake.files[path]; ok {
			return StatResult{Exists: true, Size: int64(len(data))}, nil
		}
		return StatResult{Exists: false}, nil
	}
	var execCommands []string
	fake.execFn = func(req ExecRequest) (ExecResult, error) {
		execCommands = append(execCommands, req.Command)
		switch req.Command {
		case ExecDiscover:
			return ExecResult{Stdout: fmt.Sprintf(
				`{"pid":4321,"exe":%q,"dir":%q,"name":"vegas","cmdline":%q}`,
				canonical, dir, canonical)}, nil
		case ExecPreflight:
			return ExecResult{Stdout: `{"phase":"preflight","ok":true,"checks":{"old_hash":true},"old_pid":4321}`}, nil
		case ExecLaunch:
			return ExecResult{Stdout: `{"phase":"launch","commit_pid":9999}`}, nil
		default:
			t.Errorf("SW-00..SW-08 executed an unknown command %q", req.Command)
			return ExecResult{ExitCode: 9}, nil
		}
	}

	tr := &recordingTransport{fakeTransport: fake}
	runner := NewRunner(tr)
	// Chunk boundaries are pinned at the legacy size so the census sees the
	// multi-chunk path; the carrier default is covered separately.
	runner.ChunkSize = ChunkSize
	ctx := context.Background()

	// A4 discovery is the prerequisite SW-00 consumes.
	discovered, err := runner.Discover(ctx, dir)
	if err != nil {
		t.Fatalf("A4 discover: %v", err)
	}
	if discovered.Exe != canonical {
		t.Fatalf("A4 returned exe %q", discovered.Exe)
	}

	// SW-00: canonical resolution through fs-stat.
	res, err := runner.ResolveCanonical(ctx, discovered.Exe, "")
	if err != nil {
		t.Fatalf("SW-00 resolve: %v", err)
	}
	if res.Canonical != canonical || res.Source != SourceDiscovered {
		t.Fatalf("SW-00 returned %+v", res)
	}

	// SW-01..SW-06: stage the payload beside the canonical image.
	req := stageRequest(payload)
	staged, err := runner.StagePayload(ctx, req)
	if err != nil {
		t.Fatalf("SW-01..SW-06 stage: %v", err)
	}
	if staged.Chunks != 3 {
		t.Errorf("expected 3 chunks for %d bytes, got %d", len(payload), staged.Chunks)
	}
	if got := fake.files[staged.StagedPath]; !strings.EqualFold(SHA256Hex(got), SHA256Hex(payload)) {
		t.Errorf("staged bytes do not hash to the payload digest")
	}
	if got := fake.files[staged.BootPath]; SHA256Hex(got) != BootScriptSHA256() {
		t.Errorf("armed boot script is not the embedded script")
	}

	// SW-07: preflight.
	if _, err := runner.Preflight(ctx, dir); err != nil {
		t.Fatalf("SW-07 preflight: %v", err)
	}

	// SW-08: the boundary.
	launch, err := runner.Launch(ctx, dir)
	if err != nil {
		t.Fatalf("SW-08 launch: %v", err)
	}
	if launch.CommitPID != 9999 {
		t.Fatalf("SW-08 returned commit_pid %d", launch.CommitPID)
	}

	// Every call the sequence made must be one of the five primitives. The set
	// is derived from the census so a sixth primitive cannot drift the two.
	allowed := map[string]bool{}
	for _, method := range transportMethodCensus {
		allowed[callName(method)] = true
	}
	used := map[string]bool{}
	for _, call := range tr.order {
		if !allowed[call] {
			t.Errorf("sequence reached for %q, which the old client does not expose", call)
		}
		used[call] = true
	}
	for _, want := range []string{"exec", "fs-write", "fs-hash", "fs-stat"} {
		if !used[want] {
			t.Errorf("SW-00..SW-08 never used %q; the sequence is not exercising the real path", want)
		}
	}
	// Before the boundary nothing reads whole files: fs-read is a post-swap
	// primitive. P8-11 allows exactly one pre-boundary use — the bounded
	// window that proves each chunk landed at its offset (Limit > 0).
	for _, rr := range tr.reads {
		if rr.Limit <= 0 {
			t.Errorf("pre-boundary fs-read of a whole file: %+v", rr)
		}
	}

	// The only commands allowed to run are the three pinned script invocations.
	for _, cmd := range execCommands {
		switch cmd {
		case ExecDiscover, ExecPreflight, ExecLaunch:
		default:
			t.Errorf("exec ran an unpinned command %q", cmd)
		}
	}
	if len(execCommands) != 3 {
		t.Errorf("expected 3 exec calls (discover, preflight, launch), got %d: %v", len(execCommands), execCommands)
	}
}

// A double missing a primitive must not satisfy the contract. This is the
// compile-time face of R1: `partialOldClient` below is *not* a Transport.
type partialOldClient struct{}

func (partialOldClient) Exec(context.Context, ExecRequest) (ExecResult, error) {
	return ExecResult{}, nil
}
func (partialOldClient) FsWrite(context.Context, WriteRequest) (WriteResult, error) {
	return WriteResult{}, nil
}
func (partialOldClient) FsHash(context.Context, string) (HashResult, error) { return HashResult{}, nil }
func (partialOldClient) FsStat(context.Context, string) (StatResult, error) { return StatResult{}, nil }

func TestTransportRequiresEveryOldPrimitive(t *testing.T) {
	iface := reflect.TypeOf((*Transport)(nil)).Elem()
	partial := reflect.TypeOf(partialOldClient{})
	if partial.Implements(iface) {
		t.Fatal("a client without fs-read satisfies Transport; the contract was weakened")
	}
}

// ---------------------------------------------------------------------------
// R2 - rollback when the post-rename check fails
// ---------------------------------------------------------------------------

// TestRollbackAfterPostRenameMismatch proves the SW-15 refusal is a rollback:
// the successor is never started, the new image is quarantined rather than
// deleted, the restore is verified against the journaled OLD_SHA256, and the
// journal reads as rolled back.
func TestRollbackAfterPostRenameMismatch(t *testing.T) {
	script := string(BootScript())

	// (a) The trigger: a post-rename digest mismatch must return the rollback,
	// not fall through to the spawn and not start anything itself.
	sw15 := scriptRegion(t, script, "# SW-15:", "# SW-16:")
	requireAll(t, sw15, "SW-15 block", []string{
		"$placedHash = Get-Sha256Hex -Path $canonical",
		"if ($placedHash -ne [string]$m.sha256_payload) {",
		"'HASHMISMATCH_POSTRENAME'",
		"return (Invoke-Rollback -Reason 'post-rename-hash'",
		"-NewPid 0",
	})
	if strings.Contains(sw15, "Start-ClientProcess") {
		t.Error("SW-15 starts a process; a mismatch must roll back instead")
	}

	// (b) The rollback body, in the order the invariants require.
	rb := psFunction(t, script, "Invoke-Rollback")
	requireOrder(t, rb, "Invoke-Rollback", []string{
		// R-E1: the successor is stopped before any file moves.
		"$late = -not (Test-ProcessAlive -ProcId $OldPid)",
		"'ROLLBACK'",
		"if ($NewPid -gt 0 -and (Test-ProcessAlive -ProcId $NewPid)) {",
		"Stop-ProcessById -ProcId $NewPid",
		"Wait-ProcessGone -ProcId $NewPid -BudgetMs 1000",
		// R-E3: quarantine the bad image, never delete it.
		"$quarantine = $Canonical + '.next.dead.'",
		"Invoke-RenameWithRetry -From $Canonical -To $quarantine",
		"'NEW_QUARANTINED'",
		// R-E4: restore the last-known-good.
		"Invoke-RenameWithRetry -From $Backup -To $Canonical",
	})
	// R-E5: the restored bytes must be the journaled OLD_SHA256.
	requireOrder(t, rb, "Invoke-Rollback restore", []string{
		"$restoredHash = Get-Sha256Hex -Path $Canonical",
		"if ($restoredHash -eq '' -or $restoredHash -ne $OldSha) {",
		"'ROLLBACK_HASH_BAD'",
	})
	// R-E5 must not be made vacuous. $OldSha is a parameter, so the body may
	// never assign it: inserting `$OldSha = $restoredHash` before the gate keeps
	// every anchor above intact while turning the comparison into a tautology.
	if assign := regexp.MustCompile(`\$OldSha\s*=`).FindString(rb); assign != "" {
		t.Fatalf("Invoke-Rollback assigns %q; $OldSha is a parameter and the restore check must compare against it", assign)
	}
	operand := regexp.MustCompile(`\$restoredHash\s*-ne\s*(\$[A-Za-z_][A-Za-z0-9_]*)`).FindStringSubmatch(rb)
	if operand == nil {
		t.Fatalf("the restore check never compares $restoredHash against a variable")
	}
	if operand[1] != "$OldSha" {
		t.Fatalf("the restore check compares against %s; expected $OldSha", operand[1])
	}

	// The failed verification must end the rollback, not continue to ROLLED_BACK.
	hashBlock := scriptRegion(t, rb, "# R-E5 / R-L4:", "if ($late) {")
	requireAll(t, hashBlock, "rollback hash block", []string{
		"'ROLLBACK_HASH_BAD'",
		"return $ExitRollbackFail",
	})
	// The early path ends in the rollback result. (The same literal closes the
	// late path above, so this is asserted inside the tail region.)
	tail := scriptRegion(t, rb, "'ROLLED_BACK_EARLY'", "")
	requireAll(t, tail, "rollback early outcome", []string{
		"return $ExitRollback\n",
	})

	// Nothing here may delete: the old or new image is only ever renamed.
	if strings.Contains(rb, "Remove-Item") {
		t.Error("rollback deletes a file; images are quarantined, never removed")
	}

	// "Nothing starts" on the early path: the only Start-ClientProcess in the
	// rollback is the late-path restart, guarded by if ($late) and reached only
	// after the restored image has been verified.
	if n := strings.Count(rb, "Start-ClientProcess"); n != 1 {
		t.Fatalf("rollback has %d Start-ClientProcess calls; expected only the late restart", n)
	}
	ifLate := strings.Index(rb, "if ($late) {")
	start := strings.Index(rb, "Start-ClientProcess")
	hashGate := strings.Index(rb, "ROLLBACK_HASH_BAD")
	if ifLate < 0 || start < 0 || hashGate < 0 || start < ifLate || start < hashGate {
		t.Errorf("rollback restart is not guarded by if ($late) after the hash gate (ifLate=%d start=%d hash=%d)",
			ifLate, start, hashGate)
	}

	// (c) The journal the rollback writes must read as rolled back, and the
	// reason must be the post-rename check.
	journal := strings.Join([]string{
		"ts=1700000000 COMMIT_START pid=41 mode=swap",
		"ts=1700000001 PLACED",
		"ts=1700000002 HASHMISMATCH_POSTRENAME expected=aaaa actual=bbbb",
		"ts=1700000003 ROLLBACK reason=post-rename-hash late=false",
		"ts=1700000004 NEW_QUARANTINED to=C:\\Probe\\probe-client.exe.next.dead.1700000004",
		"ts=1700000005 ROLLED_BACK_EARLY old_pid=41 old_alive=1",
	}, "\n")
	status := Analyze(ParseJournal([]byte(journal)))
	if status.Outcome != OutcomeRolledBack {
		t.Errorf("journal outcome is %q, want %q", status.Outcome, OutcomeRolledBack)
	}
	if status.Phase == "NEW_STARTED" {
		t.Error("journal reports a new process started during rollback")
	}
	var reason string
	for _, ev := range ParseJournal([]byte(journal)) {
		if ev.Name == EventRollback {
			reason = ev.Field("reason")
		}
	}
	if reason != "post-rename-hash" {
		t.Errorf("ROLLBACK reason is %q, want post-rename-hash", reason)
	}

	// A failed restore verification must not read as a successful rollback.
	bad := strings.Replace(journal, "ROLLED_BACK_EARLY old_pid=41 old_alive=1",
		"ROLLBACK_HASH_BAD expected=aaaa actual=cccc", 1)
	if got := Analyze(ParseJournal([]byte(bad))).Outcome; got != OutcomeRollbackFailed {
		t.Errorf("failed restore verification reads as %q, want %q", got, OutcomeRollbackFailed)
	}
}

// ---------------------------------------------------------------------------
// R3 - successor argv
// ---------------------------------------------------------------------------

func TestSuccessorArgvIsConfigOrBare(t *testing.T) {
	const exe = `C:\Probe\probe-client.exe`
	cases := []struct {
		name    string
		cmdline string
		want    []string
	}{
		{"embedded config build", exe, nil},
		{"config flag", exe + ` --config C:\Probe\p.json`, []string{"--config", `C:\Probe\p.json`}},
		{"broken connect prefix", `connect --canary --canary-old-pid 77 --config C:\Probe\p.json --server-host 10.0.0.5`, []string{"--config", `C:\Probe\p.json`}},
		{"connect only", exe + ` connect`, nil},
		{"canary only", exe + ` --canary`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, cfg, err := NewClientArgs(tc.cmdline)
			if err != nil {
				t.Fatalf("NewClientArgs(%q): %v", tc.cmdline, err)
			}
			if !reflect.DeepEqual(args, tc.want) {
				t.Fatalf("args = %q, want %q", args, tc.want)
			}
			if err := ValidateClientArgs(args); err != nil {
				t.Fatalf("derived argv %q is rejected: %v", args, err)
			}
			for _, a := range args {
				lower := strings.ToLower(a)
				for _, bad := range forbiddenClientTokens {
					if lower == bad || strings.HasPrefix(lower, bad+"=") {
						t.Fatalf("derived argv carries the forbidden token %q: %q", bad, args)
					}
				}
			}
			if tc.want == nil && cfg != "" {
				t.Fatalf("zero-arg successor reported config path %q", cfg)
			}
		})
	}

	// ValidateClientArgs must still refuse what the old client mis-parses; if
	// this guard is removed the derivation above stops being a contract.
	if err := ValidateClientArgs([]string{"connect", "--config", `C:\Probe\p.json`}); err == nil {
		t.Fatal("ValidateClientArgs accepted a `connect` prefix")
	}
	if err := ValidateClientArgs([]string{"--canary"}); err == nil {
		t.Fatal("ValidateClientArgs accepted --canary")
	}

	// The spawn site itself: one --config branch, one bare branch, nothing else.
	script := string(BootScript())
	spawn := psFunction(t, script, "Start-ClientProcess")
	requireOrder(t, spawn, "Start-ClientProcess", []string{
		"if ($ConfigPath -ne '') {",
		`$argumentLine = '--config "' + $ConfigPath + '"'`,
		"-ArgumentList $argumentLine",
		"} else {",
		"Start-Process -FilePath $Path -WorkingDirectory $WorkDir",
	})
	if n := strings.Count(spawn, "-ArgumentList"); n != 1 {
		t.Errorf("Start-ClientProcess has %d -ArgumentList sites; expected exactly one", n)
	}
	for _, bad := range append([]string{"connect"}, forbiddenClientTokens...) {
		if strings.Contains(strings.ToLower(spawn), bad) {
			t.Errorf("Start-ClientProcess mentions %q; the successor argv must not reproduce K9", bad)
		}
	}
}

// ---------------------------------------------------------------------------
// R4 - the 183 / OLD_RACE ladder
// ---------------------------------------------------------------------------

func TestRenameOldRaceLadderIsBounded(t *testing.T) {
	script := string(BootScript())

	// (a) 183 is returned immediately by the retry primitive: the delay ladder
	// belongs to sharing violations (5/32/33), not to an intruder at .old.
	retry := psFunction(t, script, "Invoke-RenameWithRetry")
	requireOrder(t, retry, "Invoke-RenameWithRetry", []string{
		"$delays = @(0, 250, 500, 1000, 2000)",
		"Invoke-RenameOnce -From $From -To $To",
		"'RENAME_RETRY'",
		"if ($code -eq 2 -or $code -eq 3) { return $code }",
		"if ($code -eq 183) { return $code }",
		"if ($code -ne 5 -and $code -ne 32 -and $code -ne 33) { return $code }",
	})
	if n := strings.Count(retry, "Invoke-RenameOnce"); n != 1 {
		t.Errorf("retry primitive calls Invoke-RenameOnce %d times; one call per attempt", n)
	}

	// (b) The park loop: journal, re-quarantine the intruder, retry - and stop.
	park := psFunction(t, script, "Invoke-ParkCanonical")
	requireOrder(t, park, "Invoke-ParkCanonical", []string{
		"while ($true) {",
		"$code = Invoke-RenameWithRetry -From $Canonical -To $Backup",
		"if ($code -ne 183) { return $code }",
		"$races = $races + 1",
		"'OLD_RACE'",
		"'OLD_RACE_REQUARANTINED'",
	})
	// The bound must be checked before the next quarantine attempt, and it must
	// be a finite literal: an unbounded loop spins on a genuine race.
	boundRe := regexp.MustCompile(`if \(\$races -gt (\d+)\) \{ return \$code \}`)
	m := boundRe.FindStringSubmatch(park)
	if m == nil {
		t.Fatalf("Invoke-ParkCanonical has no numeric retry bound; a 183 loop would spin forever")
	}
	bound, err := strconv.Atoi(m[1])
	if err != nil || bound < 1 || bound > 5 {
		t.Fatalf("retry bound is %q; expected 1..5", m[1])
	}
	// The bound must be reachable: $races is assigned exactly once inside the
	// loop, as $races = $races + 1. A reset (`$races = 0`) before the comparison
	// leaves the literal and the regex intact while never tripping the bound.
	loop := braceBlock(t, park, "while ($true) {")
	assigns := regexp.MustCompile(`\$races\s*=\s*([^\r\n;]*)`).FindAllStringSubmatch(loop, -1)
	if len(assigns) != 1 {
		t.Fatalf("the park loop assigns $races %d times; exactly one `$races = $races + 1` keeps the bound reachable", len(assigns))
	}
	if rhs := strings.TrimSpace(assigns[0][1]); rhs != "$races + 1" {
		t.Fatalf("the park loop assigns `$races = %s`; a reset makes the bound unreachable", rhs)
	}
	idxBound := strings.Index(park, m[0])
	idxQuarantine := strings.Index(park, "New-QuarantinePath")
	idxRequeue := strings.Index(park, "Invoke-RenameWithRetry -From $Backup -To $quarantine")
	if idxQuarantine < 0 || idxRequeue < 0 || idxBound > idxQuarantine {
		t.Errorf("bound is checked after the re-quarantine (bound=%d quarantine=%d)", idxBound, idxQuarantine)
	}
	// Every exit from the loop returns: non-183, bound, quarantine failure.
	if n := strings.Count(park, "return "); n < 3 {
		t.Errorf("park loop has %d returns; the 183 path may not terminate", n)
	}
	if !strings.Contains(park, "if ($q -ne 0) {") {
		t.Error("park loop ignores a failed re-quarantine")
	}

	// (c) Both REN1 sites go through the bounded loop, so a 183 anywhere ends
	// in RENAME1_FAIL rather than an unbounded retry.
	if n := strings.Count(script, "Invoke-ParkCanonical -Canonical $Canonical -Backup $Backup"); n < 2 {
		t.Errorf("both REN1 sites must route through Invoke-ParkCanonical, found %d", n)
	}
	// A park failure must not fall through to REN2.
	swap := psFunction(t, script, "Invoke-SwapPair")
	requireOrder(t, swap, "Invoke-SwapPair", []string{
		"$code = Invoke-ParkCanonical -Canonical $Canonical -Backup $Backup",
		"if ($code -eq 0) {",
		"'RENAME1_FAIL'",
	})
}
