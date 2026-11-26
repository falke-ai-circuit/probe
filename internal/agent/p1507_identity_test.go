package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falke-ai-circuit/probe/internal/lifecycle"
)

// P15-07: promotion must restore the name the INSTALLATION was configured with.
//
// The field sequence (P15-02, Linux canary) is two cycles of one installation:
//
//	cycle 1: the running artifact was built with the install's own name
//	         ("p15-02-linux"), so the canary connects as "p15-02-linux-canary"
//	         and trimming one suffix at promotion lands back on the install.
//	cycle 2: the running artifact is the one delivered by cycle 1, and its
//	         EMBEDDED config names it after the BUILD ("P15-02-linux-canary").
//	         Appending the canary suffix therefore yields
//	         "P15-02-linux-canary-canary", and trimming one suffix at promotion
//	         yields "P15-02-linux-canary" — not the configured identity. The
//	         canonical registry row keeps the previous version and the new
//	         version lands on a throwaway row.
//
// A single cycle is green even with the defect, which is why this test runs
// two: the second is where the identity stops returning to configuration.
func TestP1507PromotionRestoresConfiguredNameAcrossTwoCycles(t *testing.T) {
	dir := t.TempDir()
	configured := "p15-02-linux"

	// The server's registry: rows keyed by exactly the name the agent reported
	// in agent_info. The install's row starts on the old version.
	registry := map[string]string{configured: "v1.19.0"}

	// runningIdentity is the name carried by the artifact that handles the
	// update — its embedded build config, before any canary suffix.
	cycles := []struct {
		runningIdentity string
		newVersion      string
	}{
		{runningIdentity: configured, newVersion: "v1.19.1"},            // delivered by the build named after the install
		{runningIdentity: "P15-02-linux-canary", newVersion: "v1.19.2"}, // the artifact cycle 1 delivered carries the BUILD name
	}

	for i, c := range cycles {
		cycle := i + 1

		// The installation's config file: the durable identity, written by the
		// operator and read on every start.
		cfgPath := filepath.Join(dir, "p15-02-linux.json")
		if err := os.WriteFile(cfgPath, []byte(`{"server":"ws://example.invalid/ws","name":"`+configured+`"}`), 0o644); err != nil {
			t.Fatalf("cycle %d: write install config: %v", cycle, err)
		}

		// Exactly what cmd/probe/connect.go builds for a canary: the artifact's
		// own name plus the canary suffix.
		a := New(Config{
			Name:       c.runningIdentity + canaryNameSuffix,
			CanaryMode: true,
			Mode:       "connect",
			ConfigPath: cfgPath,
			StatusPath: filepath.Join(dir, "status.json"),
			Version:    c.newVersion,
		})
		retire := a.startStatusFile()
		defer retire()

		wantCanary := configured + canaryNameSuffix
		if a.cfg.Name != wantCanary {
			t.Errorf("cycle %d: canary connects as %q, want %q — the canary base name must come from configuration, not from the artifact it runs",
				cycle, a.cfg.Name, wantCanary)
		}

		canonical := filepath.Join(dir, "probe-agent")
		a.promoteToCanonical(canonical)

		if a.cfg.Name != configured {
			t.Errorf("cycle %d: promoted identity = %q, want the configured name %q — promotion restored the artifact's name, not configuration",
				cycle, a.cfg.Name, configured)
		}

		st, err := lifecycle.Read(filepath.Join(dir, "status.json"))
		if err != nil {
			t.Fatalf("cycle %d: read status after promotion: %v", cycle, err)
		}
		if st.Name != configured {
			t.Errorf("cycle %d: run-state record name = %q, want %q (P12-04 F2 keeps the record in step with the registry)", cycle, st.Name, configured)
		}
		if st.Executable != canonical {
			t.Errorf("cycle %d: run-state record executable = %q, want the canonical path %q (P12-04 F2)", cycle, st.Executable, canonical)
		}

		// The reconnect loop registers under a.cfg.Name: that is the row key.
		registry[a.cfg.Name] = c.newVersion

		if got := registry[configured]; got != c.newVersion {
			t.Errorf("cycle %d: canonical row %q carries %q, want the new version %q — the new version landed on a throwaway row (%q)",
				cycle, configured, got, c.newVersion, a.cfg.Name)
		}
		if t.Failed() {
			t.FailNow()
		}
	}
}
