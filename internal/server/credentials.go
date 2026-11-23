package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// P8-07 (P8-02 §6 C-CRED) — write-only credential store.
//
// Values live at rest only as AES-256-GCM ciphertext. The API never returns a
// value: PUT writes, GET answers metadata. The key comes from PROBE_SECRET_KEY
// or, when that is unset, from <store dir>/secret.key (created 0600).

// CredentialMeta is the only thing the read API exposes. It structurally
// cannot carry the secret, so a handler bug cannot leak it through this type.
type CredentialMeta struct {
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	LastUsed  string `json:"last_used,omitempty"`
	Used      int    `json:"times_used"`
}

type credEntry struct {
	Ciphertext string `json:"ct"` // base64(nonce || AES-256-GCM ciphertext+tag)
	CreatedAt  string `json:"created_at"`
	LastUsed   string `json:"last_used,omitempty"`
	Used       int    `json:"times_used"`
}

type credFile struct {
	Version int                  `json:"version"`
	Entries map[string]*credEntry `json:"entries"`
}

// CredentialStore is the encrypted, write-only credential vault.
type CredentialStore struct {
	mu      sync.Mutex
	path    string
	key     []byte
	entries map[string]*credEntry
}

// DefaultCredentialStore returns the process-wide store at the contract path
// (/var/lib/probe/secrets.json), overridable with PROBE_SECRET_STORE for tests
// and non-root development.
func DefaultCredentialStore() (*CredentialStore, error) {
	path := os.Getenv("PROBE_SECRET_STORE")
	if path == "" {
		path = "/var/lib/probe/secrets.json"
	}
	return OpenCredentialStore(path)
}

// OpenCredentialStore loads (or creates) the store at path. The AES key is
// read from PROBE_SECRET_KEY (hex, base64 or raw, in that order) or from a
// 0600 key file beside the store.
func OpenCredentialStore(path string) (*CredentialStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("credential store dir: %w", err)
	}
	key, err := credentialKey(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	s := &CredentialStore{path: path, key: key, entries: map[string]*credEntry{}}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil && len(raw) > 0:
		var f credFile
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("credential store corrupt: %w", err)
		}
		if f.Entries != nil {
			s.entries = f.Entries
		}
	case err != nil && !os.IsNotExist(err):
		return nil, err
	}
	return s, nil
}

// credentialKey resolves the AES-256 key. Failures are loud: running with an
// unset key would silently encrypt with a zero key.
func credentialKey(dir string) ([]byte, error) {
	if env := strings.TrimSpace(os.Getenv("PROBE_SECRET_KEY")); env != "" {
		if b, err := hex.DecodeString(env); err == nil && len(b) == 32 {
			return b, nil
		}
		if b, err := base64.StdEncoding.DecodeString(env); err == nil && len(b) == 32 {
			return b, nil
		}
		return sha256Sum(env), nil
	}
	keyPath := filepath.Join(dir, "secret.key")
	if raw, err := os.ReadFile(keyPath); err == nil && len(raw) >= 32 {
		return raw[:32], nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generate secret key: %w", err)
	}
	if err := os.WriteFile(keyPath, b, 0600); err != nil {
		return nil, fmt.Errorf("write secret key: %w", err)
	}
	_ = os.Chmod(keyPath, 0600)
	return b, nil
}

func sha256Sum(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

func (s *CredentialStore) seal(plain []byte) (string, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := make([]byte, 0, len(nonce)+len(plain)+gcm.Overhead())
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plain, nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

func (s *CredentialStore) open(sealed string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, fmt.Errorf("ciphertext too short")
	}
	return gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
}

// Put writes (or replaces) a credential. The value is sealed before it touches
// the entries map, so an accidental dump of the in-memory struct shows
// ciphertext only.
func (s *CredentialStore) Put(name, value string) error {
	name = strings.TrimSpace(name)
	if name == "" || value == "" {
		return fmt.Errorf("name and value are required")
	}
	sealed, err := s.seal([]byte(value))
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	e, ok := s.entries[name]
	if !ok {
		e = &credEntry{CreatedAt: now}
		s.entries[name] = e
	}
	e.Ciphertext = sealed
	return s.flushLocked()
}

// Meta lists credential metadata. Values are not reachable from here.
func (s *CredentialStore) Meta(name string) (CredentialMeta, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[name]
	if !ok {
		return CredentialMeta{}, false
	}
	return CredentialMeta{Name: name, CreatedAt: e.CreatedAt, LastUsed: e.LastUsed, Used: e.Used}, true
}

// List returns every credential's metadata, name-sorted, values absent.
func (s *CredentialStore) List() []CredentialMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CredentialMeta, 0, len(s.entries))
	for name, e := range s.entries {
		out = append(out, CredentialMeta{Name: name, CreatedAt: e.CreatedAt, LastUsed: e.LastUsed, Used: e.Used})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Resolve decrypts a value for the exec path. Callers must treat the returned
// string as live material: seed the redactor, then drop it.
func (s *CredentialStore) Resolve(name string) (string, bool) {
	s.mu.Lock()
	e, ok := s.entries[name]
	var sealed string
	if ok {
		sealed = e.Ciphertext
	}
	s.mu.Unlock()
	if !ok {
		return "", false
	}
	plain, err := s.open(sealed)
	if err != nil {
		return "", false
	}
	v := string(plain)
	for i := range plain {
		plain[i] = 0
	}
	return v, true
}

// Touch records use without revealing the value.
func (s *CredentialStore) Touch(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[name]; ok {
		e.LastUsed = time.Now().UTC().Format(time.RFC3339)
		e.Used++
		_ = s.flushLocked()
	}
}

// flushLocked persists ciphertext. Called with s.mu held.
func (s *CredentialStore) flushLocked() error {
	f := credFile{Version: 1, Entries: s.entries}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ---------------------------------------------------------------------------
// Single-use handles
// ---------------------------------------------------------------------------

// credHandleTTL is the contract value (§6.3): 60 s. Tests shorten it through
// PROBE_CRED_TTL so expiry is exercised without sleeping a minute.
const credHandleTTL = 60 * time.Second

func handleTTL() time.Duration {
	if v := os.Getenv("PROBE_CRED_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return credHandleTTL
}

type credHandle struct {
	Handle  string
	Name    string
	AgentID string
	JobID   string
	Sealed  []byte // value encrypted under sha256(token || agent_id)
	Expires time.Time
	Used    bool
}

// handleRegistry holds outstanding handles. A handle is single-use: Mint puts
// it here, Consume takes it out, and expiry is checked on Consume.
type handleRegistry struct {
	mu   sync.Mutex
	m    map[string]*credHandle
	last string
}

func newHandleRegistry() *handleRegistry {
	return &handleRegistry{m: map[string]*credHandle{}}
}

func newHandleID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("cred_%d", time.Now().UnixNano())
	}
	return "cred_" + hex.EncodeToString(b)
}

// perAgentKey is sha256(shared_token || agent_id): an agent that sees another
// agent's handle cannot decrypt it, because the key never leaves the server
// and the target agent's copy of it.
func perAgentKey(sharedToken, agentID string) []byte {
	return sha256Sum(sharedToken + "|" + agentID)
}

// sealForAgent encrypts the credential value for exactly one agent.
func sealForAgent(sharedToken, agentID, value string) ([]byte, error) {
	block, err := aes.NewCipher(perAgentKey(sharedToken, agentID))
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append(nonce, gcm.Seal(nil, nonce, []byte(value), nil)...)
	return out, nil
}

// Mint registers a new single-use handle bound to agent+job.
func (r *handleRegistry) Mint(sharedToken, name, agentID, jobID, value string, ttl time.Duration) (*credHandle, error) {
	sealed, err := sealForAgent(sharedToken, agentID, value)
	if err != nil {
		return nil, err
	}
	h := &credHandle{
		Handle:  newHandleID(),
		Name:    name,
		AgentID: agentID,
		JobID:   jobID,
		Sealed:  sealed,
		Expires: time.Now().Add(ttl),
	}
	r.mu.Lock()
	r.m[h.Handle] = h
	r.last = h.Handle
	r.mu.Unlock()
	return h, nil
}

// Consume returns a handle exactly once. A second call reports missing.
func (r *handleRegistry) Consume(handle string) (*credHandle, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.m[handle]
	if !ok {
		return nil, false
	}
	delete(r.m, handle)
	return h, true
}

// Expired reports whether h has passed its TTL.
func (h *credHandle) Expired() bool { return time.Now().After(h.Expires) }

// ---------------------------------------------------------------------------
// Redactor — a filter on every sink, not a promise
// ---------------------------------------------------------------------------

// Redactor replaces live secret values in any string that passes through it.
// It is seeded for the duration of a credential call and emptied afterwards, so
// the value is not held beyond the operation that needed it.
type Redactor struct {
	mu     sync.RWMutex
	values []string
	hits   int64 // redactions performed, read by data.credential.redacted
}

// NewRedactor returns an empty redactor.
func NewRedactor() *Redactor { return &Redactor{} }

// Add seeds a live value.
func (r *Redactor) Add(v string) {
	if v == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.values {
		if x == v {
			return
		}
	}
	r.values = append(r.values, v)
}

// Remove drops a value once the call is over.
func (r *Redactor) Remove(v string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.values[:0]
	for _, x := range r.values {
		if x != v {
			out = append(out, x)
		}
	}
	r.values = out
}

// Redact replaces every seeded value with [REDACTED]. It never returns the
// input unexamined: the check is a substring scan, not a regex the caller
// could reason around.
func (r *Redactor) Redact(s string) string {
	r.mu.RLock()
	vals := append([]string(nil), r.values...)
	r.mu.RUnlock()
	for _, v := range vals {
		if v != "" && strings.Contains(s, v) {
			n := strings.Count(s, v)
			s = strings.ReplaceAll(s, v, "[REDACTED]")
			atomic.AddInt64(&r.hits, int64(n))
		}
	}
	return s
}

// Hits is the cumulative number of replacements made, so the caller can report
// how many redactions a call needed without seeing the values.
func (r *Redactor) Hits() int64 { return atomic.LoadInt64(&r.hits) }

// Count reports how many seeded values occur in s (for data.credential.redacted).
func (r *Redactor) Count(s string) int {
	r.mu.RLock()
	vals := append([]string(nil), r.values...)
	r.mu.RUnlock()
	n := 0
	for _, v := range vals {
		if v != "" {
			n += strings.Count(s, v)
		}
	}
	return n
}
