package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// ---------------------------------------------------------------------------
// Enrollment tokens
// ---------------------------------------------------------------------------

// EnrollmentToken represents a pre-shared token that allows an agent to enroll.
type EnrollmentToken struct {
	Token     string    `json:"token"`
	AgentName string    `json:"agent_name"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Used      bool      `json:"used"`
}

// EnrollmentManager manages enrollment tokens and persists them to
// enrollment.json. It uses a crypto/rand-based token generator.
type EnrollmentManager struct {
	mu       sync.Mutex
	tokens   map[string]*EnrollmentToken // token -> EnrollmentToken
	savePath string
}

// NewEnrollmentManager creates a new EnrollmentManager. If savePath is
// non-empty, existing tokens are loaded from disk.
func NewEnrollmentManager(savePath string) *EnrollmentManager {
	em := &EnrollmentManager{
		tokens:   make(map[string]*EnrollmentToken),
		savePath: savePath,
	}
	em.load()
	return em
}

// CreateToken generates a new enrollment token for the given agent name
// with the specified TTL. Returns the created token.
func (em *EnrollmentManager) CreateToken(agentName string, ttl time.Duration) (*EnrollmentToken, error) {
	token := generateEnrollmentToken()
	now := time.Now().UTC()
	et := &EnrollmentToken{
		Token:     token,
		AgentName: agentName,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
		Used:      false,
	}
	em.mu.Lock()
	em.tokens[token] = et
	em.save()
	em.mu.Unlock()
	return et, nil
}

// ValidateToken checks whether the given token is valid (exists, not
// expired, not used). Returns the EnrollmentToken on success.
func (em *EnrollmentManager) ValidateToken(token string) (*EnrollmentToken, error) {
	em.mu.Lock()
	defer em.mu.Unlock()
	et, ok := em.tokens[token]
	if !ok {
		return nil, fmt.Errorf("enrollment token not found")
	}
	if et.Used {
		return nil, fmt.Errorf("enrollment token already used")
	}
	if time.Now().UTC().After(et.ExpiresAt) {
		return nil, fmt.Errorf("enrollment token expired")
	}
	return et, nil
}

// MarkUsed marks a token as used (one-time use).
func (em *EnrollmentManager) MarkUsed(token string) {
	em.mu.Lock()
	defer em.mu.Unlock()
	if et, ok := em.tokens[token]; ok {
		et.Used = true
		em.save()
	}
}

// ListTokens returns all enrollment tokens.
func (em *EnrollmentManager) ListTokens() []EnrollmentToken {
	em.mu.Lock()
	defer em.mu.Unlock()
	result := make([]EnrollmentToken, 0, len(em.tokens))
	for _, et := range em.tokens {
		result = append(result, *et)
	}
	return result
}

// RevokeToken removes a token, preventing its use.
func (em *EnrollmentManager) RevokeToken(token string) error {
	em.mu.Lock()
	defer em.mu.Unlock()
	if _, ok := em.tokens[token]; !ok {
		return fmt.Errorf("enrollment token not found")
	}
	delete(em.tokens, token)
	em.save()
	return nil
}

// --- persistence ---

func (em *EnrollmentManager) save() {
	if em.savePath == "" {
		return
	}
	ensureDir(em.savePath)
	data, err := json.MarshalIndent(em.tokens, "", "  ")
	if err != nil {
		log.Printf("[enrollment] save marshal error: %v", err)
		return
	}
	if err := os.WriteFile(em.savePath, data, 0644); err != nil {
		log.Printf("[enrollment] save write error: %v", err)
	}
}

func (em *EnrollmentManager) load() {
	if em.savePath == "" {
		return
	}
	data, err := os.ReadFile(em.savePath)
	if err != nil {
		return // file doesn't exist yet
	}
	var tokens map[string]*EnrollmentToken
	if err := json.Unmarshal(data, &tokens); err != nil {
		log.Printf("[enrollment] load unmarshal error: %v", err)
		return
	}
	em.tokens = tokens
}

func generateEnrollmentToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err == nil {
		return fmt.Sprintf("enr-%x", b[:])
	}
	return fmt.Sprintf("enr-%d", time.Now().UnixNano())
}

// ---------------------------------------------------------------------------
// CA + per-agent certificate generation
// ---------------------------------------------------------------------------

// CAManager manages a self-signed CA used to sign per-agent certificates
// for mTLS. The CA key pair is generated on first run and persisted to disk.
type CAManager struct {
	caCert    *x509.Certificate
	caKey     *ecdsa.PrivateKey
	caCertPEM []byte
	caDir     string

	mu sync.Mutex
}

// NewCAManager creates a CAManager. The CA is loaded from caDir if it
// exists, otherwise a new CA is generated and saved.
func NewCAManager(caDir string) *CAManager {
	cm := &CAManager{caDir: caDir}
	if err := cm.loadOrGenerate(); err != nil {
		log.Printf("[ca] failed to load/generate CA: %v", err)
	}
	return cm
}

// CACertPEM returns the PEM-encoded CA certificate.
func (cm *CAManager) CACertPEM() []byte {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.caCertPEM
}

// SignAgentCert generates a per-agent certificate signed by the CA.
// Returns the agent cert PEM, agent private key PEM, and server CA PEM.
func (cm *CAManager) SignAgentCert(agentID string) (certPEM, keyPEM, caPEM []byte, err error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.caCert == nil || cm.caKey == nil {
		return nil, nil, nil, fmt.Errorf("CA not initialized")
	}

	// Generate agent key pair.
	agentKey, err := ecdsa.GenerateKey(curveForCA(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generate agent key: %w", err)
	}

	// Create agent certificate template.
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   agentID,
			Organization: []string{"PROBE"},
		},
		NotBefore:             now.Add(-1 * time.Minute),
		NotAfter:              now.Add(365 * 24 * time.Hour), // 1 year
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid:  true,
		IsCA:                  false,
	}

	agentCertDER, err := x509.CreateCertificate(rand.Reader, tmpl, cm.caCert, &agentKey.PublicKey, cm.caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("sign agent cert: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: agentCertDER})
	keyDER, err := x509.MarshalECPrivateKey(agentKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal agent key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	caPEM = cm.caCertPEM

	return certPEM, keyPEM, caPEM, nil
}

// loadOrGenerate attempts to load the CA from disk; if not found, it
// generates a new self-signed CA and persists it.
func (cm *CAManager) loadOrGenerate() error {
	if cm.caDir == "" {
		return cm.generateCA("")
	}

	certPath := cm.caDir + "/ca.crt"
	keyPath := cm.caDir + "/ca.key"

	// Try loading existing CA.
	certPEM, err := os.ReadFile(certPath)
	if err == nil {
		keyPEM, errK := os.ReadFile(keyPath)
		if errK == nil {
			return cm.loadFromPEM(certPEM, keyPEM)
		}
	}

	// Generate new CA.
	if cm.caDir != "" {
		os.MkdirAll(cm.caDir, 0755)
	}
	return cm.generateCA(cm.caDir)
}

func (cm *CAManager) loadFromPEM(certPEM, keyPEM []byte) error {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return fmt.Errorf("failed to decode CA cert PEM")
	}
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse CA cert: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return fmt.Errorf("failed to decode CA key PEM")
	}
	caKey, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return fmt.Errorf("parse CA key: %w", err)
	}

	cm.caCert = caCert
	cm.caKey = caKey
	cm.caCertPEM = certPEM
	return nil
}

func (cm *CAManager) generateCA(persistDir string) error {
	// Generate CA key pair.
	caKey, err := ecdsa.GenerateKey(curveForCA(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate CA key: %w", err)
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "PROBE-CA",
			Organization: []string{"PROBE"},
		},
		NotBefore:             now.Add(-1 * time.Minute),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour), // 10 years
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageCRLSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid:  true,
		IsCA:                  true,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("create CA cert: %w", err)
	}

	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		return fmt.Errorf("parse CA cert: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})
	keyDER, err := x509.MarshalECPrivateKey(caKey)
	if err != nil {
		return fmt.Errorf("marshal CA key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	cm.caCert = caCert
	cm.caKey = caKey
	cm.caCertPEM = certPEM

	// Persist to disk.
	if persistDir != "" {
		os.MkdirAll(persistDir, 0755)
		os.WriteFile(persistDir+"/ca.crt", certPEM, 0644)
		os.WriteFile(persistDir+"/ca.key", keyPEM, 0600)
	}

	return nil
}

// curveForCA returns the elliptic curve used for key generation.
// Uses P-256 (secp256r1) — widely supported and performant.
func curveForCA() elliptic.Curve {
	return elliptic.P256()
}

// ---------------------------------------------------------------------------
// Agent revocation
// ---------------------------------------------------------------------------

// RevokedAgents tracks agent IDs that have been revoked. Revoked agents
// are rejected on WebSocket connect.
type RevokedAgents struct {
	mu      sync.RWMutex
	revoked map[string]bool
}

// NewRevokedAgents creates an empty revocation set.
func NewRevokedAgents() *RevokedAgents {
	return &RevokedAgents{revoked: make(map[string]bool)}
}

// Revoke adds an agent ID to the revocation set.
func (ra *RevokedAgents) Revoke(agentID string) {
	ra.mu.Lock()
	defer ra.mu.Unlock()
	ra.revoked[agentID] = true
}

// IsRevoked returns true if the agent ID has been revoked.
func (ra *RevokedAgents) IsRevoked(agentID string) bool {
	ra.mu.RLock()
	defer ra.mu.RUnlock()
	return ra.revoked[agentID]
}

// List returns all revoked agent IDs.
func (ra *RevokedAgents) List() []string {
	ra.mu.RLock()
	defer ra.mu.RUnlock()
	result := make([]string, 0, len(ra.revoked))
	for id := range ra.revoked {
		result = append(result, id)
	}
	return result
}

// IsAgentRevoked checks whether an agent has been revoked.
func (s *Server) IsAgentRevoked(agentID string) bool {
	if s.revokedAgents == nil {
		return false
	}
	return s.revokedAgents.IsRevoked(agentID)
}

// ---------------------------------------------------------------------------
// Capability-to-command mapping
// ---------------------------------------------------------------------------

// capabilityForCommand maps a protocol message type (command type) to the
// capability name an agent must advertise to handle it. Returns empty
// string for control messages (ping, pong, health) that don't require
// a specific capability.
func capabilityForCommand(msgType string) string {
	switch msgType {
	// Exec / shell
	case protocol.TypeExec, protocol.TypeExecPTY,
		protocol.TypeTaskList, protocol.TypeTaskStop:
		return "exec"

	// Filesystem
	case protocol.TypeFSList, protocol.TypeFSStat, protocol.TypeFSRead,
		protocol.TypeFileSave, protocol.TypeFileRemove,
		protocol.TypeFSMove, protocol.TypeFSMkdir, protocol.TypeFSHash:
		return "filesystem"

	// Screen capture
	case protocol.TypeCapture, protocol.TypeDisplayInfo,
		protocol.TypeDisplayRegion, protocol.TypeStreamBegin, protocol.TypeStreamEnd:
		return "capture"

	// Input
	case protocol.TypePointerClick, protocol.TypeTextInput,
		protocol.TypeKeyPress, protocol.TypeKeyCombo,
		protocol.TypePointerMove, protocol.TypePointerScroll, protocol.TypePointerDrag:
		return "input"

	// Window surface (P13-01 rows 13/14): enumeration/control is a screen-surface
	// primitive, gated on capture until a dedicated `ui` capability exists.
	case protocol.TypeUITree, protocol.TypeUIWindow:
		return "capture"

	// Process control
	case protocol.TypeProcList, protocol.TypeProcKill, protocol.TypeProcStart:
		return "process"

	// Network tunnel
	case protocol.TypeTunnelOpen, protocol.TypeTunnelClose, protocol.TypeTunnelData:
		return "tunnel"

	// MITM
	case protocol.TypeMitmStart, protocol.TypeMitmStop, protocol.TypeMitmData:
		return "mitm"

	// Debugger
	case protocol.TypeDebugAttach, protocol.TypeDebugDetach,
		protocol.TypeDebugReadMem, protocol.TypeDebugModules, protocol.TypeDebugMemQuery:
		return "debug"

	// Desktop
	case protocol.TypeOpenLink, protocol.TypeNotify:
		return "desktop"

	// Clipboard
	case protocol.TypeClipboardRead, protocol.TypeClipboardWrite:
		return "clipboard"

	// Agent self-update
	case protocol.TypeAgentUpdate:
		return "update"

	// Sniffer
	case protocol.TypeSniffStart, protocol.TypeSniffStop:
		return "sniff"

	// Phase 7: New capabilities
	// SOCKS5 proxy
	case protocol.TypeSocks5Start, protocol.TypeSocks5Stop:
		return "socks5"

	// Port forwarding
	case protocol.TypePortForward:
		return "port_forward"

	// Port scanning
	case protocol.TypePortScan:
		return "port_scan"

	// Net connections
	case protocol.TypeNetConnections:
		return "net_info"

	// Autostart
	case protocol.TypeAutostartEnable, protocol.TypeAutostartDisable, protocol.TypeAutostartStatus:
		return "autostart"

	// File search
	case protocol.TypeFileSearch:
		return "file_search"

	// System info
	case protocol.TypeSysInfo:
		return "sysinfo"

	// Control messages — no capability required
	case protocol.TypePing, protocol.TypePong,
		protocol.TypeHealth,
		protocol.TypeTokenRotate, protocol.TypeAuthRefresh,
		protocol.TypeTokenRefresh, protocol.TypeAuthRequest:
		return ""

	default:
		return ""
	}
}

// HasCapability returns true if the agent's capability list includes
// the given capability. An agent with no capabilities advertised (empty
// or nil list) is treated as having all capabilities — this preserves
// backward compatibility with agents that don't advertise capabilities.
func HasCapability(agentCaps []string, required string) bool {
	if required == "" {
		return true
	}
	if len(agentCaps) == 0 {
		return true // backward compat: no caps advertised = all caps
	}
	for _, cap := range agentCaps {
		if cap == required {
			return true
		}
	}
	return false
}

// CheckAgentCapability checks whether the agent has the required
// capability for a given command type. Returns nil if allowed, or an
// error describing the missing capability.
func CheckAgentCapability(agentID string, agentCaps []string, msgType string) error {
	required := capabilityForCommand(msgType)
	if required == "" {
		return nil
	}
	if !HasCapability(agentCaps, required) {
		return fmt.Errorf("agent %s lacks required capability %q for command %q", agentID, required, msgType)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ensureDir creates the parent directory of filePath if it doesn't exist.
func ensureDir(filePath string) {
	dir := ""
	for i := len(filePath) - 1; i >= 0; i-- {
		if filePath[i] == '/' {
			dir = filePath[:i]
			break
		}
	}
	if dir != "" && dir != "/" {
		os.MkdirAll(dir, 0755)
	}
}

// generateUUID generates a UUID v4 using crypto/rand.
func generateUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("agent-%d", time.Now().UnixNano())
	}
	// Set version (4) and variant (RFC 4122) bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ---------------------------------------------------------------------------
// HTTP handlers — enrollment
// ---------------------------------------------------------------------------

// EnrollRequest is the request body for POST /api/v1/enroll.
type EnrollRequest struct {
	Token       string   `json:"token"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// EnrollResponse is the response body for POST /api/v1/enroll.
type EnrollResponse struct {
	AgentID     string   `json:"agent_id"`
	Certificate string   `json:"certificate"`
	PrivateKey  string   `json:"private_key"`
	ServerCA    string   `json:"server_ca"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// handleV1Enroll handles POST /api/v1/enroll — validates an enrollment
// token, generates a unique agent ID, creates a per-agent certificate
// signed by the server CA, and returns the cert/key/CA for mTLS.
func (s *Server) handleV1Enroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use POST")
		return
	}

	var req EnrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON: "+err.Error())
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "enrollment token is required")
		return
	}

	// Validate the enrollment token.
	et, err := s.enrollment.ValidateToken(req.Token)
	if err != nil {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "invalid enrollment token: "+err.Error())
		return
	}

	// Generate unique agent ID (UUID).
	agentID := generateUUID()

	// Create per-agent certificate signed by the server CA.
	certPEM, keyPEM, caPEM, err := s.caManager.SignAgentCert(agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate agent certificate: "+err.Error())
		return
	}

	// Mark token as used (one-time use).
	s.enrollment.MarkUsed(req.Token)

	log.Printf("[enrollment] agent %s enrolled (name=%q, token=%s)", agentID, et.AgentName, req.Token[:12]+"...")

	resp := EnrollResponse{
		AgentID:      agentID,
		Certificate:  string(certPEM),
		PrivateKey:   string(keyPEM),
		ServerCA:     string(caPEM),
		Capabilities: req.Capabilities,
	}
	writeJSON(w, http.StatusCreated, resp)
}

// handleV1CreateEnrollmentToken handles POST /api/v1/enrollment-tokens —
// creates a new enrollment token (admin only).
func (s *Server) handleV1CreateEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use POST")
		return
	}

	if _, ok := s.v1CheckAuth(w, r, "enroll-manage"); !ok {
		return
	}

	var params struct {
		AgentName string `json:"agent_name"`
		TTLHours  int    `json:"ttl_hours"` // 0 = default 24h
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON: "+err.Error())
		return
	}
	if params.AgentName == "" {
		params.AgentName = "unspecified"
	}
	ttl := time.Duration(params.TTLHours) * time.Hour
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}

	et, err := s.enrollment.CreateToken(params.AgentName, ttl)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create token: "+err.Error())
		return
	}

	log.Printf("[enrollment] token created for agent %q (expires in %v)", params.AgentName, ttl)
	writeJSON(w, http.StatusCreated, et)
}

// handleV1ListEnrollmentTokens handles GET /api/v1/enrollment-tokens —
// lists all enrollment tokens (admin only).
func (s *Server) handleV1ListEnrollmentTokens(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "enroll-manage"); !ok {
		return
	}
	tokens := s.enrollment.ListTokens()
	writeJSON(w, http.StatusOK, tokens)
}

// handleV1RevokeAgent handles POST /api/v1/agents/{id}/revoke —
// revokes an agent, preventing it from reconnecting.
func (s *Server) handleV1RevokeAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "revoke"); !ok {
		return
	}

	s.revokedAgents.Revoke(agentID)
	log.Printf("[server] agent %s revoked by operator", agentID)
	writeJSON(w, http.StatusOK, map[string]interface{}{"revoked": agentID})
}

// handleV1ListRevokedAgents handles GET /api/v1/agents/revoked —
// lists all revoked agent IDs.
func (s *Server) handleV1ListRevokedAgents(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "revoke"); !ok {
		return
	}
	revoked := s.revokedAgents.List()
	writeJSON(w, http.StatusOK, map[string]interface{}{"revoked_agents": revoked})
}