//go:build linux

package agent

// P13-02 (contract P10-07 v3.1 §6, linux backend). This is the only platform
// this build enumerates: x11 displays from the X sockets in /tmp/.X11-unix,
// a wayland seat when $WAYLAND_DISPLAY names one, and the agent's own uid
// context. Windows is `[S]` — unplanned — and must stay absent rather than be
// emulated with an empty `wts` row.

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// enumerateContexts reads the host and returns its §6 rows. It answers an
// error only when there is no backend at all (non-linux); a linux host with no
// display enumerates its uid context and says so, which is a fact, not a
// failure.
func enumerateContexts() (protocol.ContextsResult, error) {
	return buildContexts(probeHostLinux()), nil
}

// probeHostLinux collects observations. Nothing here classifies: it fills the
// facts, and protocol.ClassifyContext decides.
func probeHostLinux() hostProbe {
	hp := hostProbe{
		EUID:           os.Geteuid(),
		AgentPID:       os.Getpid(),
		Display:        os.Getenv("DISPLAY"),
		WaylandDisplay: os.Getenv("WAYLAND_DISPLAY"),
		Privileged:     os.Geteuid() == 0,
		Tools:          map[string]bool{},
	}
	if u, err := user.Current(); err == nil {
		hp.Username = u.Username
	}
	hp.LookupUser = func(uid int) string {
		if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
			return u.Username
		}
		return ""
	}
	for _, tool := range []string{"xdotool", "import", "scrot", "xprop", "xdpyinfo"} {
		if _, err := exec.LookPath(tool); err == nil {
			hp.Tools[tool] = true
		}
	}
	hp.XSockets = scanX11Sockets()
	hp.XInfo = func(num int) (wmState, *bool, string) { return probeX11(num) }
	return hp
}

// scanX11Sockets lists the display sockets an operator could mean by
// "x11:<n>". The owner uid comes from the socket inode: that is the X server's
// user, which is what a selector would have to cross.
func scanX11Sockets() []x11SocketInfo {
	entries, err := os.ReadDir("/tmp/.X11-unix")
	if err != nil {
		return nil
	}
	out := make([]x11SocketInfo, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "X") {
			continue
		}
		num, err := strconv.Atoi(strings.TrimPrefix(name, "X"))
		if err != nil || num < 0 {
			continue
		}
		info := x11SocketInfo{Num: num}
		if fi, err := os.Stat(filepath.Join("/tmp/.X11-unix", name)); err == nil {
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				info.OwnerUID = int(st.Uid)
				info.OwnerKnown = true
			}
		}
		out = append(out, info)
	}
	return out
}

// probeX11 asks the display two EWMH questions in one call: is a window manager
// running (does it publish _NET_SUPPORTING_WM_CHECK), and does it name an
// active window. Both answers are observations; "we could not ask" is
// wmUnknown, never "no window manager".
func probeX11(num int) (wmState, *bool, string) {
	display := ":" + strconv.Itoa(num)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "xprop", "-display", display, "-root",
		"_NET_SUPPORTING_WM_CHECK", "_NET_ACTIVE_WINDOW").Output()
	if err != nil {
		return wmUnknown, nil, probePixels(display)
	}
	text := string(out)
	state := wmAbsent
	if strings.Contains(text, "_NET_SUPPORTING_WM_CHECK(WINDOW)") &&
		!strings.Contains(text, "_NET_SUPPORTING_WM_CHECK(WINDOW):  no such") &&
		!strings.Contains(text, "_NET_SUPPORTING_WM_CHECK:  not found") {
		state = wmPresent
	}
	var active *bool
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, "_NET_ACTIVE_WINDOW(WINDOW)") {
			continue
		}
		if i := strings.Index(line, "window id # "); i >= 0 {
			id := strings.TrimSpace(line[i+len("window id # "):])
			v := id != "" && id != "0x0"
			active = &v
		}
	}
	return state, active, probePixels(display)
}

// probePixels reads the logical dimensions from xdpyinfo. A missing binary or
// an unparsable line leaves pixels "" — an unknown surface, not a zero one.
func probePixels(display string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "xdpyinfo", "-display", display).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "dimensions:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			return fields[1]
		}
	}
	return ""
}
