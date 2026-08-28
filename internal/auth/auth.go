// Package auth implements password hashing (argon2id) and opaque session
// tokens for Keel Cloud.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/mail"
	"regexp"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters per current OWASP guidance.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// HashPassword derives an argon2id hash in PHC string format.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the stored PHC hash.
func VerifyPassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var mem, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &p); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, mem, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// NewToken returns a fresh 256-bit token (URL-safe base64) and its SHA-256
// hash. The token goes to the client; only the hash is stored.
func NewToken() (token string, hash []byte, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	h := sha256.Sum256([]byte(token))
	return token, h[:], nil
}

// HashToken hashes a client-presented token for lookup.
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// emailRe is the pragmatic shape check used for signups.
var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// ValidEmail reports whether e looks like a deliverable address.
func ValidEmail(e string) bool {
	if _, err := mail.ParseAddress(e); err != nil {
		return false
	}
	return emailRe.MatchString(e)
}

// NormalizeEmail lowercases and trims an email address.
func NormalizeEmail(e string) string {
	return strings.ToLower(strings.TrimSpace(e))
}

// CheckPasswordStrength returns a human-readable problem, or "".
func CheckPasswordStrength(pw string) string {
	if len(pw) < 10 {
		return "Use at least 10 characters."
	}
	if len(pw) > 512 {
		return "Use at most 512 characters."
	}
	return ""
}

// slugRe validates organization slugs.
var slugRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// reservedSlugs collide with application routes and can never name an org.
var reservedSlugs = map[string]bool{"new": true}

// ValidSlug reports whether s is a valid organization slug.
func ValidSlug(s string) bool {
	return len(s) >= 2 && len(s) <= 40 && slugRe.MatchString(s) && !reservedSlugs[s]
}

// Slugify derives a slug candidate from a display name.
func Slugify(name string) string {
	var b strings.Builder
	prevDash := true // suppress leading dash
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
