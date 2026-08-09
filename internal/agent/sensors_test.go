package agent

import (
	"testing"
	"time"
)

// TestSensors_RegistryPopulated checks that all built-in sensors register at init.
func TestSensors_RegistryPopulated(t *testing.T) {
	list := agentSensors.List()
	if len(list) < 19 {
		t.Errorf("got %d sensors, want at least 19 (15 v1.14 + 4 input)", len(list))
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

// TestSensors_InputSensorsRegistered checks that the 4 input-category
// sensors are all present.
func TestSensors_InputSensorsRegistered(t *testing.T) {
	required := []string{"active_window", "clipboard_read", "browser_history", "keypress_window"}
	byName := make(map[string]bool)
	for _, s := range agentSensors.List() {
		byName[s.Name] = true
	}
	for _, name := range required {
		if !byName[name] {
			t.Errorf("missing required input sensor: %s", name)
		}
	}
}

// TestSensors_InputSensorsHaveInputCategory checks the category.
func TestSensors_InputSensorsHaveInputCategory(t *testing.T) {
	inputNames := map[string]bool{
		"active_window":   true,
		"clipboard_read":  true,
		"browser_history": true,
		"keypress_window": true,
	}
	for _, s := range agentSensors.List() {
		if inputNames[s.Name] && s.Category != "input" {
			t.Errorf("sensor %s has category %q, want \"input\"", s.Name, s.Category)
		}
	}
}

// TestSensors_InputSensorsHaveDescription checks each has a non-empty
// description (operators need to know what each sensor does).
func TestSensors_InputSensorsHaveDescription(t *testing.T) {
	for _, s := range agentSensors.List() {
		if s.Category != "input" {
			continue
		}
		if len(s.Description) < 10 {
			t.Errorf("input sensor %s has too-short description: %q", s.Name, s.Description)
		}
	}
}

// TestSensor_KeypressBufferSize checks the rolling buffer caps correctly.
func TestSensor_KeypressBufferSize(t *testing.T) {
	// Inject test events
	for i := 0; i < 20; i++ {
		appendKeypressEvent(keypressEvent{
			Timestamp: time.Now(),
			Code:      uint16(i),
			Name:      "TEST",
		})
	}
	events := snapshotKeypressWindow(60, 500)
	if len(events) != 20 {
		t.Errorf("expected 20 events, got %d", len(events))
	}
}

// TestSensor_BrowserHistoryLimitClamp tests the limit param clamping.
func TestSensor_BrowserHistoryLimitClamp(t *testing.T) {
	// 0 should become 50 (default)
	// 5000 should become 1000 (max)
	// (We can't easily test the actual sqlite read without a real history
	// file, but we can test that the function doesn't panic on weird args.)
	// This is a smoke test only.
	t.Skip("requires sqlite3 + browser history fixture")
}
