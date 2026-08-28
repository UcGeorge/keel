package auth

import (
	"bytes"
	"strings"
	"testing"
)

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("hash format: %s", hash)
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Fatal("correct password rejected")
	}
	if VerifyPassword(hash, "wrong") {
		t.Fatal("wrong password accepted")
	}
	if VerifyPassword("garbage", "x") {
		t.Fatal("garbage hash accepted")
	}
}

func TestTokens(t *testing.T) {
	tok, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) < 40 {
		t.Fatalf("token too short: %d", len(tok))
	}
	if !bytes.Equal(HashToken(tok), hash) {
		t.Fatal("hash mismatch")
	}
	tok2, _, _ := NewToken()
	if tok == tok2 {
		t.Fatal("tokens not unique")
	}
}

func TestValidEmail(t *testing.T) {
	for _, good := range []string{"a@b.co", "user.name+tag@example.org"} {
		if !ValidEmail(good) {
			t.Errorf("rejected %q", good)
		}
	}
	for _, bad := range []string{"", "nope", "a@b", "a b@c.com", "Display <a@b.co>"} {
		if ValidEmail(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Acme Corp":        "acme-corp",
		"  Pete's Team!  ": "pete-s-team",
		"---":              "",
		"ABC123":           "abc123",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
	if !ValidSlug("acme-corp") || ValidSlug("-bad") || ValidSlug("a") {
		t.Error("ValidSlug boundaries wrong")
	}
}
