package upgrade

import (
	"strings"
	"testing"
	"time"
)

func TestFormatLineRoundTrip(t *testing.T) {
	now := time.Unix(1700000000, 0)
	line := FormatLine(now, EventOldKilled, "pid", "4242", "mode", "swap")
	events := ParseJournal([]byte(line + "\r\n"))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Name != EventOldKilled {
		t.Fatalf("name = %q", ev.Name)
	}
	if ev.Time != now.Unix() {
		t.Fatalf("ts = %d", ev.Time)
	}
	if ev.Field("pid") != "4242" || ev.Field("mode") != "swap" {
		t.Fatalf("fields = %v", ev.Fields)
	}
}

func TestFormatLineNeverSpansLines(t *testing.T) {
	line := FormatLine(time.Unix(1, 0), EventRollback, "reason", "no sustained\nsession")
	if strings.ContainsAny(line, "\r\n") {
		t.Fatalf("line contains a newline: %q", line)
	}
	if !strings.Contains(line, "reason=no_sustained_session") {
		t.Fatalf("detail not collapsed: %q", line)
	}
}

func TestParseJournalSkipsNoise(t *testing.T) {
	data := []byte("garbage\n\n" + `{"phase":"LAUNCH"}` + "\n" + FormatLine(time.Unix(2, 0), EventDwellOK, "new_pid", "7"))
	events := ParseJournal(data)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d: %v", len(events), events)
	}
	if events[0].Name != EventDwellOK {
		t.Fatalf("last event = %q", events[0].Name)
	}
}

func TestAnalyzeOutcomes(t *testing.T) {
	mk := func(names ...string) []Event {
		events := make([]Event, 0, len(names))
		for _, n := range names {
			events = append(events, Event{Name: n, Fields: map[string]string{}})
		}
		return events
	}

	if st := Analyze(mk(EventCommitStart, EventParked, EventPlaced, EventNewStarted, EventDone)); st.Outcome != OutcomeDone || st.Phase != "DONE" {
		t.Fatalf("done journal analysed as %+v", st)
	}
	if st := Analyze(mk(EventCommitStart, EventParked, EventRollback, EventRolledBackEarly)); st.Outcome != OutcomeRolledBack {
		t.Fatalf("early rollback analysed as %+v", st)
	}
	if st := Analyze(mk(EventCommitStart, EventNewNotEstablished, EventRollback, EventRolledBackLate)); st.Outcome != OutcomeRolledBack {
		t.Fatalf("late rollback analysed as %+v", st)
	}
	if st := Analyze(mk(EventCommitStart, EventRollback, EventRollbackHashBad)); st.Outcome != OutcomeRollbackFailed {
		t.Fatalf("bad rollback analysed as %+v", st)
	}
	if st := Analyze(mk(EventCommitStart, EventOldPIDAlive)); st.Outcome != OutcomeOldPIDAlive {
		t.Fatalf("old pid alive analysed as %+v", st)
	}
	if st := Analyze(mk(EventCommitStart, EventParked)); st.Outcome != OutcomeIncomplete || st.Phase != "PARKED" {
		t.Fatalf("incomplete journal analysed as %+v", st)
	}
}
