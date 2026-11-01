// Package mcpauth implements Keel's OAuth authorization server for remote MCP.
// Public clients use authorization code + S256 PKCE. No portal session cookie,
// upstream provider token, or caller-supplied identity authenticates MCP.
package mcpauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"time"

	"github.com/UcGeorge/keel/internal/auth"
	"github.com/UcGeorge/keel/internal/store/clouddb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const ReadScope = "keel:read"
const FullScope = "keel:full"
const AccessTTL = 15 * time.Minute
const GrantTTL = 30 * 24 * time.Hour
const CodeTTL = 5 * time.Minute

var ErrInvalidGrant = errors.New("invalid grant")

type Principal struct {
	UserID  uuid.UUID
	GrantID uuid.UUID
	Scope   string
}

type Store struct {
	Pool *pgxpool.Pool
	Q    *clouddb.Queries
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// Exchange serializes on the grant. A consumed code or refresh token revokes
// the entire family, including access tokens, even on a different replica.
func (s Store) Exchange(ctx context.Context, kind, token, client, resource, redirect, verifier, requestedScope string) (*TokenResponse, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	q := s.Q.WithTx(tx)
	hash := auth.HashToken(token)
	var grantID uuid.UUID
	if kind == "authorization_code" {
		code, err := q.GetMCPCode(ctx, hash)
		if err != nil {
			return nil, grantLookupError(err)
		}
		grantID = code.GrantID
	} else {
		refresh, err := q.GetMCPToken(ctx, hash)
		if err != nil {
			return nil, grantLookupError(err)
		}
		if refresh.Kind != "refresh" {
			return nil, ErrInvalidGrant
		}
		grantID = refresh.GrantID
	}
	grant, err := q.LockMCPGrant(ctx, grantID)
	if err != nil {
		return nil, grantLookupError(err)
	}
	if grant.ClientID != client || grant.Resource != resource || (requestedScope != "" && requestedScope != grant.Scope) {
		return nil, ErrInvalidGrant
	}
	var used *time.Time
	var expires time.Time
	// Re-read after taking the family lock: another transaction may have
	// consumed this credential while we were waiting for the lock.
	if kind == "authorization_code" {
		code, err := q.GetMCPCode(ctx, hash)
		if err != nil {
			return nil, grantLookupError(err)
		}
		h := sha256.Sum256([]byte(verifier))
		actual := base64.RawURLEncoding.EncodeToString(h[:])
		if code.RedirectUri != redirect || !validVerifier(verifier) || subtle.ConstantTimeCompare([]byte(actual), []byte(code.Challenge)) != 1 {
			return nil, ErrInvalidGrant
		}
		used, expires = code.UsedAt, code.ExpiresAt
	} else {
		refresh, err := q.GetMCPToken(ctx, hash)
		if err != nil {
			return nil, grantLookupError(err)
		}
		used, expires = refresh.UsedAt, refresh.ExpiresAt
	}
	if used != nil {
		if err = q.RevokeMCPGrant(ctx, grant.ID); err != nil {
			return nil, err
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, ErrInvalidGrant
	}
	if !expires.After(time.Now()) {
		return nil, ErrInvalidGrant
	}
	if kind == "authorization_code" {
		err = q.ConsumeMCPCode(ctx, hash)
	} else {
		err = q.ConsumeMCPToken(ctx, hash)
	}
	if err != nil {
		return nil, err
	}
	access, ah, err := auth.NewToken()
	if err != nil {
		return nil, err
	}
	refresh, rh, err := auth.NewToken()
	if err != nil {
		return nil, err
	}
	accessExpiry := time.Now().Add(AccessTTL)
	if grant.ExpiresAt.Before(accessExpiry) {
		accessExpiry = grant.ExpiresAt
	}
	if err = q.CreateMCPToken(ctx, clouddb.CreateMCPTokenParams{Hash: ah, GrantID: grant.ID, Kind: "access", ExpiresAt: accessExpiry}); err != nil {
		return nil, err
	}
	if err = q.CreateMCPToken(ctx, clouddb.CreateMCPTokenParams{Hash: rh, GrantID: grant.ID, Kind: "refresh", ExpiresAt: grant.ExpiresAt}); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &TokenResponse{AccessToken: access, TokenType: "Bearer", ExpiresIn: max(0, int(time.Until(accessExpiry).Seconds())), RefreshToken: refresh, Scope: grant.Scope}, nil
}

func (s Store) Authenticate(ctx context.Context, token, resource string) (*Principal, error) {
	if len(token) != 43 {
		return nil, ErrInvalidGrant
	}
	row, err := s.Q.AuthenticateMCPToken(ctx, clouddb.AuthenticateMCPTokenParams{Hash: auth.HashToken(token), Resource: resource})
	if err != nil {
		return nil, grantLookupError(err)
	}
	return &Principal{UserID: row.UserID, GrantID: row.GrantID, Scope: row.Scope}, nil
}

// Allow applies a shared, database-backed quota across server replicas.
func (s Store) Allow(ctx context.Context, bucket string, limit int) bool {
	hits, err := s.Q.IncrementMCPRateLimit(ctx, bucket)
	return err == nil && int(hits) <= limit
}

func (s Store) Prune(ctx context.Context) error {
	for _, prune := range []func(context.Context) error{
		s.Q.DeleteExpiredMCPClients, s.Q.DeleteExpiredMCPGrants,
		s.Q.DeleteExpiredMCPCodes, s.Q.DeleteExpiredMCPAccessTokens,
		s.Q.DeleteExpiredMCPRateLimits, s.Q.DeleteExpiredMCPAudit,
	} {
		if err := prune(ctx); err != nil {
			return err
		}
	}
	return nil
}

func validVerifier(s string) bool {
	if len(s) < 43 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~') {
			return false
		}
	}
	return true
}

func grantLookupError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidGrant
	}
	return err
}
