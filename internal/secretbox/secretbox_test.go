package secretbox

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	hexKey, err := GenerateKeyHex()
	if err != nil {
		t.Fatal(err)
	}
	box, err := NewFromHex(hexKey)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.SealString("s3cr3t value")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("s3cr3t")) {
		t.Fatal("ciphertext contains plaintext")
	}
	got, err := box.OpenString(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if got != "s3cr3t value" {
		t.Fatalf("got %q", got)
	}
}

func TestOpenWithWrongKeyFails(t *testing.T) {
	k1, _ := GenerateKeyHex()
	k2, _ := GenerateKeyHex()
	b1, _ := NewFromHex(k1)
	b2, _ := NewFromHex(k2)
	sealed, _ := b1.SealString("x")
	if _, err := b2.Open(sealed); err == nil {
		t.Fatal("expected decryption failure with wrong key")
	}
}

func TestTamperDetected(t *testing.T) {
	k, _ := GenerateKeyHex()
	b, _ := NewFromHex(k)
	sealed, _ := b.SealString("x")
	sealed[len(sealed)-1] ^= 0xff
	if _, err := b.Open(sealed); err == nil {
		t.Fatal("expected tamper detection")
	}
}

func TestBadKeys(t *testing.T) {
	if _, err := NewFromHex("zz"); err == nil {
		t.Fatal("expected error for non-hex key")
	}
	if _, err := New([]byte("short")); err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestLoadOrCreateKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "secret.key")
	b1, err := LoadOrCreateKeyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", st.Mode().Perm())
	}
	sealed, _ := b1.SealString("persist")
	// Reload from the same file: must decrypt values sealed before.
	b2, err := LoadOrCreateKeyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := b2.OpenString(sealed)
	if err != nil || got != "persist" {
		t.Fatalf("got %q, err %v", got, err)
	}
}
