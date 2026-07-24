package crypto

import (
	"testing"
)

func TestE2EEncryptDecrypt(t *testing.T) {
	e2e, err := NewE2E("test-secret-token")
	if err != nil {
		t.Fatalf("NewE2E: %v", err)
	}

	plaintext := []byte(`{"type":"ping","id":"12345"}`)
	ciphertext, err := e2e.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	if string(ciphertext) == string(plaintext) {
		t.Fatal("ciphertext should differ from plaintext")
	}

	decrypted, err := e2e.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Fatalf("decrypted != plaintext: got %q, want %q", decrypted, plaintext)
	}
}

func TestE2EDifferentNonceEachEncrypt(t *testing.T) {
	e2e, err := NewE2E("test-secret-token")
	if err != nil {
		t.Fatalf("NewE2E: %v", err)
	}

	plaintext := []byte(`{"type":"ping"}`)
	ct1, _ := e2e.Encrypt(plaintext)
	ct2, _ := e2e.Encrypt(plaintext)

	if string(ct1) == string(ct2) {
		t.Fatal("two encryptions of same plaintext should produce different ciphertexts (unique nonce)")
	}
}

func TestE2EWrongKeyFails(t *testing.T) {
	e2e1, _ := NewE2E("token-1")
	e2e2, _ := NewE2E("token-2")

	plaintext := []byte(`{"type":"ping"}`)
	ct, _ := e2e1.Encrypt(plaintext)

	_, err := e2e2.Decrypt(ct)
	if err == nil {
		t.Fatal("decrypt with wrong key should fail")
	}
}

func TestManagerDisabled(t *testing.T) {
	m := NewManager("token", false)
	if m.IsActive() {
		t.Fatal("manager should not be active when disabled")
	}

	plaintext := []byte("test data")
	encrypted, err := m.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if string(encrypted) != string(plaintext) {
		t.Fatal("disabled manager should return plaintext as-is")
	}
}

func TestManagerEnabled(t *testing.T) {
	m := NewManager("test-token", true)
	if !m.IsActive() {
		t.Fatal("manager should be active when enabled with non-empty token")
	}

	plaintext := []byte("test data")
	encrypted, err := m.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if string(encrypted) == string(plaintext) {
		t.Fatal("enabled manager should encrypt data")
	}

	decrypted, err := m.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(decrypted) != string(plaintext) {
		t.Fatal("decrypted != plaintext")
	}
}