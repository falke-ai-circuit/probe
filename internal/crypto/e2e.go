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
}

// NewManager creates a new E2E manager. If token is empty, E2E is disabled.
func NewManager(token string, enabled bool) *Manager {
	m := &Manager{enabled: enabled}
	if enabled && token != "" {
		e2e, err := NewE2E(token)
		if err == nil {
			m.e2e = e2e
		}
	}
	return m
}

// Encrypt encrypts plaintext if E2E is enabled, otherwise returns plaintext as-is.
func (m *Manager) Encrypt(plaintext []byte) ([]byte, error) {
	if m.e2e == nil {
		return plaintext, nil
	}
	return m.e2e.Encrypt(plaintext)
}

// Decrypt decrypts ciphertext if E2E is enabled, otherwise returns ciphertext as-is.
func (m *Manager) Decrypt(data []byte) ([]byte, error) {
	if m.e2e == nil {
		return data, nil
	}
	return m.e2e.Decrypt(data)
}

// IsActive returns true if E2E encryption is active (enabled + key derived).
func (m *Manager) IsActive() bool {
	return m.e2e != nil
}