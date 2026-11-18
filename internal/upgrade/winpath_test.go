package upgrade

import "testing"

func TestCleanWinPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{`C:\Program Files\Probe\probe-client.exe`, `C:\Program Files\Probe\probe-client.exe`},
		{`C:/Program Files/Probe/../Probe/probe-client.exe`, `C:\Program Files\Probe\probe-client.exe`},
		{`C:\\Probe\\\\x.exe`, `C:\Probe\x.exe`},
		{`C:\`, `C:\`},
		{`\\server\share\a.exe`, `\\server\share\a.exe`},
		{`  C:\Probe\a.exe  `, `C:\Probe\a.exe`},
	}
	for _, c := range cases {
		if got := CleanWinPath(c.in); got != c.want {
			t.Errorf("CleanWinPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsAbsWinPath(t *testing.T) {
	if !IsAbsWinPath(`C:\Probe\a.exe`) {
		t.Fatal("drive path should be absolute")
	}
	if !IsAbsWinPath(`\\srv\share\a.exe`) {
		t.Fatal("unc path should be absolute")
	}
	for _, p := range []string{`probe-client.exe`, `.\probe-client.exe`, `C:probe.exe`, ``} {
		if IsAbsWinPath(p) {
			t.Errorf("%q should not be absolute", p)
		}
	}
}

func TestWithinWinPath(t *testing.T) {
	if !WithinWinPath(`C:\Probe`, `C:\Probe\sub\a.exe`) {
		t.Fatal("child should be within")
	}
	if !WithinWinPath(`C:\Probe`, `C:\Probe`) {
		t.Fatal("equal paths are within")
	}
	// The boundary matters: a sibling whose name starts with the sandbox name
	// must not pass.
	if WithinWinPath(`C:\Probe`, `C:\ProbeOther\a.exe`) {
		t.Fatal("sibling prefix must not be within")
	}
	if !WithinWinPath(`c:\probe`, `C:\PROBE\a.exe`) {
		t.Fatal("comparison is case-insensitive")
	}
}

func TestDirAndBase(t *testing.T) {
	if got := DirWinPath(`C:\Probe\probe-client.exe`); got != `C:\Probe` {
		t.Fatalf("dir = %q", got)
	}
	if got := DirWinPath(`C:\probe-client.exe`); got != `C:\` {
		t.Fatalf("root dir = %q", got)
	}
	if got := BaseWinPath(`C:\Probe\probe-client.exe`); got != `probe-client.exe` {
		t.Fatalf("base = %q", got)
	}
	if got := JoinWinPath(`C:\Probe`, `a.next`); got != `C:\Probe\a.next` {
		t.Fatalf("join = %q", got)
	}
}
