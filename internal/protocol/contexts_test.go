package protocol

// P13-02 acceptance: the §6 addressable_reason enum is closed and every value
// has a producer; the resolution path reads the row's verdict instead of
// re-deriving it (one predicate, not two).

import (
	"testing"
)

// TestP1302AddressableReasonEnumClosed pins the §6 enum by value. Adding a
// reason without a producer, or renaming one, breaks a caller that switches on
// it — so the slice is asserted verbatim.
func TestP1302AddressableReasonEnumClosed(t *testing.T) {
	want := []string{
		"agent_own", "service_principal", "same_uid_other_process",
		"other_user", "no_user", "not_interactive", "privilege_not_held",
		"kind_has_no_surface",
	}
	if len(AddressableReasons) != len(want) {
		t.Fatalf("enum has %d values, want %d: %v", len(AddressableReasons), len(want), AddressableReasons)
	}
	for i, w := range want {
		if AddressableReasons[i] != w {
			t.Errorf("enum[%d] = %q, want %q", i, AddressableReasons[i], w)
		}
	}
	// No duplicates: a duplicate would make two causes indistinguishable.
	seen := map[string]bool{}
	for _, r := range AddressableReasons {
		if seen[r] {
			t.Errorf("duplicate reason %q", r)
		}
		seen[r] = true
	}
}

// TestP1302EveryReasonHasAProducer walks facts that must yield each enum value.
// A reason with no producer is dead vocabulary; a fact with no reason is an
// unclassified row.
func TestP1302EveryReasonHasAProducer(t *testing.T) {
	base := ContextFacts{
		Kind: ContextKindX11, AgentOwn: false, HasUser: true,
		UID: 1000, AgentUID: 1000, PID: 7, AgentPID: 99,
		Interactive: true, HasSurface: true,
	}
	cases := []struct {
		name   string
		mutate func(*ContextFacts)
		want   string
	}{
		{"own seat", func(f *ContextFacts) { f.AgentOwn = true; f.PID = f.AgentPID }, AddrAgentOwn},
		{"service principal", func(f *ContextFacts) { f.ServicePrincipal = true }, AddrServicePrincipal},
		{"same uid, other process", func(f *ContextFacts) {}, AddrSameUIDOtherProcess},
		{"other user", func(f *ContextFacts) { f.UID = 1001; f.Privileged = true }, AddrOtherUser},
		{"no user", func(f *ContextFacts) { f.HasUser = false }, AddrNoUser},
		{"not interactive", func(f *ContextFacts) { f.Interactive = false }, AddrNotInteractive},
		{"privilege not held", func(f *ContextFacts) { f.AccessDenied = true }, AddrPrivilegeNotHeld},
		{"kind has no surface", func(f *ContextFacts) { f.HasSurface = false }, AddrKindHasNoSurface},
	}
	produced := map[string]bool{}
	for _, c := range cases {
		f := base
		c.mutate(&f)
		_, reason := ClassifyContext(f)
		if reason != c.want {
			t.Errorf("%s: reason = %q, want %q", c.name, reason, c.want)
		}
		produced[reason] = true
	}
	for _, r := range AddressableReasons {
		if !produced[r] {
			t.Errorf("reason %q has no producer in this test's facts", r)
		}
	}
}

// TestP1302ResolveReadsTheRowsVerdict is the one-predicate proof at the unit
// level: a row classified unaddressable is refused with that same reason, and
// the §5.4 code is derived from it — never recomputed from facts.
func TestP1302ResolveReadsTheRowsVerdict(t *testing.T) {
	facts := ContextFacts{
		Kind: ContextKindUID, AgentOwn: true, HasUser: true,
		UID: 1000, AgentUID: 1000, PID: 5, AgentPID: 5,
		Interactive: false, HasSurface: false,
	}
	ok, reason := ClassifyContext(facts)
	if ok {
		t.Fatalf("uid context classified addressable")
	}
	rows := []ContextRow{
		{ID: "uid:1000", Kind: ContextKindUID, Addressable: ok, AddressableReason: reason},
		{ID: "x11::0", Kind: ContextKindX11, Addressable: true, AddressableReason: AddrAgentOwn},
	}
	if u := ResolveContext(rows, "x11::0"); u != nil {
		t.Fatalf("addressable context refused: %+v", u)
	}
	u := ResolveContext(rows, "uid:1000")
	if u == nil {
		t.Fatal("unaddressable context was not refused")
	}
	if u.AddressableReason != reason {
		t.Errorf("refusal addressable_reason = %q, want the row's %q", u.AddressableReason, reason)
	}
	if u.Reason != "KIND_NOT_APPLICABLE" {
		t.Errorf("§5.4 code = %q, want KIND_NOT_APPLICABLE", u.Reason)
	}
	if miss := ResolveContext(rows, "x11::9"); miss == nil || miss.Reason != "NOT_FOUND" {
		t.Errorf("unknown context = %+v, want NOT_FOUND", miss)
	}
}

// TestP1302ContextUnavailableMappingIsTotal keeps the §5.4 mapping total: no
// input can produce an empty code.
func TestP1302ContextUnavailableMappingIsTotal(t *testing.T) {
	codes := map[string]string{
		AddrNoUser:           "NO_USER",
		AddrNotInteractive:   "NOT_INTERACTIVE",
		AddrOtherUser:        "OTHER_USER",
		AddrPrivilegeNotHeld: "PRIVILEGE_NOT_HELD",
		AddrServicePrincipal: "PRIVILEGE_NOT_HELD",
		AddrKindHasNoSurface: "KIND_NOT_APPLICABLE",
	}
	for reason, want := range codes {
		if got := ContextUnavailableReason(reason); got != want {
			t.Errorf("ContextUnavailableReason(%q) = %q, want %q", reason, got, want)
		}
	}
	for _, r := range AddressableReasons {
		if ContextUnavailableReason(r) == "" {
			t.Errorf("reason %q maps to an empty code", r)
		}
	}
}
