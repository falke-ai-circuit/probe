package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"github.com/falke-ai-circuit/probe/internal/protocol"
)


// generateToken produces a fresh opaque token for rotation. It uses crypto/rand
// (stdlib) so the token is unpredictable enough for an auth bearer string.
// On the unlikely chance rand.Read fails it falls back to a time-based token so
// rotation never blocks.
func generateToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("tok-%d", time.Now().UnixNano())
}


// StartTokenRotation launches the background goroutine that proactively
// rotates tokens before they expire. It is a no-op if tokenTTL is zero. Safe
// to call once; started automatically by Start/StartTLS when tokenTTL > 0.
func (s *Server) StartTokenRotation() {
	if s.tokenTTL <= 0 {
		return
	}
	s.tokenWG.Add(1)
	go s.runTokenRotation()
}


// runTokenRotation scans every minute for agents whose token is close to
// expiry and sends them a new token via InitiateTokenRotation. It records the
// new expiry in the tokenExpiry map so the next rotation is scheduled relative
// to the new token. Exits when tokenStop is closed (in Close).
func (s *Server) runTokenRotation() {
	defer s.tokenWG.Done()
	// Check every minute. rotationLeadTime is how far before expiry we rotate.
	const checkInterval = 60 * time.Second
	const rotationLeadTime = 5 * time.Minute
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.tokenStop:
			return
		case <-ticker.C:
			now := time.Now()
			// Snapshot agents needing rotation under lock, then rotate without holding lock
			type rotateTask struct {
				agentID   string
				newToken  string
			}
			var tasks []rotateTask
			s.tokenMu.Lock()
			for agentID, expiry := range s.tokenExpiry {
				if !expiry.IsZero() && now.Add(rotationLeadTime).After(expiry) {
					tasks = append(tasks, rotateTask{agentID: agentID, newToken: generateToken()})
					// Schedule next expiry relative to now
					s.tokenExpiry[agentID] = now.Add(s.tokenTTL)
				}
			}
			s.tokenMu.Unlock()
			// Rotate without holding tokenMu (InitiateTokenRotation locks it internally)
			for _, t := range tasks {
				if err := s.InitiateTokenRotation(t.agentID, t.newToken); err != nil {
					log.Printf("[server] token rotation failed for agent %s: %v", t.agentID, err)
				} else {
					log.Printf("[server] proactively rotated token for agent %s (next expiry in %v)", t.agentID, s.tokenTTL)
				}
			}
			}
			}
			}


			// SetTokenExpiry records the expiry time for an agent's token. Called when an
// agent connects (the server issues a TTL-based expiry) or after a manual
// rotation. A zero expiry means "no expiry tracking".
func (s *Server) SetTokenExpiry(agentID string, expiry time.Time) {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	if expiry.IsZero() {
		delete(s.tokenExpiry, agentID)
		return
	}
	s.tokenExpiry[agentID] = expiry
}


// ClearTokenExpiry removes the expiry tracking for an agent (called on disconnect).
func (s *Server) ClearTokenExpiry(agentID string) {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	delete(s.tokenExpiry, agentID)
}


// SetExtraTokens configures additional accepted auth tokens. This enables
// safe deployment rollover: start a new server with a new primary token plus
// the old token as an extra, so the old agent (still using the old token)
// can connect to the new server until its config is updated.
func (s *Server) SetExtraTokens(extra []string) {
	s.tokens = append(s.tokens, s.token)
	s.tokens = append(s.tokens, extra...)
}


// isValidToken checks whether the given bearer token matches any accepted token.
func (s *Server) isValidToken(authHeader string) bool {
	if s.token == "" {
		return true // no auth configured
	}
	// Check primary token
	if authHeader == "Bearer "+s.token {
		return true
	}
	// Check extra tokens
	for _, t := range s.tokens {
		if authHeader == "Bearer "+t {
			return true
		}
	}
	// Check rotated tokens
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	for _, rt := range s.rotatedTokens {
		if authHeader == "Bearer "+rt {
			return true
		}
	}
	return false
}


// SetRequireAPIAuth enables or disables mandatory bearer-token authentication
// on HTTP API endpoints. When enabled, requests without a valid Authorization
// header are rejected with 401. When disabled (default), missing auth is logged
// as a warning but the request is allowed through.
func (s *Server) SetRequireAPIAuth(require bool) {
	s.requireAPIAuth = require
}


// checkAPIAuth verifies the Authorization header on HTTP API requests. Returns
// true if the request should proceed, false if it should be rejected with 401.
// When requireAPIAuth is false, missing auth is logged but allowed.
func (s *Server) checkAPIAuth(w http.ResponseWriter, r *http.Request) bool {
	return s.checkAPIAuthWithHeader(w, r, r.Header.Get("Authorization"))
}

func (s *Server) checkAPIAuthWithHeader(w http.ResponseWriter, r *http.Request, authHeader string) bool {
	if s.isValidToken(authHeader) {
		return true
	}
	// Also check operator auth (bearer token from /api/v1/login)
	if s.operators != nil && !s.operators.IsEmpty() {
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token != authHeader && token != "" {
			if op := s.operators.GetByToken(token); op != nil {
				return true
			}
		}
	}
	if !s.requireAPIAuth {
		// Auth optional: log warning, allow through
		if authHeader == "" {
			log.Printf("[server] warning: unauthenticated API request from %s: %s (enable --require-api-auth to enforce)", r.RemoteAddr, r.URL.Path)
		} else {
			log.Printf("[server] warning: invalid auth token from %s: %s", r.RemoteAddr, r.URL.Path)
		}
		return true
	}
	// Auth required: reject
	log.Printf("[server] API auth rejected from %s: %s", r.RemoteAddr, r.URL.Path)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}


// InitiateTokenRotation sends a token_rotate command to the agent with the new
// token. It is used by the proactive rotation goroutine and can also be called
// manually (e.g. from an admin endpoint). Returns an error if the agent is not
// connected.
func (s *Server) InitiateTokenRotation(agentID string, newToken string) error {
	tr, rerr := s.resolveAgentTransport(agentID)
	if rerr != nil {
		return rerr
	}
	conn := tr.Conn
	params := protocol.TokenRotateParams{
		NewToken: newToken,
	}
	if s.tokenTTL > 0 {
		params.Expiry = time.Now().Add(s.tokenTTL)
	}
	paramData, _ := json.Marshal(params)
	env := protocol.Envelope{
		ID:     fmt.Sprintf("token-rotate-%d", time.Now().UnixMilli()),
		Type:   protocol.TypeAuthRefresh,
		Params: paramData,
	}
	if err := s.writeEncryptedJSON(agentID, conn, env); err != nil {
		return fmt.Errorf("send token_rotate: %w", err)
	}
	// Store rotated token so server accepts it on reconnect (P19-06: and
	// mirror the write to the persistent store so the slot survives restart).
	err := s.rotatedTokenStore.set(agentID, newToken, func() {
		s.rotatedTokens[agentID] = newToken
	})
	if err != nil {
		log.Printf("[server] P19-06 WARNING: rotated token for %s NOT persisted: %v", agentID, err)
	}
	log.Printf("[server] rotated token stored for agent %s", agentID)
	return nil
}

