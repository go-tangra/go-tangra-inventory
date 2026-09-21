// Package enroll issues single-use enrollment tokens and per-agent
// credentials for the off-mesh agent plane. A token's plaintext secret is
// returned once at mint and only its SHA-256 hash is stored; a per-agent
// credential is generated once at enroll, sealed at rest by the envelope, and
// verified with a constant-time compare. Sealed bytes and stored hashes never
// leave this package.
package enroll

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/go-freya/freya/services/inventory/internal/repo"
	"github.com/go-freya/freya/services/inventory/internal/sealed"
	"github.com/go-freya/freya/services/inventory/internal/store"
)

// Sentinel errors. They are deliberately coarse so callers cannot distinguish
// "used" from "expired" from "unknown" (avoids an enumeration oracle).
var (
	// ErrTokenInvalid is returned when an enrollment token is missing, used,
	// expired, revoked, or a mint precondition fails.
	ErrTokenInvalid = errors.New("enroll: enrollment token invalid")
	// ErrUnauthenticated is returned when an agent credential does not verify.
	ErrUnauthenticated = errors.New("enroll: agent credential rejected")
)

// secretBytes is the entropy of a minted token secret and a per-agent
// credential (32 bytes -> 43 base64url chars).
const secretBytes = 32

// Test seams: overridable so error paths of the CSPRNG and the envelope seal
// can be exercised. Production always uses crypto/rand and Envelope.Seal.
var (
	randRead = rand.Read
	sealFn   = func(env *sealed.Envelope, plaintext, ad []byte) ([]byte, error) {
		return env.Seal(plaintext, ad)
	}
)

// Service mints enrollment tokens and issues/verifies per-agent credentials.
type Service struct {
	st  repo.Store
	env *sealed.Envelope
	now func() time.Time
}

// New builds a Service. The clock defaults to time.Now.
func New(st repo.Store, env *sealed.Envelope) *Service {
	return &Service{st: st, env: env, now: time.Now}
}

// SetClock overrides the clock (tests). A nil clock is ignored.
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// MintToken generates a high-entropy secret, stores only its SHA-256 hash with
// an expiry, and returns the plaintext secret ONCE plus the audit-friendly
// token row (its stored hash blanked). The secret is never persisted.
func (s *Service) MintToken(ctx context.Context, tenantID, createdBy, label string, ttl time.Duration) (secret string, tok store.EnrollmentToken, err error) {
	if tenantID == "" || ttl <= 0 {
		return "", store.EnrollmentToken{}, ErrTokenInvalid
	}
	secret, err = randomSecret()
	if err != nil {
		return "", store.EnrollmentToken{}, err
	}
	now := s.now()
	tok = store.EnrollmentToken{
		ID:        store.NewID(),
		TenantID:  tenantID,
		TokenHash: hashSecret(secret),
		ExpiresAt: now.Add(ttl),
		CreatedBy: createdBy,
		CreatedAt: now,
		Label:     label,
	}
	if err = s.st.CreateEnrollmentToken(ctx, tok); err != nil {
		return "", store.EnrollmentToken{}, err
	}
	tok.TokenHash = "" // never hand back the stored hash
	return secret, tok, nil
}

// Enroll consumes an enrollment token atomically (single use), then generates a
// per-agent credential, seals it, and creates the agent bound to the token's
// tenant. It returns the agent id and the plaintext credential ONCE. Any token
// problem is collapsed to ErrTokenInvalid.
func (s *Service) Enroll(ctx context.Context, secret string, ident store.Identity, agentVersion string) (agentID string, credential string, err error) {
	tok, err := s.st.ConsumeEnrollmentToken(ctx, hashSecret(secret), s.now())
	if err != nil {
		return "", "", ErrTokenInvalid
	}
	credential, err = randomSecret()
	if err != nil {
		return "", "", err
	}
	agentID = store.NewID()
	blob, err := sealFn(s.env, []byte(credential), agentAD(agentID))
	if err != nil {
		return "", "", err
	}
	now := s.now()
	agent := store.Agent{
		ID:               agentID,
		TenantID:         tok.TenantID,
		CredentialSealed: blob,
		EnrolledAt:       now,
		LastSeen:         now,
		AgentVersion:     agentVersion,
		IdentityHint:     ident.Hostname,
	}
	if err = s.st.CreateAgent(ctx, agent); err != nil {
		return "", "", err
	}
	return agentID, credential, nil
}

// Verify authenticates a presented credential against the sealed one. On
// success it returns the agent (tenant/host scope) with the sealed bytes
// cleared; on any failure it returns ErrUnauthenticated.
func (s *Service) Verify(ctx context.Context, agentID, credential string) (store.Agent, error) {
	agent, err := s.st.GetAgentByID(ctx, agentID)
	if err != nil {
		return store.Agent{}, ErrUnauthenticated
	}
	if agent.Revoked {
		return store.Agent{}, ErrUnauthenticated
	}
	clear, err := s.env.Open(agent.CredentialSealed, agentAD(agentID))
	if err != nil {
		return store.Agent{}, ErrUnauthenticated
	}
	if subtle.ConstantTimeCompare(clear, []byte(credential)) != 1 {
		return store.Agent{}, ErrUnauthenticated
	}
	agent.CredentialSealed = nil // never surface sealed bytes
	return agent, nil
}

// RevokeAgent revokes an agent within a tenant.
func (s *Service) RevokeAgent(ctx context.Context, tenantID, agentID string) error {
	return s.st.RevokeAgent(ctx, tenantID, agentID)
}

// RevokeToken revokes an enrollment token within a tenant.
func (s *Service) RevokeToken(ctx context.Context, tenantID, tokenID string) error {
	return s.st.RevokeEnrollmentToken(ctx, tenantID, tokenID)
}

// randomSecret returns a URL-safe, high-entropy secret.
func randomSecret() (string, error) {
	b := make([]byte, secretBytes)
	if _, err := randRead(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashSecret returns the lowercase hex SHA-256 of a secret. Only the hash is
// stored; it is never returned to callers.
func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// agentAD binds a sealed credential to its agent id (envelope associated data).
func agentAD(agentID string) []byte { return []byte("agent:" + agentID) }
