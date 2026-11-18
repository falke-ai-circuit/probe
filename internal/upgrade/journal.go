package upgrade

import (
	"strconv"
	"strings"
	"time"
)

// Journal event names. A line is "<EVENT> k=v k=v" with a leading "ts=<unix>",
// ASCII, one per line, append only, written before the action it describes.
const (
	EventPreflightRefused   = "PREFLIGHT_REFUSED"
	EventPreflight          = "PREFLIGHT"
	EventLaunch             = "LAUNCH"
	EventLaunchFail         = "LAUNCH_FAIL"
	EventCommitStart        = "COMMIT_START"
	EventBootSHA256         = "BOOT_SHA256"
	EventBootHashMismatch   = "BOOT_HASH_MISMATCH"
	EventOldQuarantined     = "OLD_QUARANTINED"
	EventOldLocked          = "OLD_LOCKED"
	EventOldSHA256          = "OLD_SHA256"
	EventOldHashFail        = "OLD_HASH_FAIL"
	EventSwapMode           = "SWAP_MODE"
	EventParked             = "PARKED"
	EventPlaced             = "PLACED"
	EventRenameRetry        = "RENAME_RETRY"
	EventRename1Fail        = "RENAME1_FAIL"
	EventRename2Fail        = "RENAME2_FAIL"
	EventCompensated        = "COMPENSATED"
	EventCompensateFail     = "COMPENSATE_FAIL"
	EventInvariantBroken    = "INVARIANT_BROKEN"
	EventHashMismatchPost   = "HASHMISMATCH_POSTRENAME"
	EventStartFail          = "START_FAIL"
	EventNewStarted         = "NEW_STARTED"
	EventNewEstablished     = "NEW_ESTABLISHED"
	EventNewNotEstablished  = "NEW_NOT_ESTABLISHED"
	EventOldKilled          = "OLD_KILLED"
	EventOldPIDAlive        = "OLDPID_ALIVE"
	EventHeartbeat          = "HEARTBEAT"
	EventDwellOK            = "DWELL_OK"
	EventNewDiedInDwell     = "NEW_DIED_IN_DWELL"
	EventDone               = "DONE"
	EventRollback           = "ROLLBACK"
	EventRollbackBlocked    = "ROLLBACK_BLOCKED"
	EventNewQuarantined     = "NEW_QUARANTINED"
	EventRollbackHashBad    = "ROLLBACK_HASH_BAD"
	EventRollbackStartFail  = "ROLLBACK_START_FAIL"
	EventRolledBackEarly    = "ROLLED_BACK_EARLY"
	EventRolledBackLate     = "ROLLED_BACK_LATE"
	EventRollbackUnverified = "ROLLBACK_UNVERIFIED"
	EventJournalFull        = "JOURNAL_FULL"
)

// Event is one parsed journal line.
type Event struct {
	Name   string
	Fields map[string]string
	Time   int64
	Raw    string
}

// Field returns a journal field or "".
func (e Event) Field(key string) string { return e.Fields[key] }

// FormatLine renders a journal line. Every value is whitespace-collapsed so a
// single event can never span two lines.
func FormatLine(now time.Time, name string, kv ...string) string {
	var b strings.Builder
	b.WriteString("ts=")
	b.WriteString(strconv.FormatInt(now.Unix(), 10))
	b.WriteString(" ")
	b.WriteString(name)
	for i := 0; i+1 < len(kv); i += 2 {
		v := kv[i+1]
		v = strings.Join(strings.Fields(v), "_")
		if v == "" {
			v = "empty"
		}
		b.WriteString(" ")
		b.WriteString(kv[i])
		b.WriteString("=")
		b.WriteString(v)
	}
	return b.String()
}

// isJournalEventName accepts the uppercase event vocabulary and rejects noise
// such as banner text or a stray JSON line.
func isJournalEventName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'A' && c <= 'Z' {
			continue
		}
		if c >= '0' && c <= '9' {
			continue
		}
		if c == '_' {
			continue
		}
		return false
	}
	return name[0] >= 'A' && name[0] <= 'Z'
}

// ParseJournal parses journal bytes. Unknown or malformed lines are dropped,
func ParseJournal(data []byte) []Event {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	events := make([]Event, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			continue
		}
		ev := Event{Fields: make(map[string]string, 6), Raw: line}
		tokens := strings.Fields(line)
		if len(tokens) == 0 {
			continue
		}
		start := 0
		if strings.HasPrefix(tokens[0], "ts=") {
			if ts, err := strconv.ParseInt(strings.TrimPrefix(tokens[0], "ts="), 10, 64); err == nil {
				ev.Time = ts
			}
			start = 1
		}
		if start >= len(tokens) {
			continue
		}
		name := tokens[start]
		if !isJournalEventName(name) {
			continue
		}
		ev.Name = name
		for _, tok := range tokens[start+1:] {
			if i := strings.Index(tok, "="); i > 0 {
				ev.Fields[tok[:i]] = tok[i+1:]
			}
		}
		events = append(events, ev)
	}
	return events
}

// Outcome is the journal's terminal reading.
type Outcome string

const (
	OutcomeIncomplete     Outcome = "incomplete"
	OutcomeDone           Outcome = "done"
	OutcomeRolledBack     Outcome = "rolled_back"
	OutcomeRollbackFailed Outcome = "rollback_failed"
	OutcomeFailed         Outcome = "failed"
	OutcomeOldPIDAlive    Outcome = "old_pid_alive"
)

// Status summarises a journal.
type Status struct {
	Outcome Outcome
	Phase   string // last phase marker (COMMIT_START, PARKED, PLACED, WAITING, KILLED...)
	Last    Event
	Events  int
}

// Analyze reduces a journal to the outcome the caller reports at SV-02.
func Analyze(events []Event) Status {
	st := Status{Outcome: OutcomeIncomplete, Events: len(events)}
	for _, ev := range events {
		if ev.Name == "" {
			continue
		}
		st.Last = ev
		switch ev.Name {
		case EventCommitStart:
			st.Phase = "COMMIT_START"
		case EventParked:
			st.Phase = "PARKED"
		case EventPlaced:
			st.Phase = "PLACED"
		case EventNewStarted:
			st.Phase = "NEW_STARTED"
		case EventNewEstablished:
			st.Phase = "NEW_ESTABLISHED"
		case EventOldKilled:
			st.Phase = "OLD_KILLED"
		case EventDone:
			st.Phase = "DONE"
			st.Outcome = OutcomeDone
		case EventRolledBackEarly, EventRolledBackLate:
			st.Phase = "ROLLED_BACK"
			st.Outcome = OutcomeRolledBack
		case EventRollbackUnverified, EventRollbackBlocked, EventRollbackHashBad,
			EventRollbackStartFail:
			st.Phase = "ROLLBACK_FAILED"
			st.Outcome = OutcomeRollbackFailed
		case EventOldPIDAlive:
			st.Outcome = OutcomeOldPIDAlive
		}
	}
	return st
}
