// Package secretbox encrypts variable values at rest with AES-256-GCM.
//
// Keel Dev generates and stores a key per machine automatically; Keel Cloud
// takes its key from the KEEL_ENCRYPTION_KEY environment variable (or
// generates one into its data directory on first start).
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// KeySize is the AES-256 key size in bytes.
const KeySize = 32

// Box seals and opens values with a fixed key.
type Box struct {
	aead cipher.AEAD
}

// New creates a Box from a raw 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("encryption key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// NewFromHex creates a Box from a 64-character hex-encoded key.
func NewFromHex(hexKey string) (*Box, error) {
	key, err := hex.DecodeString(strings.TrimSpace(hexKey))
	if err != nil {
		return nil, fmt.Errorf("encryption key is not valid hex: %w", err)
	}
	return New(key)
}

// GenerateKeyHex returns a fresh random key, hex encoded.
func GenerateKeyHex() (string, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return hex.EncodeToString(key), nil
}

// Seal encrypts plaintext. The nonce is prepended to the ciphertext.
func (b *Box) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open decrypts data produced by Seal.
func (b *Box) Open(data []byte) ([]byte, error) {
	ns := b.aead.NonceSize()
	if len(data) < ns {
		return nil, errors.New("ciphertext too short")
	}
	return b.aead.Open(nil, data[:ns], data[ns:], nil)
}

// SealString encrypts a string value.
func (b *Box) SealString(s string) ([]byte, error) { return b.Seal([]byte(s)) }

// OpenString decrypts to a string value.
func (b *Box) OpenString(data []byte) (string, error) {
	p, err := b.Open(data)
	if err != nil {
		return "", err
	}
	return string(p), nil
}

// LoadOrCreateKeyFile returns a Box backed by the hex key stored at path,
// creating the file (0600) with a fresh key if it does not exist.
func LoadOrCreateKeyFile(path string) (*Box, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return NewFromHex(string(data))
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	hexKey, err := GenerateKeyHex()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hexKey+"\n"), 0o600); err != nil {
		return nil, err
	}
	return NewFromHex(hexKey)
}
