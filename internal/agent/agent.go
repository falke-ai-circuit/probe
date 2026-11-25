package agent

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"unicode/utf16"
	"time"

	cryptomanager "github.com/falke-ai-circuit/probe/internal/crypto"
	"github.com/falke-ai-circuit/probe/internal/platform"
	"github.com/falke-ai-circuit/probe/internal/protocol"
	"github.com/falke-ai-circuit/probe/internal/version"
	"github.com/gorilla/websocket"
)

const (
	pingInterval    = 15 * time.Second
	missThreshold   = 3
	defaultTimeout  = 60
	maxTimeout      = 300
)

// A3: agent-side deadlines for the fs-* surface. The server's forward wall for
// these calls is 120 s (server_api.go forwardToAgentWithTimeout); the caller in
// the field saw 300 s. A stalled volume — offline share, spun-down disk — is a
// normal Windows condition, not a crash, so the agent answers first with a
// typed error and the caller learns the cause instead of blocking.
//
// These are vars, not consts, so tests can shorten them; they are read once per
// call and never mutated at runtime.
var (
	fsListDeadline  = 30 * time.Second
	fsReadDeadline  = 30 * time.Second
	fsHashDeadline  = 30 * time.Second
	fsWriteDeadline = 30 * time.Second
	fsStatDeadline  = 30 * time.Second
	fsMetaDeadline  = 30 * time.Second
)

// P8-05: per-request deadline bounds. deadline_ms is capped so a caller cannot
// ask for an unbounded fs operation; no fs-* handler is left outside boundedFS.
const (
	fsDefaultDeadlineMs = 30000
	fsMaxDeadlineMs     = 120000
)

// fsDeadline resolves the deadline for one call: deadline_ms when supplied,
// capped at 120 s, otherwise the op's default.
func fsDeadline(params protocol.FSParams, def time.Duration) time.Duration {
	d := def
	if params.DeadlineMs > 0 {
		d = time.Duration(params.DeadlineMs) * time.Millisecond
	}
	if d > fsMaxDeadlineMs*time.Millisecond {
		d = fsMaxDeadlineMs * time.Millisecond
	}
	return d
}

// ErrFSDeadline marks an fs-* call that did not return inside its deadline.
// The underlying syscall cannot be cancelled: the goroutine stays parked on the
// stalled volume, which is why the answer is a typed error rather than silence.
var ErrFSDeadline = errors.New("filesystem operation exceeded its deadline")

// boundedFS runs fn under a deadline. It is the only place the fs-* handlers
// wait for the disk, so the bound cannot be forgotten on a new handler. The
// timeout error names the operation, the path and the elapsed time, so the
// caller learns what stalled without a second round trip.
func boundedFS[T any](d time.Duration, op, path string, fn func() (T, error)) (T, error) {
	type outcome struct {
		v   T
		err error
	}
	ch := make(chan outcome, 1)
	start := time.Now()
	go func() {
		v, err := fn()
		ch <- outcome{v: v, err: err}
	}()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case o := <-ch:
		return o.v, o.err
	case <-timer.C:
		var zero T
		elapsed := time.Since(start).Milliseconds()
		return zero, fmt.Errorf("%s %s: %w (elapsed %d ms, deadline %d ms)", op, path, ErrFSDeadline, elapsed, d.Milliseconds())
	}
}

// fileDigestResult carries the verified size and digest of a file.
type fileDigestResult struct {
	Size int64
	Sum  string
}

// fileDigest reads the file back from disk and returns its size and lowercase
// hex SHA256. Verification always goes through here: the digest of what the
// request claimed to send is not evidence that those bytes reached the disk.
func fileDigest(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return 0, "", err
	}
	return info.Size(), fmt.Sprintf("%x", h.Sum(nil)), nil
}

// Config holds agent configuration.
type Config struct {
	Mode     string // "outbound", "inbound", "dual"
	URL      string // wss://host:port for outbound
	Addr     string // :port for inbound
	Token    string
	CertPath string // CA cert for outbound (server verification)
	// ClientCertFile/ClientKeyFile are an optional client certificate used for
	// TLS mutual authentication when dialing a wss:// server (mTLS).
	ClientCertFile string // client cert for mTLS (outbound)
	ClientKeyFile  string // client key for mTLS (outbound)
	CertFile       string // TLS cert for inbound
	KeyFile        string // TLS key for inbound
	Name           string // optional display name
	LogPath        string // log file path (empty = stdout)
	MaxRetries     int           // 0 = infinite retries
	BackoffMin     time.Duration // default 1s
	BackoffMax     time.Duration // default 60s
	TokenFile      string        // path to persist token (empty = no persistence)
	// Permissions tier: "read-only", "standard", "full" (default: "full")
	// read-only: fs-read, fs-list, fs-stat, fs-hash, exec (read-only commands only)
	// standard: read-only + exec (all commands) + fs-write + fs-mkdir + fs-move
	// full: everything (no restrictions)
	Permissions string
	// SandboxDir restricts all filesystem operations to within this directory.
	// Empty = no restriction. Combined with permissions tier for defense-in-depth.
	SandboxDir string
	// Capabilities is the list of capabilities this agent advertises to the
	// server on connect (e.g. "exec", "filesystem", "capture"). When empty,
	// the server treats the agent as having all capabilities (backward compat).
	Capabilities []string
	// ConfigPath is the path to the config file used to start this agent,
	// resolved at startup by ResolveConfigPath (explicit flag, probe-client.json,
	// agent-named <exe>.json, the single .json beside the exe). It is empty when
	// the build carries EmbeddedConfig instead. The updater hands this exact path
	// to the canary — never the child's default filename.
	ConfigPath string
	// EmbeddedConfig is true when the settings were compiled into the binary
	// (-X main.configB64). Such a build needs no config file, so a canary may be
	// launched without --config.
	EmbeddedConfig bool
	// StateDir is where durable agent state lives — for P8-04, detached job
	// spools. Empty resolves to PROBE_STATE_DIR, then <ConfigPath dir>/state,
	// then ./probe-state. It must be stable across restarts: job_status is
	// answered from this directory after the agent has been replaced.
	StateDir string
	// E2EEnabled enables AES-GCM end-to-end encryption of protocol payloads.
	// When true, all messages between agent and server are encrypted with
	// a key derived from the token (SHA-256). Relays see only encrypted bytes.
	// Default: false (backward compat).
	E2EEnabled bool
	// CanaryMode marks this process as an update canary: it connects under a
	// distinct name, proves the connection is stable, then atomically swaps
	// itself into the canonical binary path and stops the old process — only
	// after the new version is confirmed healthy. Backward compatible: false
	// for every normal run.
	CanaryMode bool
	// CanaryOldPID is the PID of the old process to stop after a successful swap.
	CanaryOldPID int
	// Version is the build version this agent reports to the server as
	// agent_info.Version. Callers pass the same string their banner prints
	// (typically version.Display()); it is normalised to the bare wire form
	// by VersionForInfo. Empty means "use the package default" (Version),
	// which keeps Config literals that predate this field compiling and
	// reporting a real version.
	Version string
	// StatusPath is an explicit path for the run-state record the status
	// surface reads (--status-file / $PROBE_STATUS_FILE). Empty resolves via
	// lifecycle.ResolvePath: $PROBE_STATE_DIR, then the platform's shared state
	// directory, then <exe dir>/state. The agent and the status command share
	// that resolver so they cannot disagree about which file they mean.
	StatusPath string
	// DoubleClickMS is the EFFECTIVE double-click interval of the seat this
	// agent runs on, in milliseconds. §9.2's 400 ms is a fallback, not a
	// universal: a desktop configured for 200 ms would not read presses spaced
	// for a 400 ms window as one double-click, so the gesture's spacing is
	// derived from this value. Zero means "not configured" and resolves to the
	// contract default. Nothing reads this from the JSON config file yet —
	// cmd/probe/config.go is outside this unit's scope — so it is the seam a
	// config mapper (or a caller that knows the seat) sets at construction.
	DoubleClickMS int
}

// Version is the wire form of the version this build reports when Config.Version
// is not set. It is derived from internal/version — the single source of truth —
// so it is not an independent literal and cannot drift from the client banner.
var Version = version.Wire()

// VersionForInfo returns the version string to report in agent_info for cfg.
// It is the only place the reported version is decided: the configured build
// version wins when present (normalised to the wire form), otherwise the
// package default is used.
func VersionForInfo(cfg Config) string {
	if strings.TrimSpace(cfg.Version) != "" {
		return version.Normalize(cfg.Version)
	}
	return Version
}

// Agent is the remote agent instance.
type Agent struct {
	cfg            Config
	conn           *websocket.Conn
	connectedAt    time.Time
	mu             sync.Mutex
	writeMu        sync.Mutex // protects WebSocket writes (prevents concurrent WriteJSON panic)
	lastPing       time.Time
	pingMisses     int
	backoffAttempt int
	plat           platform.Platform

	// openDisplay opens the display a window-scoped input request resolves
	// against (§9.1). It is a field so the acceptance tests drive the X11 path
	// through the same treeDisplay seam the window tree uses; nil means the
	// platform's own opener.
	openDisplay func(num int) (windowProbe, error)
	// clickWindow is this agent's effective double-click interval, resolved
	// once at construction from Config.DoubleClickMS. The gesture's spacing is
	// derived from it (clickSpacing), so a seat configured below the 400 ms
	// contract default still receives presses inside its own window. Zero means
	// "nothing configured" and effectiveClickWindow answers the default.
	clickWindow time.Duration
	server         *protocol.Server
	stopped        chan struct{}
	tunnelMgr      *tunnelManager
	mitmMgr        *mitmManager
	debugMgr       *debugManager
	streamMgr      *streamManager

	// Phase 4: optional mode manager for dynamic mode switching.
	// When set, the agent can handle mode_control messages from the server
	// to start/stop serve/connect/relay modes at runtime.
	modeMgr        *modeManagerRef

	// spawnedPIDs tracks PIDs of processes started by this agent (proc_start or exec).
	// In sandboxed mode, only these PIDs can be killed — protecting other system processes.
	spawnedPIDs   map[int]bool
	spawnedPIDMu  sync.Mutex

	// tokenExpiry is the expiry time of the current token, if the server has
	// issued a rotating token with an expiry. A zero value means "no expiry".
	// Guarded by mu alongside cfg.Token.
	tokenExpiry time.Time

	// Phase 4 Step 11: forward policies for server-as-relay.
	// Maps local agent ID → "relay" or "local" forwarding policy.
	forwardMu       sync.RWMutex
	forwardPolicies map[string]string

	// Phase 4 Step 13: E2E encryption manager (optional).
	// When active, all outgoing messages are encrypted and incoming
	// messages are decrypted. Relays see only encrypted bytes.
	e2eMgr *cryptomanager.Manager

	// P8-07: live credential values waiting for the exec that names them.
	// Held only in memory, zeroed after the child exits.
	credMu      sync.Mutex
	credPending map[string]*pendingCredential

	// statusPub is the live run-state record this process published, or nil
	// when publishing failed (read-only state dir). It is the same object the
	// heartbeat rewrites, so an identity change applied after the canary swap
	// (P12-04 F2: name/executable still named the -canary staging path) is not
	// reverted by the next tick. Guarded by mu for the pointer, by its own
	// mutex for the record contents.
	statusPub *statusPublisher
}

// writeMessage sends a WebSocket message with write mutex protection.
// gorilla/websocket panics on concurrent writes — this must be used for ALL writes.
// When E2E encryption is active, the JSON payload is encrypted before sending.
func (a *Agent) writeMessage(conn *websocket.Conn, env protocol.Envelope) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	agentMsgSent.Add(1)

	if a.e2eMgr != nil && a.e2eMgr.IsActive() {
		// Encrypt the JSON payload
		plaintext, err := json.Marshal(env)
		if err != nil {
			return fmt.Errorf("marshal for e2e: %w", err)
		}
		ciphertext, err := a.e2eMgr.Encrypt(plaintext)
		if err != nil {
			return fmt.Errorf("e2e encrypt: %w", err)
		}
		return conn.WriteMessage(websocket.BinaryMessage, ciphertext)
	}
	return protocol.WriteMessage(conn, env)
}

// New creates a new agent.
func New(cfg Config) *Agent {
	// Auto-sandbox: if permissions is "sandboxed" and sandbox_dir is empty,
	// use the current working directory as the sandbox.
	if cfg.Permissions == "sandboxed" && cfg.SandboxDir == "" {
		if cwd, err := os.Getwd(); err == nil {
			cfg.SandboxDir = cwd
		}
	}
	return &Agent{
		cfg:          cfg,
		stopped:      make(chan struct{}),
		plat:         platform.New(cfg.Name),
		clickWindow:  clickWindowFor(cfg.DoubleClickMS),
		tunnelMgr:    newTunnelManager(),
		mitmMgr:      newMitmManager(),
		debugMgr:     newDebugManager(),
		spawnedPIDs:  make(map[int]bool),
		e2eMgr:       cryptomanager.NewManager(cfg.Token, cfg.E2EEnabled),
	}
}

// Stop signals the agent to shut down gracefully.
// This closes the stopped channel, causing the Run loop to exit.
func (a *Agent) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	select {
	case <-a.stopped:
		// already closed
	default:
		close(a.stopped)
	}
	if a.conn != nil {
		a.conn.Close()
	}
}

// Run starts the agent in the configured mode.
func (a *Agent) Run() error {
	// Publish the run-state record the status surface reads (see statusfile.go)
	// for every mode. The result is what "is it running, which version, for how
	// long" is answered from — live process facts, not configuration.
	stopStatus := a.startStatusFile()
	defer stopStatus()

	switch a.cfg.Mode {
	case "outbound":
		return a.runOutbound()
	case "inbound":
		return a.runInbound()
	case "dual":
		go a.runInbound()
		return a.runOutbound()
	default:
		return fmt.Errorf("unknown mode: %s", a.cfg.Mode)
	}
}

func (a *Agent) runOutbound() error {
	log.Printf("Connecting to %s (mode: outbound)", a.cfg.URL)
	for {
		conn, err := protocol.Dial(a.cfg.URL, a.cfg.CertPath, a.cfg.ClientCertFile, a.cfg.ClientKeyFile, a.cfg.Token)
		if err != nil {
			a.backoffAttempt++
			agentReconnects.Add(1)
			if a.cfg.MaxRetries > 0 && a.backoffAttempt > a.cfg.MaxRetries {
				return fmt.Errorf("max retries (%d) exceeded: %w", a.cfg.MaxRetries, err)
			}
			backoff := a.computeBackoff()
			log.Printf("Connection failed (attempt %d): %v, retrying in %v", a.backoffAttempt, err, backoff)
			select {
			case <-a.stopped:
				return nil
			case <-time.After(backoff):
			}
			continue
		}
		log.Printf("Connected.")
		a.mu.Lock()
		a.conn = conn
		a.connectedAt = time.Now()
		a.pingMisses = 0
		a.backoffAttempt = 0
		a.mu.Unlock()
		a.handleConnection(conn)
		// disconnected — reconnect (unless this is a canary that aborted before
		// committing: in that case the old process is still running and must be
		// left untouched, so the canary exits without reconnecting).
		if a.cfg.CanaryMode {
			log.Printf("[canary] disconnected — aborting update, old keeps running")
			return nil
		}
		log.Printf("Disconnected, reconnecting...")
		select {
		case <-a.stopped:
			return nil
		default:
		}
	}
}

// computeBackoff returns an exponential backoff duration with jitter.
// Formula: min * 2^(attempt-1) capped at max, plus random jitter.
func (a *Agent) computeBackoff() time.Duration {
	min := a.cfg.BackoffMin
	max := a.cfg.BackoffMax
	if min <= 0 {
		min = 1 * time.Second
	}
	if max <= 0 {
		max = 60 * time.Second
	}
	// Cap the exponent to avoid overflow: 2^10 = 1024 is plenty
	exp := a.backoffAttempt - 1
	if exp > 10 {
		exp = 10
	}
	base := min * time.Duration(1<<exp)
	if base > max {
		base = max
	}
	// Add jitter: random value in [0, base/2]
	jitter := time.Duration(rand.Int64N(int64(base/2 + 1)))
	return base + jitter
}

func (a *Agent) runInbound() error {
	log.Printf("Listening on %s (mode: inbound)", a.cfg.Addr)
	srv, err := protocol.NewServer(a.cfg.Addr, a.cfg.CertFile, a.cfg.KeyFile, "")
	if err != nil {
		return fmt.Errorf("start server: %w", err)
	}
	a.server = srv
	srv.OnConnect = func(conn *websocket.Conn, r *http.Request) bool {
		token := r.Header.Get("Authorization")
		if token != "Bearer "+a.cfg.Token {
			log.Printf("Rejected connection from %s: authentication failed", r.RemoteAddr)
			return false
		}
		log.Printf("Accepted connection from %s", r.RemoteAddr)
		a.mu.Lock()
		a.conn = conn
		a.connectedAt = time.Now()
		a.pingMisses = 0
		a.mu.Unlock()
		go a.handleConnection(conn)
		return true
	}
	log.Printf("Server stopped.")
	return srv.ListenAndServe()
}

func (a *Agent) handleConnection(conn *websocket.Conn) {
	defer func() {
		conn.Close()
		a.closeAllTunnels()
		a.closeAllMitm()
		a.closeAllDebug()
		a.closeAllStreams()
	}()

	// Send agent info
	info := protocol.AgentInfo{
		Name:            a.cfg.Name,
		Version:         VersionForInfo(a.cfg),
		OS:              getOS(),
		Arch:            getArch(),
		Mode:            a.cfg.Mode,
		ProtocolVersion: "2",
		Capabilities:    a.advertisedCapabilities(),
	}
	if err := a.writeMessage(conn, protocol.Envelope{
		ID:     "agent-info",
		Type:   "agent_info",
		Result: mustMarshal(info),
	}); err != nil {
		log.Printf("failed to send agent info: %v", err)
		return
	}

	// If this is a canary process (update candidate), start the prove-healthy →
	// swap → stop-old sequence once the connection is up. Backward compatible:
	// CanaryMode is false for every normal run.
	if a.cfg.CanaryMode {
		go a.runCanaryCommit(conn)
	}

	// Ping ticker
	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	// Token-refresh ticker: checks every minute whether the current token is
	// close to expiry and, if so, asks the server for a new one proactively.
	refreshInterval := 60 * time.Second
	refreshTicker := time.NewTicker(refreshInterval)
	defer refreshTicker.Stop()
	// refreshLeadTime is how far before expiry the agent requests a new token.
	const refreshLeadTime = 5 * time.Minute

	// Read messages
	readErr := make(chan error, 1)
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			agentMsgRecv.Add(1)
			var env protocol.Envelope
			if err := json.Unmarshal(msg, &env); err != nil {
				resp := protocol.NewError("", protocol.ErrInvalidParams, "invalid JSON")
				a.writeMessage(conn, resp)
				continue
			}
			a.handleCommand(conn, env)
		}
	}()

	for {
		select {
		case err := <-readErr:
			log.Printf("read error: %v", err)
			return
		case <-pingTicker.C:
			a.mu.Lock()
			a.pingMisses++
			if a.pingMisses >= missThreshold {
				log.Printf("ping timeout (%d misses), closing", a.pingMisses)
				a.mu.Unlock()
				return
			}
			a.mu.Unlock()
			if err := a.writeMessage(conn, protocol.Envelope{
				ID:   fmt.Sprintf("ping-%d", time.Now().UnixMilli()),
				Type: protocol.TypePing,
			}); err != nil {
				log.Printf("ping failed: %v", err)
				return
			}
		case <-refreshTicker.C:
			// Proactive refresh: if the token has an expiry and we're within
			// refreshLeadTime of it, ask the server to rotate the token now.
			a.mu.Lock()
			expiry := a.tokenExpiry
			a.mu.Unlock()
			if !expiry.IsZero() && time.Now().Add(refreshLeadTime).After(expiry) {
				log.Printf("token nearing expiry (%v), requesting refresh", expiry)
				refreshEnv := protocol.Envelope{
					ID:   fmt.Sprintf("token-refresh-%d", time.Now().UnixMilli()),
					Type: protocol.TypeTokenRefresh,
				}
				if err := a.writeMessage(conn, refreshEnv); err != nil {
					log.Printf("token refresh request failed: %v", err)
				}
			}
		}
	}
}

// cmdTable is a data-driven command registry (the "vault"): each command
// type maps to its handler. Lookup-by-key keeps the dispatch control-flow
// free of a per-command switch statement.
var cmdTable = map[string]func(*Agent, protocol.Envelope) protocol.Envelope{
	protocol.TypePing: func(a *Agent, env protocol.Envelope) protocol.Envelope { return protocol.NewPong(env.ID) },
	protocol.TypeExec: (*Agent).handleExec,
	protocol.TypeExecPTY: (*Agent).handleShellPTY,
	protocol.TypeFSList: (*Agent).handleFSList,
	protocol.TypeFSStat: (*Agent).handleFSStat,
	protocol.TypeFSRead: (*Agent).handleFSRead,
	protocol.TypeFileSave: (*Agent).handleFSWrite,
	protocol.TypeFileRemove: (*Agent).handleFSDelete,
	protocol.TypeFSMove: (*Agent).handleFSMove,
	protocol.TypeFSMkdir: (*Agent).handleFSMkdir,
	protocol.TypeFSHash: (*Agent).handleFSHash,
	protocol.TypeCapture: (*Agent).handleCapture,
	protocol.TypeStreamBegin: (*Agent).handleStreamBegin,
	protocol.TypeStreamEnd: (*Agent).handleStreamEnd,
	protocol.TypeDisplayInfo: (*Agent).handleDisplayInfo,
	protocol.TypePointerClick: (*Agent).handleClick,
	protocol.TypePointerMove: (*Agent).handlePointerMove,
	protocol.TypePointerScroll: (*Agent).handlePointerScroll,
	protocol.TypePointerDrag: (*Agent).handlePointerDrag,
	protocol.TypeUITree: (*Agent).handleUITree,
	protocol.TypeUIWindow: (*Agent).handleUIWindow,
	// P13-02 (§6): read-only context enumeration. Body-less: it answers from
	// the host, it does not act on a context.
	protocol.TypeContexts: (*Agent).handleContexts,
	protocol.TypeTextInput: (*Agent).handleType,
	protocol.TypeKeyPress: (*Agent).handleKey,
	protocol.TypeKeyCombo: (*Agent).handleKeyCombo,
	protocol.TypeHealth: (*Agent).handleHealth,
	protocol.TypeTaskList: (*Agent).handleTaskList,
	protocol.TypeTaskStop: (*Agent).handleTaskStop,
	protocol.TypeOpenLink: (*Agent).handleOpenLink,
	protocol.TypeNotify: (*Agent).handleNotify,
	protocol.TypeClipboardRead: (*Agent).handleClipboardRead,
	protocol.TypeClipboardWrite: (*Agent).handleClipboardWrite,
	protocol.TypeSensorList: (*Agent).handleSensorList,
	protocol.TypeSensorRead: (*Agent).handleSensorRead,
	protocol.TypeAuthRefresh: (*Agent).handleTokenRotate,
	protocol.TypeTokenRotate: (*Agent).handleTokenRotate,
	protocol.TypeTunnelOpen: (*Agent).handleTunnelOpen,
	protocol.TypeTunnelData: (*Agent).handleTunnelData,
	protocol.TypeTunnelClose: (*Agent).handleTunnelClose,
	protocol.TypeProcList: (*Agent).handleProcList,
	protocol.TypeProcKill: (*Agent).handleProcKill,
	protocol.TypeProcStart: (*Agent).handleProcStart,
	protocol.TypeMitmStart: (*Agent).handleMitmStart,
	protocol.TypeMitmStop: (*Agent).handleMitmStop,
	protocol.TypeMitmData: (*Agent).handleMitmTraffic,
	protocol.TypeDebugAttach: (*Agent).handleDebugAttach,
	protocol.TypeDebugDetach: (*Agent).handleDebugDetach,
	protocol.TypeDebugReadMem: (*Agent).handleDebugReadMem,
	protocol.TypeDebugModules: (*Agent).handleDebugModules,
	protocol.TypeDebugMemQuery: (*Agent).handleDebugMemQuery,
	protocol.TypeAgentUpdate: (*Agent).handleAgentUpdate,
	protocol.TypeSocks5Start: (*Agent).handleSocks5Start,
	protocol.TypeSocks5Stop: (*Agent).handleSocks5Stop,
	protocol.TypePortForward: (*Agent).handlePortForward,
	protocol.TypePortScan: (*Agent).handlePortScan,
	protocol.TypeNetConnections: (*Agent).handleNetConnections,
	protocol.TypeAutostartEnable: (*Agent).handleAutostartEnable,
	protocol.TypeAutostartDisable: (*Agent).handleAutostartDisable,
	protocol.TypeAutostartStatus: (*Agent).handleAutostartStatus,
	protocol.TypeFileSearch: (*Agent).handleFileSearch,
	protocol.TypeSysInfo: (*Agent).handleSysInfo,
	protocol.TypeModeControl: (*Agent).handleModeControl,
	protocol.TypeForwardPolicy: (*Agent).handleForwardPolicy,
	protocol.TypeReconfigure: (*Agent).handleReconfigure,
	// P8-04: detached job queries, answered from the spool file.
	protocol.TypeJobStatus: (*Agent).handleJobStatus,
	protocol.TypeJobLog:    (*Agent).handleJobLog,
	// P8-07: the credential handle arrives as its own envelope, immediately
	// before the exec that references it by name.
	protocol.TypeCredentialDeliver: (*Agent).handleCredentialDeliver,
}

func (a *Agent) handleCommand(conn *websocket.Conn, env protocol.Envelope) {
	var resp protocol.Envelope

	// Permission check: extract command string (for exec destructive filter)
	// and path (for fs sandbox check)
	execCmd := ""
	path := ""
	if env.Type == protocol.TypeExec || env.Type == protocol.TypeExecPTY {
		if params, err := protocol.ParseCommandStrict[protocol.ExecParams](env, protocol.AllowExec); err == nil {
			execCmd = params.Command
		}
	}
	// Extract path from FS params for sandbox checking
	if env.Type == protocol.TypeFileSave || env.Type == protocol.TypeFileRemove ||
		env.Type == protocol.TypeFSList || env.Type == protocol.TypeFSStat ||
		env.Type == protocol.TypeFSRead || env.Type == protocol.TypeFSHash ||
		env.Type == protocol.TypeFSMkdir {
		if params, err := protocol.ParseCommand[protocol.FSParams](env); err == nil {
			path = params.Path
		}
	}
	if env.Type == protocol.TypeFSMove {
		if params, err := protocol.ParseCommand[protocol.FSParams](env); err == nil {
			path = params.To // check destination path
		}
	}
	// Bypass: user-approved override — skip permission check entirely.
	// Logged for audit trail. The bypass flag is set by the server-side
	// agent (Hermes) only after explicit user approval in DM.
	if env.Bypass {
		log.Printf("[PERMISSION BYPASS] type=%s cmd=%q path=%q — user-approved override, skipping permission check", env.Type, execCmd, path)
	} else if !isAllowed(a.cfg.Permissions, a.cfg.SandboxDir, env.Type, execCmd, path) {
		resp = protocol.NewError(env.ID, "permission_denied",
			fmt.Sprintf("command type '%s' is not allowed under permissions '%s'", env.Type, a.cfg.Permissions))
		if err := a.writeMessage(conn, resp); err != nil {
			log.Printf("write error: %v", err)
		}
		return
	}

	// P13-02 (§6): a selector must name a context this agent enumerated and
	// classifies addressable. The gate runs before dispatch, so a refused
	// selector cannot leave a half-acted command behind. It reads the row's
	// verdict from the same enumeration /contexts returns.
	if refusal := a.selectorGate(env); refusal != nil {
		if err := a.writeMessage(conn, *refusal); err != nil {
			log.Printf("write error: %v", err)
		}
		return
	}

	// Pong is an acknowledgment that resets the miss counter and needs no
	// reply, so it is handled before the data-driven dispatch table.
	if env.Type == protocol.TypePong {
		a.mu.Lock()
		a.pingMisses = 0
		a.mu.Unlock()
		return
	}

	// Data-driven dispatch: look the command up in the registry rather than
	// branching on it directly.
	if handler, ok := cmdTable[env.Type]; ok {
		resp = handler(a, env)
	} else {
		resp = protocol.NewError(env.ID, protocol.ErrInvalidParams, fmt.Sprintf("unknown command: %s", env.Type))
	}
	if err := a.writeMessage(conn, resp); err != nil {
		log.Printf("write error: %v", err)
	}
}

func (a *Agent) handleExec(env protocol.Envelope) protocol.Envelope {
	params, serr := protocol.ParseCommandStrict[protocol.ExecParams](env, protocol.AllowExec)
	if serr != nil {
		return strictRefusal(env, serr)
	}
	// P8-03: `as` is a declaration we verify, not an escalation primitive.
	// Refusal happens BEFORE anything is spawned, so a refused call cannot
	// leave a half-run command behind.
	if want := strings.TrimSpace(params.As); want != "" && !strings.EqualFold(want, "current") {
		if !strings.EqualFold(want, "SYSTEM") {
			return protocol.NewError(env.ID, protocol.ErrInvalidParams,
				fmt.Sprintf("as must be \"current\" or \"SYSTEM\", got %q", params.As))
		}
		pr := currentPrincipal()
		if !pr.IsSystem {
			return protocol.NewErrorDetail(env.ID, protocol.ErrElevationUnavailable,
				fmt.Sprintf("exec{as:\"SYSTEM\"} refused: agent principal is %q (is_system=false, integrity=%s); "+
					"install the agent as a Windows service so the process itself runs as LocalSystem",
					pr.User, pr.Integrity),
				map[string]interface{}{
					"principal":      pr.User,
					"is_system":      pr.IsSystem,
					"elevated":       pr.Elevated,
					"integrity":      pr.Integrity,
					"token_filtered": pr.TokenFiltered,
					"remediation":    "install the agent as a Windows service (probe-client.exe --install-service, one admin step per node); the service principal is LocalSystem",
					"requested":      "SYSTEM",
				})
		}
	}
	// P8-04: a detached exec bypasses the per-call deadline entirely — that
	// deadline is what killed the runner between `shutdown` and
	// `no shutdown`. It returns a handle and the guardian owns completion.
	if params.Detach {
		return a.handleExecDetached(env, params)
	}
	if params.Timeout <= 0 {
		params.Timeout = defaultTimeout
	}
	if params.Timeout > maxTimeout {
		params.Timeout = maxTimeout
	}
	start := time.Now()
	// P8-07: a named credential is consumed from memory and handed to the
	// child on stdin (default) or in its environment (documented fallback).
	// Either way it is absent from argv; only the NAME appears in the command.
	var result protocol.ExecResult
	var err error
	if name := strings.TrimSpace(params.Credential); name != "" {
		pc, expired := a.takeCredential(name)
		if expired {
			return protocol.NewError(env.ID, protocol.ErrCredentialExpired,
				fmt.Sprintf("credential handle for %q expired; the server may re-mint once", name))
		}
		if pc == nil {
			return protocol.NewError(env.ID, protocol.ErrCredentialUnknown,
				fmt.Sprintf("no delivered handle for credential %q on this agent", name))
		}
		// P8-18 (P8-17 C6): the job binding is enforced where the secret is
		// actually opened. A handle minted for job J is refused for any other
		// job, and the refusal consumes the parked secret like any use would.
		if pc.JobID != "" && pc.JobID != params.JobID {
			zeroCredential(pc.value)
			return protocol.NewError(env.ID, protocol.ErrCredentialJobMismatch,
				fmt.Sprintf("credential handle for %q is bound to job %s, not job %s", name, pc.JobID, params.JobID))
		}
		// The buffer is zeroed on every exit path after this point.
		defer zeroCredential(pc.value)
		if strings.EqualFold(pc.Mode, "env") {
			if params.Env == nil {
				params.Env = map[string]string{}
			}
			params.Env["PROBE_CREDENTIAL"] = string(pc.value)
			result, err = a.plat.Exec(params.Command, params.Timeout, params.WorkDir, params.Env)
		} else {
			result, err = platform.ExecWithStdin(params.Command, params.Timeout, params.WorkDir, params.Env, pc.value)
		}
		// Scrub at the source: whatever the child printed, the secret cannot
		// reach the response, the job spool or the agent's own log. The
		// server-side redactor is the second layer, not the only one.
		result.Stdout = scrubSecret(result.Stdout, pc.value)
		result.Stderr = scrubSecret(result.Stderr, pc.value)
	} else {
		result, err = a.plat.Exec(params.Command, params.Timeout, params.WorkDir, params.Env)
	}
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}
	result.DurationMs = time.Since(start).Milliseconds()
	// P8-03 envelope: the same shape for an elevated and a normal exec.
	// started_at_* are the instant the command was dispatched; tz is declared
	// by the host, never inferred by the caller.
	tzName, tzOff := agentTZ()
	result.StartedAtUTC = start.UTC().Format(time.RFC3339Nano)
	result.StartedAtLocal = start.Format(time.RFC3339)
	result.TZ = tzName
	result.TZOffsetMin = tzOff
	result.Principal = currentPrincipal().User
	return protocol.NewResult(env.ID, protocol.TypeExecResult, result)
}

func (a *Agent) handleShellPTY(env protocol.Envelope) protocol.Envelope {
	return a.handleExec(env)
}

// --- command handlers for all protocol commands ---

func (a *Agent) handleFSList(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommand[protocol.FSParams](env)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}
	// A3: a directory on an offline share never returns from ReadDir. The
	// deadline turns that into a typed TIMEOUT for the caller.
	entries, err := boundedFS(fsDeadline(params, fsListDeadline), "fs-list", params.Path, func() ([]protocol.FSEntry, error) {
		return a.plat.ListDir(params.Path)
	})
	if err != nil {
		if errors.Is(err, ErrFSDeadline) {
			return protocol.NewError(env.ID, protocol.ErrTimeout, err.Error())
		}
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}
	return protocol.NewResult(env.ID, protocol.TypeFSListResult, withListingDigest(entries))
}

// withListingDigest stamps the entry count and the digest of the canonical JSON
// of the entries on a listing (P8-06 / P8-02 §4.1). A listing conveys no file
// bytes, so the thing it can prove is the exact array that was returned: a
// caller that hashes the entries it received and compares gets a real equality
// check instead of a claim.
func withListingDigest(entries []protocol.FSEntry) protocol.FSListResult {
	if entries == nil {
		entries = []protocol.FSEntry{}
	}
	canonical, err := json.Marshal(entries)
	res := protocol.FSListResult{
		Entries:     entries,
		Count:       len(entries),
		EntryCount:  len(entries),
		Sha256Scope: "listing",
	}
	if err == nil {
		sum := sha256.Sum256(canonical)
		res.Sha256 = fmt.Sprintf("%x", sum)
	}
	return res
}

func (a *Agent) handleFSStat(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommand[protocol.FSParams](env)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}
	result, err := boundedFS(fsDeadline(params, fsStatDeadline), "fs-stat", params.Path, func() (protocol.FSStatResult, error) {
		return a.plat.FileStat(params.Path)
	})
	if err != nil {
		if errors.Is(err, ErrFSDeadline) {
			return protocol.NewError(env.ID, protocol.ErrTimeout, err.Error())
		}
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}
	// P8-06 (P8-02 §4.1): fs-stat conveys metadata, so the digest covers the
	// canonical JSON of the record it is about to return. Computed over the
	// zero-valued digest fields so the hash is a function of the record alone.
	stamp := result
	stamp.Sha256 = ""
	stamp.Sha256Scope = ""
	if canonical, err := json.Marshal(stamp); err == nil {
		sum := sha256.Sum256(canonical)
		result.Sha256 = fmt.Sprintf("%x", sum)
		result.Sha256Scope = "listing"
	}
	return protocol.NewResult(env.ID, protocol.TypeFSStatResult, result)
}

func (a *Agent) handleFSRead(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommand[protocol.FSParams](env)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}
	result, err := boundedFS(fsDeadline(params, fsReadDeadline), "fs-read", params.Path, func() (protocol.FSReadResult, error) {
		return a.plat.ReadFile(params.Path, params.Offset, params.Limit)
	})
	if err != nil {
		if errors.Is(err, ErrFSDeadline) {
			return protocol.NewError(env.ID, protocol.ErrTimeout, err.Error())
		}
		return protocol.NewError(env.ID, protocol.ErrNotFound, err.Error())
	}
	return protocol.NewResult(env.ID, protocol.TypeFSReadResult, result)
}

func (a *Agent) handleFSWrite(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommand[protocol.FSParams](env)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}
	data, err := decodeBase64(params.Data)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, "invalid base64 data")
	}
	// P8-05: a 0-byte write is never an "ok". Creating an empty file is still
	// possible, but only as an explicit request: expected_size=0.
	if len(data) == 0 {
		if params.ExpectedSize == nil {
			return protocol.NewError(env.ID, protocol.ErrFSWriteEmptyData,
				fmt.Sprintf("fs-write %s: refusing 0-byte write (no expected_size supplied)", params.Path))
		}
		if *params.ExpectedSize != 0 {
			return protocol.NewError(env.ID, protocol.ErrFSWriteSizeMismatch,
				fmt.Sprintf("fs-write %s: empty data contradicts expected_size=%d", params.Path, *params.ExpectedSize))
		}
	}
	result, err := boundedFS(fsDeadline(params, fsWriteDeadline), "fs-write", params.Path, func() (protocol.FSWriteResult, error) {
		return a.writeFileChunk(params, data)
	})
	if err != nil {
		if errors.Is(err, ErrFSDeadline) {
			return protocol.NewError(env.ID, protocol.ErrTimeout, err.Error())
		}
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}
	if len(data) > 0 && result.Written == 0 {
		return protocol.NewError(env.ID, protocol.ErrFSWriteEmpty,
			fmt.Sprintf("fs-write %s: wrote 0 of %d bytes", params.Path, len(data)))
	}
	// F5: verification has its own bound. The write call returned first, so the
	// whole-file read-back and the expectation comparisons below used to run
	// with no deadline at all — a volume that stalls on read-back after
	// accepting the write blocked the handler with no TIMEOUT, which is the
	// exact condition the deadline exists for.
	dig, statErr := boundedFS(fsDeadline(params, fsWriteDeadline), "fs-write-verify", params.Path, func() (fileDigestResult, error) {
		size, sum, err := fileDigest(params.Path)
		return fileDigestResult{Size: size, Sum: sum}, err
	})
	if statErr != nil {
		if errors.Is(statErr, ErrFSDeadline) {
			return protocol.NewError(env.ID, protocol.ErrTimeout, statErr.Error())
		}
		return protocol.NewError(env.ID, protocol.ErrInternal,
			fmt.Sprintf("fs-write %s: post-write verification failed: %v", params.Path, statErr))
	}
	size, sum := dig.Size, dig.Sum
	result.Size = size
	result.Sha256 = sum
	result.Sha256Scope = "file" // sha256 is the whole file on disk after the write
	result.Verified = true
	if params.ExpectedSize != nil && size != *params.ExpectedSize {
		return protocol.NewError(env.ID, protocol.ErrFSWriteSizeMismatch,
			fmt.Sprintf("fs-write %s: expected %d bytes, %d on disk", params.Path, *params.ExpectedSize, size))
	}
	if params.ExpectedSHA256 != "" && !strings.EqualFold(params.ExpectedSHA256, sum) {
		return protocol.NewError(env.ID, protocol.ErrFSWriteDigestMismatch,
			fmt.Sprintf("fs-write %s: expected sha256 %s, %s on disk", params.Path, params.ExpectedSHA256, sum))
	}
	return protocol.NewResult(env.ID, protocol.TypeFileSaveResult, result)
}

// writeFileChunk performs the actual write. Resumable chunked write:
// - offset=0, mode="create": truncate/create file, write from beginning (first chunk)
// - offset>0: open existing file (no truncate), seek to offset, write (subsequent chunk)
// - offset=0, mode!="create": overwrite from beginning without truncate (retransmit first chunk)
func (a *Agent) writeFileChunk(params protocol.FSParams, data []byte) (protocol.FSWriteResult, error) {
	if params.Offset > 0 || (params.Offset == 0 && params.Mode != "" && params.Mode != "create") {
		// Subsequent chunk: open existing file without truncating
		f, err := os.OpenFile(params.Path, os.O_WRONLY, 0644)
		if err != nil {
			// File doesn't exist yet — create it
			f, err = os.OpenFile(params.Path, os.O_WRONLY|os.O_CREATE, 0644)
			if err != nil {
				return protocol.FSWriteResult{}, err
			}
		}
		defer f.Close()
		if _, err = f.Seek(int64(params.Offset), 0); err != nil {
			return protocol.FSWriteResult{}, err
		}
		if _, err = f.Write(data); err != nil {
			return protocol.FSWriteResult{}, err
		}
		return protocol.FSWriteResult{Path: params.Path, Written: len(data)}, nil
	}
	// First chunk (offset=0, no mode or mode="create"): create/truncate and write
	return a.plat.WriteFile(params.Path, data, params.Mode)
}

func (a *Agent) handleFSDelete(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommand[protocol.FSParams](env)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}
	result, err := boundedFS(fsDeadline(params, fsMetaDeadline), "fs-delete", params.Path, func() (protocol.FSDeleteResult, error) {
		return a.plat.DeleteFile(params.Path)
	})
	if err != nil {
		if errors.Is(err, ErrFSDeadline) {
			return protocol.NewError(env.ID, protocol.ErrTimeout, err.Error())
		}
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}
	return protocol.NewResult(env.ID, protocol.TypeFileRemoveResult, result)
}

func (a *Agent) handleFSMove(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommand[protocol.FSParams](env)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}
	result, err := boundedFS(fsDeadline(params, fsMetaDeadline), "fs-move", params.From+" -> "+params.To, func() (protocol.FSMoveResult, error) {
		return a.plat.MoveFile(params.From, params.To)
	})
	if err != nil {
		if errors.Is(err, ErrFSDeadline) {
			return protocol.NewError(env.ID, protocol.ErrTimeout, err.Error())
		}
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}
	return protocol.NewResult(env.ID, protocol.TypeFSMoveResult, result)
}

func (a *Agent) handleFSMkdir(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommand[protocol.FSParams](env)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}
	result, err := boundedFS(fsDeadline(params, fsMetaDeadline), "fs-mkdir", params.Path, func() (protocol.FSMkdirResult, error) {
		return a.plat.Mkdir(params.Path)
	})
	if err != nil {
		if errors.Is(err, ErrFSDeadline) {
			return protocol.NewError(env.ID, protocol.ErrTimeout, err.Error())
		}
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}
	return protocol.NewResult(env.ID, protocol.TypeFSMkdirResult, result)
}

// handleFSHash computes SHA256 of a file on the agent.
// Returns {path, sha256, size} — used for verifying chunked uploads.
func (a *Agent) handleFSHash(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommand[protocol.FSParams](env)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}
	// A3: hashing a large file on a stalled volume blocks exactly like
	// fs-list. The handle is opened *and closed* inside the bounded call: a
	// deadline that returned while another goroutine still reads would close
	// the file under it.
	result, err := boundedFS(fsDeadline(params, fsHashDeadline), "fs-hash", params.Path, func() (protocol.FSHashResult, error) {
		f, err := os.Open(params.Path)
		if err != nil {
			return protocol.FSHashResult{}, err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return protocol.FSHashResult{}, err
		}
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return protocol.FSHashResult{}, err
		}
		return protocol.FSHashResult{
			Path:        params.Path,
			Hash:        fmt.Sprintf("%x", h.Sum(nil)),
			Size:        info.Size(),
			Sha256Scope: "file",
		}, nil
	})
	if err != nil {
		if errors.Is(err, ErrFSDeadline) {
			return protocol.NewError(env.ID, protocol.ErrTimeout, err.Error())
		}
		if os.IsNotExist(err) {
			return protocol.NewError(env.ID, protocol.ErrNotFound, err.Error())
		}
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}
	return protocol.NewResult(env.ID, protocol.TypeFSHashResult, result)
}

// P14-03: handleCapture moved to capture.go — the §10 shape (hash of decoded
// pixels, absent data/size_bytes under hash_only, changed:null without a
// baseline) is a body of rules, not three lines of dispatch.


func (a *Agent) handleDisplayInfo(env protocol.Envelope) protocol.Envelope {
	return protocol.NewResult(env.ID, protocol.TypeDisplayInfoResult, a.plat.ScreenInfo())
}

func (a *Agent) handleClick(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommandStrict[protocol.InputParams](env, protocol.AllowPointerClick)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}
	count := params.Count
	if count == 0 {
		count = 1
	}
	if count < 1 || count > maxClickCount {
		return *clickRefusalForCount(env, count)
	}
	// §9.1: the window-relative resolution and the occlusion hit-test run
	// BEFORE the injection, on the same display the tree reads.
	pt, refusal := a.resolveInputPoint(env, "pointer-click", params.Context, params.Window, params.X, params.Y)
	if refusal != nil {
		return *refusal
	}
	if pt.Probe != nil {
		defer pt.Probe.Close()
	}
	// §9.2: one gesture, one call. A count>1 click is NOT a loop of the
	// single-click path — the app decides on the gap between the presses.
	if inj, ok := injectorFor(&pt.inputTarget); ok {
		win := a.effectiveClickWindow()
		gap, gerr := emitClickGesture(inj, pt.X, pt.Y, params.Button, count, win)
		if gerr != nil {
			return clickBackendError(env, gerr)
		}
		return protocol.NewResult(env.ID, protocol.TypePointerClickResult, protocol.ClickResult{
			Success: true, Count: count, GapMS: gapMillis(gap),
		})
	}
	if count == 1 {
		if err := a.plat.Click(pt.X, pt.Y, params.Button); err != nil {
			return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
		}
		return protocol.NewResult(env.ID, protocol.TypePointerClickResult, protocol.ClickResult{Success: true, Count: 1})
	}
	// A multi-click needs a backend that can time the gesture. A display that
	// was not named above can still be opened for it: the point stays absolute,
	// and nothing is placed or hit-tested.
	if pt.Probe == nil {
		if probe, ref, oref := a.openContextDisplay(env, "pointer-click", params.Context, params.Window); oref == nil {
			defer probe.Close()
			if inj, ok := probe.(inputSeam); ok {
				gap, gerr := emitClickGesture(inj, pt.X, pt.Y, params.Button, count, a.effectiveClickWindow())
				if gerr != nil {
					return clickBackendError(env, gerr)
				}
				_ = ref
				return protocol.NewResult(env.ID, protocol.TypePointerClickResult, protocol.ClickResult{
					Success: true, Count: count, GapMS: gapMillis(gap),
				})
			}
		}
	}
	if mc, ok := a.plat.(multiClicker); ok {
		// The platform layer times the gesture itself, so it is handed the
		// spacing derived from the same effective window rather than a fixed
		// 120 ms: a seat configured at 200 ms must still see one double-click.
		ms, merr := mc.ClickCount(pt.X, pt.Y, params.Button, count, clickSpacing(a.effectiveClickWindow()))
		if merr != nil {
			return clickBackendError(env, merr)
		}
		return protocol.NewResult(env.ID, protocol.TypePointerClickResult, protocol.ClickResult{
			Success: true, Count: count, GapMS: ms,
		})
	}
	return multiClickUnsupported(env, "count>1 needs a multi-click backend and this platform has none")
}

func (a *Agent) handleType(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommandStrict[protocol.InputParams](env, protocol.AllowTextInput)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}
	enc, ok := textEncodingOK(params.Encoding)
	if !ok {
		envl := protocol.NewErrorDetail(env.ID, protocol.ErrInvalidParams,
			fmt.Sprintf("text-input: encoding %q is not one this build defines", params.Encoding),
			map[string]interface{}{
				"reason":  protocol.ReasonOutOfRange,
				"field":   "encoding",
				"allowed": []string{"utf8", "utf16"},
				"ran":     false,
			})
		return envl
	}
	target, refusal := a.resolveInputTarget(env, "text-input", params.Context, params.Window)
	if refusal != nil {
		return *refusal
	}
	if target.Probe != nil {
		defer target.Probe.Close()
	}
	if ref := focusGate(env, "text-input", target, params.Focus); ref != nil {
		return *ref
	}
	// A text-input that names no window still has to type through the display:
	// no window means no geometry to resolve, not no display to type on.
	if target.Probe == nil {
		if probe, _, oref := a.openContextDisplay(env, "text-input", params.Context, params.Window); oref == nil {
			defer probe.Close()
			target = &inputTarget{Probe: probe}
		}
	}
	// §9.4 utf16 on Windows: the caller's code units are the wire format, so
	// they are emitted verbatim and nothing is transcoded.
	if runtime.GOOS == "windows" {
		if ut, ok := a.plat.(unicodeTyper); ok {
			units := utf16.Encode([]rune(params.Text))
			if terr := ut.TypeTextUnits(units); terr != nil {
				return protocol.NewError(env.ID, protocol.ErrInternal, terr.Error())
			}
			return protocol.NewResult(env.ID, protocol.TypeTextInputResult, protocol.TextResult{
				Success: true,
				Detail:  textDetail(env, enc, len(units)),
			})
		}
	}
	if inj, ok := injectorFor(target); ok {
		syms, kc, perr := planText(inj, params.Text)
		if perr != nil {
			return codepointUnsupported(env, params.Text, perr.Error())
		}
		if terr := emitText(inj, syms, kc); terr != nil {
			if errors.Is(terr, errNoSpareKeycode) || errors.Is(terr, errXTestMissing) {
				return codepointUnsupported(env, params.Text, terr.Error())
			}
			return protocol.NewError(env.ID, protocol.ErrInternal, terr.Error())
		}
		return protocol.NewResult(env.ID, protocol.TypeTextInputResult, protocol.TextResult{
			Success: true,
			Detail:  textDetail(env, enc, len(syms)),
		})
	}
	if err := a.plat.TypeText(params.Text); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}
	return protocol.NewResult(env.ID, protocol.TypeTextInputResult, protocol.TextResult{
		Success: true,
		Detail:  textDetail(env, enc, len([]rune(params.Text))),
	})
}

func (a *Agent) handleKey(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommandStrict[protocol.InputParams](env, protocol.AllowKeyPress)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}
	target, refusal := a.resolveInputTarget(env, "key-press", params.Context, params.Window)
	if refusal != nil {
		return *refusal
	}
	if target.Probe != nil {
		defer target.Probe.Close()
	}
	if ref := focusGate(env, "key-press", target, params.Focus); ref != nil {
		return *ref
	}
	if err := a.plat.KeyPress(params.Key); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}
	return protocol.NewResult(env.ID, protocol.TypeKeyPressResult, protocol.InputResult{Success: true})
}

func (a *Agent) handleKeyCombo(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommandStrict[protocol.InputParams](env, protocol.AllowKeyCombo)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}
	target, refusal := a.resolveInputTarget(env, "key-combo", params.Context, params.Window)
	if refusal != nil {
		return *refusal
	}
	if target.Probe != nil {
		defer target.Probe.Close()
	}
	if ref := focusGate(env, "key-combo", target, params.Focus); ref != nil {
		return *ref
	}
	if err := a.plat.KeyCombo(params.Keys); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}
	return protocol.NewResult(env.ID, protocol.TypeKeyComboResult, protocol.InputResult{Success: true})
}

func (a *Agent) handleHealth(env protocol.Envelope) protocol.Envelope {
	hr := a.plat.Health(a.cfg.Mode)
	hr.Capabilities = a.cfg.Capabilities
	return protocol.NewResult(env.ID, protocol.TypeHealthResult, hr)
}

func (a *Agent) handleTaskList(env protocol.Envelope) protocol.Envelope {
	procs, err := a.plat.ProcessList()
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}
	return protocol.NewResult(env.ID, protocol.TypeTaskListResult, protocol.ProcessListResult{Processes: procs})
}

func (a *Agent) handleTaskStop(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommand[protocol.TaskStopParams](env)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}

	// Sandbox check: in sandboxed/standard mode, only kill PIDs this agent started.
	// Bypass flag (user-approved override) skips this check.
	if !env.Bypass && (a.cfg.Permissions == "sandboxed" || a.cfg.Permissions == "standard") {
		a.spawnedPIDMu.Lock()
		allowed := a.spawnedPIDs[params.PID]
		a.spawnedPIDMu.Unlock()
		if !allowed {
			return protocol.NewError(env.ID, "permission_denied",
				fmt.Sprintf("cannot kill PID %d: process was not started by this agent (sandboxed mode protects system processes)", params.PID))
		}
	}

	if err := a.plat.ProcessKill(params.PID, params.Signal); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}

	// Remove from tracked PIDs
	a.spawnedPIDMu.Lock()
	delete(a.spawnedPIDs, params.PID)
	a.spawnedPIDMu.Unlock()

	return protocol.NewResult(env.ID, protocol.TypeTaskStopResult, protocol.TaskStopResult{Killed: true, PID: params.PID})
}

func (a *Agent) handleOpenLink(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommand[protocol.OpenURLParams](env)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}
	if err := a.plat.OpenURL(params.URL); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}
	return protocol.NewResult(env.ID, protocol.TypeOpenLinkResult, protocol.InputResult{Success: true})
}

func (a *Agent) handleNotify(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommand[protocol.NotifyParams](env)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}
	if err := a.plat.Notify(params.Title, params.Body, params.Icon); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}
	return protocol.NewResult(env.ID, protocol.TypeNotifyResult, protocol.InputResult{Success: true})
}

func (a *Agent) handleClipboardRead(env protocol.Envelope) protocol.Envelope {
	text, err := a.plat.ClipboardGet()
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}
	return protocol.NewResult(env.ID, protocol.TypeClipboardReadResult, protocol.ClipboardResult{Text: text})
}

func (a *Agent) handleClipboardWrite(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommand[protocol.ClipboardWriteParams](env)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}
	if err := a.plat.ClipboardSet(params.Text); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}
	return protocol.NewResult(env.ID, protocol.TypeClipboardWriteResult, protocol.InputResult{Success: true})
}

func (a *Agent) handleTokenRotate(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommand[protocol.TokenRotateParams](env)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}
	a.mu.Lock()
	a.cfg.Token = params.NewToken
	// Track the expiry so the proactive-refresh loop can request a new token
	// before this one expires. A zero Expiry means the server did not set one.
	a.tokenExpiry = params.Expiry
	a.mu.Unlock()

	// Persist the new token to disk so reconnects use the rotated token.
	if a.cfg.TokenFile != "" {
		if err := a.persistToken(params.NewToken); err != nil {
			log.Printf("Warning: could not persist token: %v", err)
		}
	}

	log.Printf("Token rotated successfully.")
	return protocol.NewResult(env.ID, protocol.TypeAuthRefreshResult, protocol.TokenRotateResult{
		Rotated:  true,
		NewToken: params.NewToken,
	})
}

// persistToken writes the token to the configured TokenFile with 0600 perms.
// It is called by handleTokenRotate after a successful rotation so reconnects
// pick up the new token automatically.
func (a *Agent) persistToken(token string) error {
	if a.cfg.TokenFile == "" {
		return nil
	}
	return os.WriteFile(a.cfg.TokenFile, []byte(token), 0600)
}

// LoadPersistedToken reads a previously persisted token from TokenFile. It
// returns an empty string (and no error) if the file does not exist or is
// empty. Used at startup to resume with the most recent rotated token.
func LoadPersistedToken(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

func mustMarshal(v interface{}) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

func decodeBase64(s string) ([]byte, error) {
	// Simple decoder inline
	if s == "" {
		return nil, nil
	}
	var result []byte
	table := map[byte]byte{}
	for i := 0; i < 64; i++ {
		c := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"[i]
		table[c] = byte(i)
	}
	data := []byte(s)
	// Strip padding
	for len(data) > 0 && data[len(data)-1] == '=' {
		data = data[:len(data)-1]
	}
	for i := 0; i < len(data); i += 4 {
		if i+1 >= len(data) {
			break
		}
		b0 := table[data[i]]
		b1 := table[data[i+1]]
		b2 := byte(0)
		b3 := byte(0)
		if i+2 < len(data) {
			b2 = table[data[i+2]]
		}
		if i+3 < len(data) {
			b3 = table[data[i+3]]
		}
		result = append(result, (b0<<2)|(b1>>4))
		if i+2 < len(data) {
			result = append(result, ((b1&0x0f)<<4)|(b2>>2))
		}
		if i+3 < len(data) {
			result = append(result, ((b2&0x03)<<6)|b3)
		}
	}
	return result, nil
}

// SendPrompt sends an exec command to the connected server and displays results.
func (a *Agent) SendPrompt(prompt string) {
	a.mu.Lock()
	conn := a.conn
	a.mu.Unlock()
	if conn == nil {
		log.Printf("not connected, cannot send prompt")
		return
	}
	env := protocol.Envelope{
		ID:   fmt.Sprintf("prompt-%d", time.Now().UnixMilli()),
		Type: protocol.TypeExec,
		Params: mustMarshal(protocol.ExecParams{
			Command: prompt,
			Timeout: defaultTimeout,
		}),
	}
	if err := a.writeMessage(conn, env); err != nil {
		log.Printf("send prompt error: %v", err)
	}
}

func getOS() string   { return runtime.GOOS }
func getArch() string { return runtime.GOARCH }

// --- Phase 4: Dynamic mode switching ---

// modeManagerRef is a lightweight reference to a mode manager that allows
// the agent to start/stop modes at runtime. This avoids importing the modes
// package directly (which would create a circular dependency).
type modeManagerRef struct {
	startFn func(mode string, cfg json.RawMessage) error
	stopFn  func(mode string) error
	statusFn func() map[string]bool
}

// SetModeManager connects the agent to a mode manager for remote control.
func (a *Agent) SetModeManager(startFn func(string, json.RawMessage) error, stopFn func(string) error, statusFn func() map[string]bool) {
	a.modeMgr = &modeManagerRef{startFn: startFn, stopFn: stopFn, statusFn: statusFn}
}

// handleModeControl processes mode_control messages from the server.
// It starts or stops a mode (serve/connect/relay) on the agent at runtime.
func (a *Agent) handleModeControl(env protocol.Envelope) protocol.Envelope {
	if a.modeMgr == nil {
		return protocol.NewError(env.ID, "mode_manager_unavailable",
			"this agent does not have a mode manager (not running in supervisor mode)")
	}

	var params struct {
		Action string          `json:"action"`
		Mode   string          `json:"mode"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(env.Params, &params); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}

	switch params.Action {
	case "start":
		if err := a.modeMgr.startFn(params.Mode, params.Config); err != nil {
			return protocol.NewResult(env.ID, protocol.TypeModeControlResult, struct {
				Mode   string `json:"mode"`
				Action string `json:"action"`
				Status string `json:"status"`
				Error  string `json:"error"`
			}{params.Mode, "start", "error", err.Error()})
		}
		return protocol.NewResult(env.ID, protocol.TypeModeControlResult, struct {
			Mode   string `json:"mode"`
			Action string `json:"action"`
			Status string `json:"status"`
		}{params.Mode, "start", "running"})

	case "stop":
		if err := a.modeMgr.stopFn(params.Mode); err != nil {
			return protocol.NewResult(env.ID, protocol.TypeModeControlResult, struct {
				Mode   string `json:"mode"`
				Action string `json:"action"`
				Status string `json:"status"`
				Error  string `json:"error"`
			}{params.Mode, "stop", "error", err.Error()})
		}
		return protocol.NewResult(env.ID, protocol.TypeModeControlResult, struct {
			Mode   string `json:"mode"`
			Action string `json:"action"`
			Status string `json:"status"`
		}{params.Mode, "stop", "stopped"})

	default:
		return protocol.NewError(env.ID, protocol.ErrInvalidParams,
			fmt.Sprintf("unknown action: %s (must be 'start' or 'stop')", params.Action))
	}
}

// handleForwardPolicy processes forward_policy messages from the server.
// This is used for server-as-relay selective forwarding (Step 11).
// The policy determines whether a local agent's traffic is forwarded
// upstream through the relay ("relay") or kept local only ("local").
func (a *Agent) handleForwardPolicy(env protocol.Envelope) protocol.Envelope {
	var params struct {
		Agent  string `json:"agent"`
		Action string `json:"action"` // "relay" or "local"
	}
	if err := json.Unmarshal(env.Params, &params); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}

	if params.Action != "relay" && params.Action != "local" {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams,
			fmt.Sprintf("action must be 'relay' or 'local', got: %s", params.Action))
	}

	// Store the policy. The serve mode's server checks this when deciding
	// whether to forward agent traffic through the relay.
	a.forwardMu.Lock()
	if a.forwardPolicies == nil {
		a.forwardPolicies = make(map[string]string)
	}
	a.forwardPolicies[params.Agent] = params.Action
	a.forwardMu.Unlock()

	log.Printf("[agent] forward_policy applied: agent=%s action=%s", params.Agent, params.Action)

	return protocol.NewResult(env.ID, protocol.TypeModeControlResult, struct {
		Agent  string `json:"agent"`
		Action string `json:"action"`
		Status string `json:"status"`
	}{params.Agent, params.Action, "applied"})
}

// SendModeStatus sends the current mode status to the server.
// Called on connect and on mode change.
func (a *Agent) SendModeStatus() {
	if a.modeMgr == nil {
		return
	}
	a.mu.Lock()
	conn := a.conn
	a.mu.Unlock()
	if conn == nil {
		return
	}

	status := a.modeMgr.statusFn()
	env := protocol.Envelope{
		ID:     fmt.Sprintf("mode-status-%d", time.Now().UnixMilli()),
		Type:   protocol.TypeModeStatus,
		Result: mustMarshal(status),
	}
	if err := a.writeMessage(conn, env); err != nil {
		log.Printf("[agent] send mode_status error: %v", err)
	}
}

// handleReconfigure processes a reconfigure message from the server.
// The agent saves the new server URL to its config file (if known) and
// reconnects to the new server address. This enables mass migration of
// all agents to a new server IP without manual reconfiguration.
func (a *Agent) handleReconfigure(env protocol.Envelope) protocol.Envelope {
	var params struct {
		ServerURL string `json:"server_url"` // new WebSocket URL
		Token     string `json:"token,omitempty"` // new auth token (optional, keep existing if empty)
		SavePath  string `json:"save_path,omitempty"` // path to save updated config
	}
	if err := json.Unmarshal(env.Params, &params); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}

	if params.ServerURL == "" {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, "server_url is required")
	}

	// Ensure /ws path
	newURL := params.ServerURL
	if !strings.Contains(newURL, "/ws") {
		newURL = strings.TrimRight(newURL, "/") + "/ws"
	}

	// Update token if provided
	newToken := a.cfg.Token
	if params.Token != "" {
		newToken = params.Token
	}

	log.Printf("[agent] reconfigure: %s → %s", a.cfg.URL, newURL)

	// Try to save updated config to file (if path provided or config file known)
	savePath := params.SavePath
	if savePath == "" && a.cfg.ConfigPath != "" {
		// Use the config file path the agent was started with
		if _, err := os.Stat(a.cfg.ConfigPath); err == nil {
			savePath = a.cfg.ConfigPath
		}
	}
	if savePath == "" {
		// Try common config file names in working directory
		for _, name := range []string{"probe.json", "probe-client.json"} {
			if _, err := os.Stat(name); err == nil {
				savePath = name
				break
			}
		}
	}

	if savePath != "" {
		// Read existing config, update server field, write back
		cfgData, err := os.ReadFile(savePath)
		if err == nil {
			var cfgMap map[string]interface{}
			if err := json.Unmarshal(cfgData, &cfgMap); err == nil {
				// Update server URL — handle both flat and structured formats
				if _, hasClient := cfgMap["client"]; hasClient {
					if clientMap, ok := cfgMap["client"].(map[string]interface{}); ok {
						clientMap["server"] = newURL
						if params.Token != "" {
							clientMap["token"] = newToken
						}
					}
				} else {
					// Legacy flat format
					cfgMap["server"] = newURL
					if params.Token != "" {
						cfgMap["token"] = newToken
					}
				}
				updatedData, _ := json.MarshalIndent(cfgMap, "", "  ")
				if err := os.WriteFile(savePath, updatedData, 0644); err != nil {
					log.Printf("[agent] reconfigure: failed to save config: %v", err)
				} else {
					log.Printf("[agent] reconfigure: config saved to %s", savePath)
				}
			}
		}
	}

	// Update in-memory config
	a.cfg.URL = newURL
	a.cfg.Token = newToken

	// Force reconnect by closing current connection
	// The agent's Run() loop will reconnect with the new URL
	go func() {
		time.Sleep(500 * time.Millisecond) // give response time to send
		a.mu.Lock()
		if a.conn != nil {
			a.conn.Close()
		}
		a.mu.Unlock()
		log.Printf("[agent] reconfigure: closing connection for reconnect to %s", newURL)
	}()

	return protocol.NewResult(env.ID, "reconfigure", struct {
		OldServer string `json:"old_server"`
		NewServer string `json:"new_server"`
		Status    string `json:"status"`
		SavedTo   string `json:"saved_to,omitempty"`
	}{a.cfg.URL, newURL, "reconnecting", savePath})
}
