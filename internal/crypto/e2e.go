package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
)

// E2E provides AES-GCM encryption/decryption for protocol payloads.
// The key is derived from the shared token using SHA-256 (32 bytes = AES-256).
// This ensures that relays cannot read the content of agent↔server traffic —
// they see only encrypted bytes. The relay forwards opaque frames without
// needing to parse the payload.
//
// Usage:
//   e2e := NewE2E("shared-token")
//   ciphertext := e2e.Encrypt(plaintext)
//   plaintext, err := e2e.Decrypt(ciphertext)
//
// Wire format: [12-byte nonce][ciphertext+16-byte GCM tag]
// The nonce is prepended to each encrypted message and is unique per message.

// E2E holds the AES-GCM cipher for encrypting/decrypting payloads.
type E2E struct {
	gcm cipher.AEAD
}

// NewE2E creates a new E2E encryptor from a shared token.
// The token is hashed with SHA-256 to produce a 32-byte AES-256 key.
func NewE2E(token string) (*E2E, error) {
	key := sha256.Sum256([]byte(token))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}
	return &E2E{gcm: gcm}, nil
}

// Encrypt encrypts a plaintext payload using AES-GCM.
// Returns: [nonce (12 bytes)][ciphertext + GCM tag (16 bytes)]
func (e *E2E) Encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, e.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := e.gcm.Seal(nil, nonce, plaintext, nil)
	// Prepend nonce to ciphertext
	result := make([]byte, len(nonce)+len(ciphertext))
	copy(result, nonce)
	copy(result[len(nonce):], ciphertext)
	return result, nil
}

// Decrypt decrypts an AES-GCM encrypted payload.
// Input format: [nonce (12 bytes)][ciphertext + GCM tag]
func (e *E2E) Decrypt(data []byte) ([]byte, error) {
	nonceSize := e.gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short: %d bytes (need at least %d)", len(data), nonceSize)
	}
	nonce := data[:nonceSize]
	ciphertext := data[nonceSize:]
	plaintext, err := e.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plaintext, nil
}

// Enabled returns true if E2E encryption is active.
// In the agent/server, this checks whether the e2e field is non-nil.
type Manager struct {
	e2e    *E2E
	enabled bool
	keyErr  error
}

// NewManager creates a new E2E manager.
//
// Posture (P23-14): "enabled but the key could not be built" is a LOUD state,
// never a silent fallback to plaintext. When enabled is true and the cipher
// construction fails (empty token is the only way today), the manager reports
// Enabled()==true with a nil cipher; IsActive() is false and Decrypt fails
// closed. A caller that constructed it with enabled=true MUST check
// CipherError() at startup and refuse to serve/connect on a non-nil error —
// the manager is not the component that decides to ship plaintext.
//
// enabled=false keeps the pass-through semantics the disabled case always had.
func NewManager(token string, enabled bool) *Manager {
	m := &Manager{enabled: enabled}
	if enabled {
		if token == "" {
			m.keyErr = fmt.Errorf("e2e enabled but token is empty — no key can be derived")
			return m
		}
		e2e, err := NewE2E(token)
		if err != nil {
			// AES-256 with a 32-byte SHA-256 key cannot fail in practice,
			// but a failure must never masquerade as "disabled": record it.
			m.keyErr = fmt.Errorf("e2e key construction failed: %w", err)
			return m
		}
		m.e2e = e2e
	}
	return m
}

// Encrypt encrypts plaintext when the cipher is active. When the manager was
// constructed enabled but its key never built, Encrypt fails closed — it never
// passes plaintext through under a posture that promised encryption.
func (m *Manager) Encrypt(plaintext []byte) ([]byte, error) {
	if m.e2e == nil {
		if m.keyErr != nil {
			return nil, fmt.Errorf("encrypt unavailable: %w", m.keyErr)
		}
		return plaintext, nil
	}
	return m.e2e.Encrypt(plaintext)
}

// Decrypt decrypts ciphertext when the cipher is active. When the manager was
// constructed enabled but its key never built, Decrypt fails closed.
func (m *Manager) Decrypt(data []byte) ([]byte, error) {
	if m.e2e == nil {
		if m.keyErr != nil {
			return nil, fmt.Errorf("decrypt unavailable: %w", m.keyErr)
		}
		return data, nil
	}
	return m.e2e.Decrypt(data)
}

// IsActive returns true if E2E encryption is active (cipher built). An
// enabled-but-broken manager is NOT active: its Decrypt fails closed, so the
// server's posture checks stay truthful and cannot mistake it for disabled.
func (m *Manager) IsActive() bool {
	return m.e2e != nil
}

// Enabled reports the configured posture, independent of whether the cipher
// actually built. Enabled() && !IsActive() means construction failed — a
// startup error the caller must surface, not a silent downgrade.
func (m *Manager) Enabled() bool { return m.enabled }

// CipherError returns the key-construction failure of an enabled manager, or
// nil when the manager is disabled or the cipher built cleanly.
func (m *Manager) CipherError() error { return m.keyErr }