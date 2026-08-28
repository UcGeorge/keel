package githubapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhookSignature(t *testing.T) {
	body := []byte(`{"action":"created"}`)
	good := sign("s3cret", body)
	if !VerifyWebhookSignature("s3cret", good, body) {
		t.Fatal("valid signature rejected")
	}
	if VerifyWebhookSignature("wrong", good, body) {
		t.Fatal("signature with wrong secret accepted")
	}
	if VerifyWebhookSignature("s3cret", good, []byte(`tampered`)) {
		t.Fatal("tampered body accepted")
	}
	if VerifyWebhookSignature("s3cret", "sha1=abc", body) {
		t.Fatal("non-sha256 scheme accepted")
	}
	if VerifyWebhookSignature("s3cret", "", body) {
		t.Fatal("empty header accepted")
	}
}

func TestPushEventBranch(t *testing.T) {
	ev := PushEvent{Ref: "refs/heads/main"}
	if ev.Branch() != "main" {
		t.Fatalf("branch = %q", ev.Branch())
	}
	tag := PushEvent{Ref: "refs/tags/v1"}
	if tag.Branch() == "v1" {
		// tags are not branches; Branch trims only refs/heads/
		t.Fatalf("tag treated as branch")
	}
}

func TestFromEnvUnconfigured(t *testing.T) {
	t.Setenv("KEEL_GITHUB_APP_ID", "")
	app, err := FromEnv()
	if app != nil || err != nil {
		t.Fatalf("expected nil app when unconfigured, got %v, %v", app, err)
	}
}
