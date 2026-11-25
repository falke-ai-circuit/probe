//go:build linux

package agent

// P13-03 acceptance item 6, against a real X server. This host has Xvfb and no
// window manager, which is exactly the substrate §7.1 was written for: the
// answer must be a 501 refusal, and a test that would also pass on
// `200 {count:0}` would be worthless. So the assertions are the code, the
// reason, and `ran:false` — not the absence of windows.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// startBareXvfb runs an access-control-disabled Xvfb with no window manager
// and returns its display number. It skips when the binary is not installed:
// a missing X server is not evidence about the refusal.
func startBareXvfb(t *testing.T) int {
	t.Helper()
	if _, err := exec.LookPath("Xvfb"); err != nil {
		t.Skip("Xvfb not installed: the §7.1 negative is proved on the measured substrate only")
	}
	num := 90 + os.Getpid()%8
	sock := fmt.Sprintf("/tmp/.X11-unix/X%d", num)
	cmd := exec.Command("Xvfb", fmt.Sprintf(":%d", num), "-screen", "0", "320x240x24", "-ac")
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		t.Skipf("could not start Xvfb: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			if d, err := x11Dial(num); err == nil {
				d.Close()
				return num
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Skipf("Xvfb :%d did not come up", num)
	return 0
}

// TestP1303RealXvfbRefusesNoWindowManager is the load-bearing negative: on a
// live X server with no WM, ui-tree answers PRIMITIVE_UNSUPPORTED with
// reason NO_WINDOW_MANAGER and ran:false — never a 200 with an empty list.
func TestP1303RealXvfbRefusesNoWindowManager(t *testing.T) {
	num := startBareXvfb(t)

	// The probe itself must see the absence, not a probe failure: wmUnknown
	// would be NO_BACKEND, a different (and wrong) refusal.
	state, active, pixels := probeX11(num)
	if state != wmAbsent {
		t.Fatalf("probeX11(:%d) = %v, want wmAbsent (active=%v pixels=%q)", num, state, active, pixels)
	}
	if pixels != "320x240" {
		t.Errorf("probe pixels = %q, want the setup screen 320x240", pixels)
	}

	a := &Agent{}
	env := protocol.Envelope{
		ID:     "p1303-xvfb",
		Type:   protocol.TypeUITree,
		Params: json.RawMessage(fmt.Sprintf(`{"context":"x11::%d","visible_only":true}`, num)),
	}
	resp := a.handleUITree(env)

	if resp.Error == nil {
		t.Fatalf("ui-tree on a WM-less display answered a result: %s", resp.Result)
	}
	if resp.Error.Code != protocol.ErrPrimitiveUnsupported {
		t.Fatalf("code = %q, want %q", resp.Error.Code, protocol.ErrPrimitiveUnsupported)
	}
	d := resp.Error.Detail
	if d["reason"] != protocol.ReasonNoWindowManager {
		t.Fatalf("reason = %v, want %q", d["reason"], protocol.ReasonNoWindowManager)
	}
	if d["ran"] != false {
		t.Fatalf("ran = %v, want false", d["ran"])
	}
	if d["display"] != fmt.Sprintf(":%d", num) {
		t.Errorf("display = %v", d["display"])
	}
	if d["tree_source_attempted"] != protocol.TreeSourceEWMHClientList {
		t.Errorf("tree_source_attempted = %v", d["tree_source_attempted"])
	}
	if _, ok := d["remediation"].(string); !ok {
		t.Errorf("detail carries no remediation string: %v", d["remediation"])
	}
	ctxDetail, ok := d["context"].(protocol.ContextRef)
	if !ok || ctxDetail.Kind != protocol.ContextKindX11 || ctxDetail.ID != fmt.Sprintf("x11::%d", num) {
		t.Errorf("detail.context = %#v", d["context"])
	}

	// The negative proof: this body is not the empty-success body. A 200
	// {count:0} has no error at all, and that is what this test forbids.
	if len(resp.Result) != 0 {
		t.Errorf("a refusal carried a result payload: %s", resp.Result)
	}
}

// TestP1303NonX11ContextKeepsRefusing keeps the out-of-scope kinds honest: a
// wayland context is NO_BACKEND, not an empty tree.
func TestP1303NonX11ContextKeepsRefusing(t *testing.T) {
	a := &Agent{}
	for _, ctx := range []string{"wayland:wayland-0", "uid:1000"} {
		env := protocol.Envelope{
			ID:     "p1303-ctx",
			Type:   protocol.TypeUITree,
			Params: json.RawMessage(fmt.Sprintf(`{"context":%q}`, ctx)),
		}
		resp := a.handleUITree(env)
		if resp.Error == nil || resp.Error.Code != protocol.ErrPrimitiveUnsupported {
			t.Fatalf("context %q = %+v, want PRIMITIVE_UNSUPPORTED", ctx, resp.Error)
		}
		if resp.Error.Detail["reason"] != protocol.ReasonNoBackend {
			t.Errorf("context %q reason = %v, want NO_BACKEND", ctx, resp.Error.Detail["reason"])
		}
	}
}
