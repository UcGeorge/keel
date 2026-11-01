package mcpauth

import "testing"

func TestIssuerAndRedirectValidation(t *testing.T) {
	for _, s := range []string{"https://keel.example.com", "http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		if err := ValidateIssuer(s); err != nil {
			t.Errorf("rejected %s: %v", s, err)
		}
	}
	for _, s := range []string{"http://keel.example.com", "https://u:p@example.com", "https://example.com/base", "https://example.com?q=1", "https://example.com#x", "//example.com", "file:///tmp/keel"} {
		if ValidateIssuer(s) == nil {
			t.Errorf("accepted issuer %s", s)
		}
	}
	for _, s := range []string{"https://agent.example/callback", "http://localhost:1234/cb?x=y", "http://127.0.0.1/cb", "http://[::1]:1234/cb"} {
		if !validRedirect(s) {
			t.Errorf("rejected callback %s", s)
		}
	}
	for _, s := range []string{"http://evil.example/callback", "https://user@example.com/cb", "https://example.com/cb#fragment", "javascript:alert(1)", "//example.com/cb", "https://example.com/\\evil", "https://example.com/\n"} {
		if validRedirect(s) {
			t.Errorf("accepted callback %s", s)
		}
	}
}
func TestPKCEVerifierValidation(t *testing.T) {
	for _, s := range []string{"", "short", "01234567890123456789012345678901234567890123!", "01234567890123456789012345678901234567890123="} {
		if validVerifier(s) {
			t.Errorf("accepted %q", s)
		}
	}
	if !validVerifier("01234567890123456789012345678901234567890123-._~") {
		t.Fatal("valid verifier rejected")
	}
}
