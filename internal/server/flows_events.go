package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// NDEventStore is the default FlowEventStore implementation. It persists events
// to a newline-delimited JSON file with optional rotation.
//
// One writer goroutine (the server's dispatcher) appends events. The
// Query() method reads the file and returns matching events. For high-volume
// deployments, a separate indexer (FalkorDB / SQLite FTS) should be used
// instead of scanning the NDJSON.
type NDEventStore struct {
	mu       sync.Mutex
	path     string
	appendCh chan *FlowEvent
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// NewNDEventStore creates a new NDEventStore backed by the given file path.
// The file is created (with parent directories) on first write.
func NewNDEventStore(path string) *NDEventStore {
	// Ensure parent directory exists
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	s := &NDEventStore{
		path:     path,
		appendCh: make(chan *FlowEvent, 1024),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
	go s.writeLoop()
	return s
}

// Append adds an event to the store. Non-blocking if the buffer is full;
// in that case the event is dropped (with a log line) to protect the server
// from backpressure.
func (s *NDEventStore) Append(ev *FlowEvent) error {
	if ev.ID == "" {
		ev.ID = generateFlowID()
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	select {
	case s.appendCh <- ev:
		return nil
	default:
		// Buffer full — log and drop. This protects against event storms.
		log.Printf("[events] append buffer full, dropping event %s", ev.ID)
		return fmt.Errorf("event store buffer full")
	}
}

// writeLoop is the single writer goroutine. It serializes appends to the
// NDJSON file. Exits when stopCh is closed.
func (s *NDEventStore) writeLoop() {
	defer close(s.doneCh)
	for {
		select {
		case <-s.stopCh:
			// Drain remaining events on shutdown
			for {
				select {
				case ev := <-s.appendCh:
					s.writeOne(ev)
				default:
					return
				}
			}
		case ev := <-s.appendCh:
			s.writeOne(ev)
		}
	}
}

func (s *NDEventStore) writeOne(ev *FlowEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(ev)
	if err != nil {
		log.Printf("[events] marshal error: %v", err)
		return
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("[events] open %s: %v", s.path, err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		log.Printf("[events] write error: %v", err)
	}
}

// Query reads the NDJSON file and returns events matching the filter. For
// performance, callers should set a Limit. The file is read in full; for
// very large event logs, swap in a real indexer.
func (s *NDEventStore) Query(filter FlowEventFilter) ([]*FlowEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	out := make([]*FlowEvent, 0)
	scanner := bufio.NewScanner(f)
	// Allow large lines (events can carry JSON payloads).
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev FlowEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // skip malformed lines
		}
		if filter.FlowID != "" && ev.FlowID != filter.FlowID {
			continue
		}
		if filter.AgentID != "" && ev.AgentID != filter.AgentID {
			continue
		}
		if filter.Signal != "" && ev.Signal != filter.Signal {
			continue
		}
		if !filter.From.IsZero() && ev.Timestamp.Before(filter.From) {
			continue
		}
		if !filter.To.IsZero() && ev.Timestamp.After(filter.To) {
			continue
		}
		out = append(out, &ev)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// Stop is the implementation backing FlowEventStore interface (caller must
// type-assert) and is called from server.go's RegisterOnShutdown hook.
// Returns nil; logs any final errors.
func (s *NDEventStore) Close() {
	s.Stop()
}

// Stop signals the write loop to drain and exit. Safe to call once.
func (s *NDEventStore) Stop() {
	select {
	case <-s.stopCh:
		// already stopped
	default:
		close(s.stopCh)
		<-s.doneCh
	}
}

// Path returns the on-disk path of the NDJSON file.
func (s *NDEventStore) Path() string { return s.path }
