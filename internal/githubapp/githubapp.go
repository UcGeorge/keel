// Package githubapp integrates Keel Cloud with a GitHub App: signing app
// JWTs, minting installation tokens, listing installation repositories, and
// verifying webhook signatures.
//
// Configuration comes from the environment (see FromEnv). The integration
// is optional — when unconfigured, repositories connect via plain git URLs.
package githubapp

import (
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// App is a configured GitHub App integration.
type App struct {
	AppID         int64
	Slug          string
	WebhookSecret string
	PrivateKey    *rsa.PrivateKey

	BaseURL string // https://api.github.com, overridable for tests

	mu     sync.Mutex
	tokens map[int64]cachedToken
	client *http.Client
}

type cachedToken struct {
	token   string
	expires time.Time
}

// FromEnv builds the App from KEEL_GITHUB_APP_ID, KEEL_GITHUB_APP_SLUG,
// KEEL_GITHUB_WEBHOOK_SECRET, and KEEL_GITHUB_PRIVATE_KEY (PEM, inline or
// a file path via KEEL_GITHUB_PRIVATE_KEY_FILE). It returns (nil, nil) when
// the integration is not configured.
func FromEnv() (*App, error) {
	appID := strings.TrimSpace(os.Getenv("KEEL_GITHUB_APP_ID"))
	if appID == "" {
		return nil, nil
	}
	var id int64
	if _, err := fmt.Sscanf(appID, "%d", &id); err != nil {
		return nil, fmt.Errorf("KEEL_GITHUB_APP_ID must be numeric: %q", appID)
	}
	pemData := os.Getenv("KEEL_GITHUB_PRIVATE_KEY")
	if pemData == "" {
		if path := os.Getenv("KEEL_GITHUB_PRIVATE_KEY_FILE"); path != "" {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read KEEL_GITHUB_PRIVATE_KEY_FILE: %w", err)
			}
			pemData = string(data)
		}
	}
	if pemData == "" {
		return nil, fmt.Errorf("KEEL_GITHUB_APP_ID is set but no private key was provided (KEEL_GITHUB_PRIVATE_KEY or KEEL_GITHUB_PRIVATE_KEY_FILE)")
	}
	key, err := parsePrivateKey([]byte(pemData))
	if err != nil {
		return nil, err
	}
	secret := os.Getenv("KEEL_GITHUB_WEBHOOK_SECRET")
	if secret == "" {
		return nil, fmt.Errorf("KEEL_GITHUB_WEBHOOK_SECRET is required when the GitHub App is configured")
	}
	return &App{
		AppID:         id,
		Slug:          os.Getenv("KEEL_GITHUB_APP_SLUG"),
		WebhookSecret: secret,
		PrivateKey:    key,
		BaseURL:       "https://api.github.com",
		tokens:        map[int64]cachedToken{},
		client:        &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func parsePrivateKey(pemData []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, fmt.Errorf("GitHub App private key is not valid PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse GitHub App private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("GitHub App private key must be RSA")
	}
	return key, nil
}

// InstallURL is where an admin installs the app on their account.
func (a *App) InstallURL() string {
	if a.Slug == "" {
		return "https://github.com/settings/installations"
	}
	return "https://github.com/apps/" + a.Slug + "/installations/new"
}

// appJWT signs a short-lived app JWT.
func (a *App) appJWT() (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now.Add(-60 * time.Second)),
		ExpiresAt: jwt.NewNumericDate(now.Add(9 * time.Minute)),
		Issuer:    fmt.Sprintf("%d", a.AppID),
	}
	return jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(a.PrivateKey)
}

// InstallationToken returns a token for one installation, cached until
// shortly before expiry.
func (a *App) InstallationToken(installationID int64) (string, error) {
	a.mu.Lock()
	if c, ok := a.tokens[installationID]; ok && time.Until(c.expires) > 2*time.Minute {
		a.mu.Unlock()
		return c.token, nil
	}
	a.mu.Unlock()

	jwtStr, err := a.appJWT()
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", a.BaseURL, installationID)
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwtStr)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("github: create installation token: %s: %s", resp.Status, truncateBody(body))
	}
	var payload struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	a.mu.Lock()
	a.tokens[installationID] = cachedToken{token: payload.Token, expires: payload.ExpiresAt}
	a.mu.Unlock()
	return payload.Token, nil
}

// Repo is one repository visible to an installation.
type Repo struct {
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
}

// ListInstallationRepos lists every repository the installation can access.
func (a *App) ListInstallationRepos(installationID int64) ([]Repo, error) {
	token, err := a.InstallationToken(installationID)
	if err != nil {
		return nil, err
	}
	var all []Repo
	for page := 1; page <= 20; page++ {
		url := fmt.Sprintf("%s/installation/repositories?per_page=100&page=%d", a.BaseURL, page)
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := a.client.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("github: list repositories: %s: %s", resp.Status, truncateBody(body))
		}
		var payload struct {
			TotalCount   int    `json:"total_count"`
			Repositories []Repo `json:"repositories"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		all = append(all, payload.Repositories...)
		if len(all) >= payload.TotalCount || len(payload.Repositories) == 0 {
			break
		}
	}
	return all, nil
}

// VerifyWebhook checks the X-Hub-Signature-256 header against the body.
func (a *App) VerifyWebhook(signatureHeader string, body []byte) bool {
	return VerifyWebhookSignature(a.WebhookSecret, signatureHeader, body)
}

// VerifyWebhookSignature implements GitHub's HMAC-SHA256 webhook scheme.
func VerifyWebhookSignature(secret, signatureHeader string, body []byte) bool {
	sig, ok := strings.CutPrefix(signatureHeader, "sha256=")
	if !ok {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(sig))
}

func truncateBody(b []byte) string {
	s := string(b)
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// --- webhook payloads --------------------------------------------------------

// PushEvent is the subset of the push webhook Keel uses.
type PushEvent struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Repository struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// Branch returns the branch name for a branch push, or "".
func (p *PushEvent) Branch() string {
	return strings.TrimPrefix(p.Ref, "refs/heads/")
}

// InstallationEvent covers installation and installation_repositories hooks.
type InstallationEvent struct {
	Action       string `json:"action"`
	Installation struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"account"`
	} `json:"installation"`
}
