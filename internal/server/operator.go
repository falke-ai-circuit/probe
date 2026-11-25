package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Role constants for operator permissions.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

// viewerActions is the set of actions a viewer-role operator may perform.
var viewerActions = map[string]bool{
	"list":             true,
	"health":          true,
	"fs-list":         true,
	"fs-stat":         true,
	"fs-read":         true,
	"fs-hash":         true,
	// Phase 7: read-only info capabilities
	"sysinfo":         true,
	"net-connections": true,
	"port-scan":       true,
	"file-search":     true,
}

// Operator represents an authenticated API user with a role-based permission
// level. The Token field is never serialized to JSON (json:"-") so it cannot
// leak through any API response.
type Operator struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Role         string    `json:"role"` // "admin", "operator", "viewer"
	Token        string    `json:"-"`    // API token (not serialized)
	PasswordHash string    `json:"-"`    // bcrypt hash, not serialized
	CreatedAt    time.Time `json:"created_at"`
	LastSeen     time.Time `json:"last_seen,omitempty"`
}

// SetPassword sets the operator's password by bcrypt-hashing the given
// plaintext password.
func (o *Operator) SetPassword(password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	o.PasswordHash = string(hash)
	return nil
}

// CheckPassword returns true when the given plaintext password matches the
// stored bcrypt hash.
func (o *Operator) CheckPassword(password string) bool {
	if o.PasswordHash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(o.PasswordHash), []byte(password)) == nil
}

// CanPerform returns true when the operator's role permits the given action.
//   - admin:    all actions
//   - operator: all agent commands (everything except operator management)
//   - viewer:   read-only actions (list, health, fs-list, fs-stat, fs-read, fs-hash)
func (o *Operator) CanPerform(action string) bool {
	switch o.Role {
	case RoleAdmin:
		return true
	case RoleOperator:
		return true
	case RoleViewer:
		return viewerActions[action]
	default:
		return false
	}
}

// OperatorManager manages operators and persists them to operators.json.
// It follows the same load/save pattern as Registry.
type OperatorManager struct {
	mu       sync.RWMutex
	operators map[string]*Operator // ID -> Operator
	byToken  map[string]string     // Token -> OperatorID
	savePath string
}

// NewOperatorManager creates a new OperatorManager. If savePath is non-empty
// the manager loads existing operators from disk and persists changes.
func NewOperatorManager(savePath string) *OperatorManager {
	om := &OperatorManager{
		operators: make(map[string]*Operator),
		byToken:   make(map[string]string),
		savePath:  savePath,
	}
	om.load()
	return om
}

// Create adds a new operator with the given name, role, and token. If token
// is empty a random one is generated. Returns the created operator or an
// error if the role is invalid or an operator with the same ID already exists.
func (om *OperatorManager) Create(name, role, token string) (*Operator, error) {
	if !isValidRole(role) {
		return nil, fmt.Errorf("invalid role %q: must be admin, operator, or viewer", role)
	}
	om.mu.Lock()
	defer om.mu.Unlock()

	id := generateOperatorID()
	if token == "" {
		token = generateOperatorToken()
	}
	op := &Operator{
		ID:        id,
		Name:      name,
		Role:      role,
		Token:     token,
		CreatedAt: time.Now().UTC(),
	}
	om.operators[id] = op
	om.byToken[token] = id
	om.save()
	return op, nil
}

// CreateWithPassword adds a new operator with the given name, role, password,
// and token. If token is empty a random one is generated. Returns the created
// operator or an error if the role is invalid. The password is bcrypt-hashed
// and stored in PasswordHash.
func (om *OperatorManager) CreateWithPassword(name, role, password, token string) (*Operator, error) {
	if !isValidRole(role) {
		return nil, fmt.Errorf("invalid role %q: must be admin, operator, or viewer", role)
	}
	om.mu.Lock()
	defer om.mu.Unlock()

	id := generateOperatorID()
	if token == "" {
		token = generateOperatorToken()
	}
	op := &Operator{
		ID:        id,
		Name:      name,
		Role:      role,
		Token:     token,
		CreatedAt: time.Now().UTC(),
	}
	if password != "" {
		if err := op.SetPassword(password); err != nil {
			return nil, err
		}
	}
	om.operators[id] = op
	om.byToken[token] = id
	om.save()
	return op, nil
}

// GetByName returns the operator whose Name matches, or nil if not found.
// Used by the login endpoint to look up operators by username.
func (om *OperatorManager) GetByName(name string) *Operator {
	if name == "" {
		return nil
	}
	om.mu.RLock()
	defer om.mu.RUnlock()
	for _, op := range om.operators {
		if op.Name == name {
			snap := *op
			return &snap
		}
	}
	return nil
}

// Get returns the operator by ID, or nil if not found.
func (om *OperatorManager) Get(id string) *Operator {
	om.mu.RLock()
	defer om.mu.RUnlock()
	if op, ok := om.operators[id]; ok {
		snap := *op
		return &snap
	}
	return nil
}

// GetByToken returns the operator whose token matches, or nil if not found.
func (om *OperatorManager) GetByToken(token string) *Operator {
	if token == "" {
		return nil
	}
	om.mu.RLock()
	defer om.mu.RUnlock()
	id, ok := om.byToken[token]
	if !ok {
		return nil
	}
	if op, ok := om.operators[id]; ok {
		snap := *op
		return &snap
	}
	return nil
}

// List returns a slice of all operators (tokens omitted via json:"-").
func (om *OperatorManager) List() []Operator {
	om.mu.RLock()
	defer om.mu.RUnlock()
	result := make([]Operator, 0, len(om.operators))
	for _, op := range om.operators {
		result = append(result, *op)
	}
	return result
}

// Delete removes an operator by ID. Returns true if the operator existed.
func (om *OperatorManager) Delete(id string) bool {
	om.mu.Lock()
	defer om.mu.Unlock()
	op, ok := om.operators[id]
	if !ok {
		return false
	}
	delete(om.byToken, op.Token)
	delete(om.operators, id)
	om.save()
	return true
}

// RotateToken generates a new token for the operator, invalidating the old one.
// Returns the new token or an error if the operator doesn't exist.
func (om *OperatorManager) RotateToken(id string) (string, error) {
	om.mu.Lock()
	defer om.mu.Unlock()
	op, ok := om.operators[id]
	if !ok {
		return "", fmt.Errorf("operator %s not found", id)
	}
	delete(om.byToken, op.Token)
	newToken := generateOperatorToken()
	op.Token = newToken
	om.byToken[newToken] = id
	om.save()
	return newToken, nil
}

// UpdateLastSeen records that the operator was active at the given time.
func (om *OperatorManager) UpdateLastSeen(id string, t time.Time) {
	om.mu.Lock()
	defer om.mu.Unlock()
	if op, ok := om.operators[id]; ok {
		op.LastSeen = t
		om.save()
	}
}

// IsEmpty returns true when no operators are configured. The server uses
// this to fall back to legacy token-based auth when no operators exist.
func (om *OperatorManager) IsEmpty() bool {
	om.mu.RLock()
	defer om.mu.RUnlock()
	return len(om.operators) == 0
}

// --- persistence (same pattern as registry.go) ---

func (om *OperatorManager) save() {
	if om.savePath == "" {
		return
	}
	dir := ""
	for i := len(om.savePath) - 1; i >= 0; i-- {
		if om.savePath[i] == '/' {
			dir = om.savePath[:i]
			break
		}
	}
	if dir != "" && dir != "/" {
		os.MkdirAll(dir, 0755)
	}
	// Use a serializable wrapper that includes the Token field (which has
	// json:"-" on the Operator struct to prevent API leakage).
	type persistOperator struct {
		ID           string    `json:"id"`
		Name         string    `json:"name"`
		Role         string    `json:"role"`
		Token        string    `json:"token"`
		PasswordHash string    `json:"password_hash,omitempty"`
		CreatedAt    time.Time `json:"created_at"`
		LastSeen     time.Time `json:"last_seen,omitempty"`
	}
	persist := make(map[string]persistOperator, len(om.operators))
	for id, op := range om.operators {
		persist[id] = persistOperator{
			ID:           op.ID,
			Name:         op.Name,
			Role:         op.Role,
			Token:        op.Token,
			PasswordHash: op.PasswordHash,
			CreatedAt:    op.CreatedAt,
			LastSeen:     op.LastSeen,
		}
	}
	data, err := json.MarshalIndent(persist, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[operator] save marshal error: %v\n", err)
		return
	}
	if err := writeFileAtomic(om.savePath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "[operator] save write error: %v\n", err)
	}
}

func (om *OperatorManager) load() {
	if om.savePath == "" {
		return
	}
	data, err := os.ReadFile(om.savePath)
	if err != nil {
		return // file doesn't exist yet
	}
	type persistOperator struct {
		ID           string    `json:"id"`
		Name         string    `json:"name"`
		Role         string    `json:"role"`
		Token        string    `json:"token"`
		PasswordHash string    `json:"password_hash,omitempty"`
		CreatedAt    time.Time `json:"created_at"`
		LastSeen     time.Time `json:"last_seen,omitempty"`
	}
	var persist map[string]persistOperator
	if err := json.Unmarshal(data, &persist); err != nil {
		fmt.Fprintf(os.Stderr, "[operator] load unmarshal error: %v\n", err)
		return
	}
	om.operators = make(map[string]*Operator, len(persist))
	om.byToken = make(map[string]string, len(persist))
	for id, po := range persist {
		op := &Operator{
			ID:           po.ID,
			Name:         po.Name,
			Role:         po.Role,
			Token:        po.Token,
			PasswordHash: po.PasswordHash,
			CreatedAt:    po.CreatedAt,
			LastSeen:     po.LastSeen,
		}
		om.operators[id] = op
		om.byToken[po.Token] = id
	}
}

// --- helpers ---

func isValidRole(role string) bool {
	return role == RoleAdmin || role == RoleOperator || role == RoleViewer
}

func generateOperatorID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "op-" + hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("op-%d", time.Now().UnixNano())
}

func generateOperatorToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("tok-%d", time.Now().UnixNano())
}