// Package version tests: the canonical/wire/display conversions must agree, and
// normalization must be the only place a "v" prefix is added or removed.
package version

import "testing"

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"1.18.6":   "1.18.6",
		"v1.18.6":  "1.18.6",
		" 1.18.6 ": "1.18.6",
		"v1.18.7":  "1.18.7",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWireAndDisplayAgree(t *testing.T) {
	if got := Normalize(Display()); got != Wire() {
		t.Fatalf("Normalize(Display()) = %q, want Wire() = %q", got, Wire())
	}
	if Wire() == "" {
		t.Fatal("Wire() is empty")
	}
}
