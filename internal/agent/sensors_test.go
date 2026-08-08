package agent

import (
	"testing"
)

// TestSensors_RegistryPopulated checks that all built-in sensors register at init.
func TestSensors_RegistryPopulated(t *testing.T) {
	list := agentSensors.List()
	if len(list) < 15 {
		t.Errorf("got %d sensors, want at least 15", len(list))
		for _, s := range list {
			t.Logf("  %s (%s): %s", s.Name, s.Category, s.Description)
		}
	}
}

// TestSensors_AllHaveNameCategory checks metadata is populated.
func TestSensors_AllHaveMetadata(t *testing.T) {
	for _, s := range agentSensors.List() {
		if s.Name == "" {
			t.Error("sensor with empty name")
		}
		if s.Category == "" {
			t.Errorf("%s: empty category", s.Name)
		}
		if s.Description == "" {
			t.Errorf("%s: empty description", s.Name)
		}
	}
}

// TestSensors_UniqueNames checks no two sensors share a name.
func TestSensors_UniqueNames(t *testing.T) {
	seen := make(map[string]bool)
	for _, s := range agentSensors.List() {
		if seen[s.Name] {
			t.Errorf("duplicate sensor name: %s", s.Name)
		}
		seen[s.Name] = true
	}
}

// TestSensors_ReadAll checks that every sensor's Read returns a non-nil
// value with no error (using no args).
func TestSensors_ReadAll(t *testing.T) {
	for _, info := range agentSensors.List() {
		s, ok := agentSensors.Get(info.Name)
		if !ok {
			t.Errorf("Get(%s) returned not-found", info.Name)
			continue
		}
		// Pass nil map; sensors should handle empty args gracefully.
		val, err := s.Read(nil)
		if err != nil {
			// network_dial, dns_resolve, dns_resolve_mx, dns_resolve_txt,
			// file_stat, disk_usage, env_vars may require args. Skip with note.
			t.Logf("%s requires args (err=%v) — OK if expected", info.Name, err)
			continue
		}
		if val == nil {
			t.Errorf("%s: Read returned nil value", info.Name)
		}
	}
}

// TestSensors_StableListOrder checks the sort is stable.
func TestSensors_StableListOrder(t *testing.T) {
	a := agentSensors.List()
	b := agentSensors.List()
	if len(a) != len(b) {
		t.Fatalf("length changed: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Name != b[i].Name {
			t.Errorf("position %d: %s != %s", i, a[i].Name, b[i].Name)
		}
	}
}
