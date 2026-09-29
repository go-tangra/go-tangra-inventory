// Package autoenroll lets agents enroll without a single-use token (feature
// 029). A tenant turns the feature on with a master switch and creates
// auto-enrollment keys. An agent (or go-tangra-client acting for it) proves
// possession of a key with an HMAC over its enrollment request
// (sdk/pkg/autoenroll); the key itself never travels.
//
// An enrollment is accepted only when all hold: the key exists, is enabled,
// not expired and below its enrollment limit; the tenant switch is on; the
// caller's address is inside the key's allowed networks; the signature
// matches; the timestamp is within the freshness window; and the nonce was
// not used before. Refusals are coarse to the caller (ErrRejected) and
// audited with a reason code in the key's tenant. Key secrets are sealed with
// the module envelope and returned only once, at creation or rotation.
package autoenroll

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"
	"unicode"

	inventoryv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	proof "github.com/go-tangra/go-tangra-inventory/sdk/v4/pkg/autoenroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/enroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sealed"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Errors.
var (
	// ErrRejected is the only error an enrolling caller sees.
	ErrRejected = errors.New("autoenroll: enrollment rejected")
	// ErrNotFound reports an unknown key in the caller's tenant.
	ErrNotFound = errors.New("autoenroll: key not found")
	// ErrConflict reports a duplicate key name.
	ErrConflict = errors.New("autoenroll: key name already used")
)

// InvalidError is a validation failure of an administrative request.
type InvalidError struct{ Field, Msg string }

func (e *InvalidError) Error() string { return "autoenroll: " + e.Field + ": " + e.Msg }

func invalid(field, msg string) error { return &InvalidError{Field: field, Msg: msg} }

// Refusal reason codes (audit only).
const (
	ReasonTenantDisabled = "tenant_disabled"
	ReasonKeyDisabled    = "key_disabled"
	ReasonKeyExpired     = "key_expired"
	ReasonKeyExhausted   = "key_exhausted"
	ReasonAddress        = "address_not_allowed"
	ReasonSignature      = "bad_signature"
	ReasonStale          = "stale_timestamp"
	ReasonReplay         = "replay"
	ReasonStateChanged   = "state_changed" // key or switch changed during the enrollment
	ReasonRateLimited    = "rate_limited"
)

// Limits.
const (
	DefaultWindow  = 5 * time.Minute
	MaxCIDRs       = 32
	MaxNameLen     = 100
	MaxEnrollments = 1_000_000
	secretPrefix   = "aks_"
	refusalQuiet   = 10 * time.Second // per key+reason+address audit throttle
	// RatePerMinute bounds accepted enrollments per key and replica: a
	// leaked secret cannot mint agents faster than this.
	RatePerMinute = 60
)

// Test seams.
var (
	randRead = rand.Read
	sealFn   = func(env *sealed.Envelope, pt, ad []byte) ([]byte, error) { return env.Seal(pt, ad) }
)

// Actor is who performs an administrative change.
type Actor struct{ Kind, ID string }

// Service manages settings and keys and verifies automatic enrollments.
type Service struct {
	st     repo.Store
	env    *sealed.Envelope
	now    func() time.Time
	window time.Duration

	mu    sync.Mutex
	quiet map[string]time.Time   // key_id|reason|address -> last refusal audited
	rate  map[string][]time.Time // key_id -> accepted enrollments in the last minute
}

// New builds a Service with the default freshness window.
func New(st repo.Store, env *sealed.Envelope) *Service {
	return &Service{st: st, env: env, now: time.Now, window: DefaultWindow, quiet: map[string]time.Time{}, rate: map[string][]time.Time{}}
}

// SetClock overrides the clock (tests); nil is ignored.
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// SetWindow overrides the freshness window; non-positive values are ignored.
func (s *Service) SetWindow(d time.Duration) {
	if d > 0 {
		s.window = d
	}
}

// Window is the accepted clock skew of a proof timestamp (either way).
func (s *Service) Window() time.Duration { return s.window }

func keyAD(id string) []byte { return []byte("autoenroll:" + id) }

// ---- settings ----

// Settings returns the tenant's switch (off when never set).
func (s *Service) Settings(ctx context.Context, tenantID string) (store.AutoEnrollSettings, error) {
	st, _, err := s.st.GetAutoEnrollSettings(ctx, tenantID)
	return st, err
}

// SetEnabled turns automatic enrollment on or off for the tenant.
func (s *Service) SetEnabled(ctx context.Context, tenantID string, actor Actor, enabled bool) (store.AutoEnrollSettings, error) {
	now := s.now().UTC()
	st := store.AutoEnrollSettings{TenantID: tenantID, Enabled: enabled, UpdatedBy: actor.ID, UpdatedAt: now}
	row, err := audit.Row(audit.Event{TenantID: tenantID, EventType: audit.AutoEnrollSettingsUpdated, ActorKind: actor.Kind, ActorID: actor.ID,
		SubjectKind: audit.SubjectSystem, SubjectID: "auto_enroll", Outcome: audit.OutcomeOK, Details: map[string]any{"enabled": enabled}}, now)
	if err != nil {
		return store.AutoEnrollSettings{}, err
	}
	if err := s.st.PutAutoEnrollSettings(ctx, st, row); err != nil {
		return store.AutoEnrollSettings{}, err
	}
	return st, nil
}

// ---- keys ----

// KeyInput creates a key.
type KeyInput struct {
	Name           string
	AllowedCIDRs   []string
	Enabled        *bool // default true
	ExpiresAt      *time.Time
	MaxEnrollments int
}

// KeyPatch changes a key; nil fields are kept. ClearExpiry removes the expiry.
type KeyPatch struct {
	Name           *string
	AllowedCIDRs   *[]string
	Enabled        *bool
	ExpiresAt      *time.Time
	ClearExpiry    bool
	MaxEnrollments *int
}

// Keys lists the tenant's keys (secrets stripped).
func (s *Service) Keys(ctx context.Context, tenantID string) ([]store.AutoEnrollKey, error) {
	keys, err := s.st.ListAutoEnrollKeys(ctx, tenantID)
	for i := range keys {
		keys[i].SecretSealed = nil
	}
	return keys, err
}

// CreateKey validates in, generates the key id and secret, stores the sealed
// secret and returns the key with the plaintext secret (shown once).
func (s *Service) CreateKey(ctx context.Context, tenantID string, actor Actor, in KeyInput) (store.AutoEnrollKey, string, error) {
	now := s.now().UTC()
	name, err := checkName(in.Name)
	if err != nil {
		return store.AutoEnrollKey{}, "", err
	}
	cidrs, err := NormalizeCIDRs(in.AllowedCIDRs)
	if err != nil {
		return store.AutoEnrollKey{}, "", err
	}
	if err := checkExpiry(in.ExpiresAt, now); err != nil {
		return store.AutoEnrollKey{}, "", err
	}
	if err := checkMax(in.MaxEnrollments); err != nil {
		return store.AutoEnrollKey{}, "", err
	}
	keyID, secret, err := newKeyMaterial()
	if err != nil {
		return store.AutoEnrollKey{}, "", err
	}
	k := store.AutoEnrollKey{ID: store.NewID(), TenantID: tenantID, KeyID: keyID, Name: name, AllowedCIDRs: cidrs, Enabled: in.Enabled == nil || *in.Enabled,
		ExpiresAt: utcPtr(in.ExpiresAt), MaxEnrollments: in.MaxEnrollments, CreatedBy: actor.ID, CreatedAt: now, UpdatedAt: now}
	if k.SecretSealed, err = sealFn(s.env, []byte(secret), keyAD(k.ID)); err != nil {
		return store.AutoEnrollKey{}, "", err
	}
	row, err := s.keyRow(audit.AutoEnrollKeyCreated, actor, k, now, map[string]any{"allowed_cidrs": cidrs, "max_enrollments": k.MaxEnrollments})
	if err != nil {
		return store.AutoEnrollKey{}, "", err
	}
	if err := s.st.CreateAutoEnrollKey(ctx, k, row); err != nil {
		return store.AutoEnrollKey{}, "", mapRepo(err)
	}
	k.SecretSealed = nil
	return k, secret, nil
}

// UpdateKey applies p to the key.
func (s *Service) UpdateKey(ctx context.Context, tenantID string, actor Actor, id string, p KeyPatch) (store.AutoEnrollKey, error) {
	now := s.now().UTC()
	var name string
	var cidrs []string
	var err error
	if p.Name != nil {
		if name, err = checkName(*p.Name); err != nil {
			return store.AutoEnrollKey{}, err
		}
	}
	if p.AllowedCIDRs != nil {
		if cidrs, err = NormalizeCIDRs(*p.AllowedCIDRs); err != nil {
			return store.AutoEnrollKey{}, err
		}
	}
	if !p.ClearExpiry {
		if err := checkExpiry(p.ExpiresAt, now); err != nil {
			return store.AutoEnrollKey{}, err
		}
	}
	if p.MaxEnrollments != nil {
		if err := checkMax(*p.MaxEnrollments); err != nil {
			return store.AutoEnrollKey{}, err
		}
	}
	k, err := s.st.UpdateAutoEnrollKey(ctx, tenantID, id, func(k *store.AutoEnrollKey) (store.AuditRow, error) {
		changed := map[string]any{}
		if p.Name != nil {
			k.Name, changed["name"] = name, name
		}
		if p.AllowedCIDRs != nil {
			k.AllowedCIDRs, changed["allowed_cidrs"] = cidrs, cidrs
		}
		if p.Enabled != nil {
			k.Enabled, changed["enabled"] = *p.Enabled, *p.Enabled
		}
		if p.ClearExpiry {
			k.ExpiresAt, changed["expires_at"] = nil, nil
		} else if p.ExpiresAt != nil {
			k.ExpiresAt, changed["expires_at"] = utcPtr(p.ExpiresAt), p.ExpiresAt.UTC()
		}
		if p.MaxEnrollments != nil {
			k.MaxEnrollments, changed["max_enrollments"] = *p.MaxEnrollments, *p.MaxEnrollments
		}
		k.UpdatedAt = now
		return s.keyRow(audit.AutoEnrollKeyUpdated, actor, *k, now, changed)
	})
	if err != nil {
		return store.AutoEnrollKey{}, mapRepo(err)
	}
	k.SecretSealed = nil
	return k, nil
}

// RotateKey replaces the key's secret (the key id stays) and returns the new
// secret once. Agents already enrolled keep working; new enrollments need the
// new secret.
func (s *Service) RotateKey(ctx context.Context, tenantID string, actor Actor, id string) (store.AutoEnrollKey, string, error) {
	now := s.now().UTC()
	_, secret, err := newKeyMaterial()
	if err != nil {
		return store.AutoEnrollKey{}, "", err
	}
	k, err := s.st.UpdateAutoEnrollKey(ctx, tenantID, id, func(k *store.AutoEnrollKey) (store.AuditRow, error) {
		blob, err := sealFn(s.env, []byte(secret), keyAD(k.ID))
		if err != nil {
			return store.AuditRow{}, err
		}
		k.SecretSealed, k.UpdatedAt = blob, now
		return s.keyRow(audit.AutoEnrollKeyRotated, actor, *k, now, nil)
	})
	if err != nil {
		return store.AutoEnrollKey{}, "", mapRepo(err)
	}
	k.SecretSealed = nil
	return k, secret, nil
}

// DeleteKey removes the key; agents enrolled with it keep their credential.
func (s *Service) DeleteKey(ctx context.Context, tenantID string, actor Actor, id string) error {
	row, err := audit.Row(audit.Event{TenantID: tenantID, EventType: audit.AutoEnrollKeyDeleted, ActorKind: actor.Kind, ActorID: actor.ID,
		SubjectKind: audit.SubjectAutoEnrollKey, SubjectID: id, Outcome: audit.OutcomeOK}, s.now().UTC())
	if err != nil {
		return err
	}
	return mapRepo(s.st.DeleteAutoEnrollKey(ctx, tenantID, id, row))
}

func (s *Service) keyRow(t audit.EventType, actor Actor, k store.AutoEnrollKey, now time.Time, details map[string]any) (store.AuditRow, error) {
	d := map[string]any{"key_id": k.KeyID, "name": k.Name}
	for n, v := range details {
		d[n] = v
	}
	return audit.Row(audit.Event{TenantID: k.TenantID, EventType: t, ActorKind: actor.Kind, ActorID: actor.ID,
		SubjectKind: audit.SubjectAutoEnrollKey, SubjectID: k.ID, Outcome: audit.OutcomeOK, Details: d}, now)
}

// ---- enrollment ----

// Enroll verifies p for an enrollment from peer carrying ident and, when it
// is accepted, creates the agent in the key's tenant and returns its id and
// credential (once). Every refusal is ErrRejected; other errors are
// transient.
func (s *Service) Enroll(ctx context.Context, p *inventoryv1.AutoEnrollProof, ident store.Identity, agentVersion string, peer netip.Addr) (agentID, credential string, err error) {
	if p == nil || !proof.ValidKeyID(p.GetKeyId()) || !proof.ValidNonce(p.GetNonce()) || !peer.IsValid() {
		return "", "", ErrRejected
	}
	peer = peer.Unmap()
	k, tenantOn, err := s.st.LookupAutoEnrollKey(ctx, p.GetKeyId())
	if errors.Is(err, repo.ErrNotFound) {
		return "", "", ErrRejected // unknown key: nothing to audit into
	}
	if err != nil {
		return "", "", err
	}
	now := s.now().UTC()
	refuse := func(reason string) (string, string, error) {
		s.refused(ctx, k, reason, peer, now)
		return "", "", ErrRejected
	}
	if !inNetworks(peer, k.AllowedCIDRs) {
		return refuse(ReasonAddress)
	}
	secret, err := s.env.Open(k.SecretSealed, keyAD(k.ID))
	if err != nil {
		return "", "", err
	}
	if !proof.Check(string(secret), p, proof.Identity{HardwareUUID: ident.HardwareUUID, MachineID: ident.MachineID, Hostname: ident.Hostname}) {
		return refuse(ReasonSignature)
	}
	if skew := now.Sub(time.Unix(p.GetTimestamp(), 0)); skew > s.window || skew < -s.window {
		return refuse(ReasonStale)
	}
	switch {
	case !tenantOn:
		return refuse(ReasonTenantDisabled)
	case !k.Enabled:
		return refuse(ReasonKeyDisabled)
	case k.Expired(now):
		return refuse(ReasonKeyExpired)
	case k.Exhausted():
		return refuse(ReasonKeyExhausted)
	case !s.allow(k.KeyID, now):
		return refuse(ReasonRateLimited)
	}
	agentID = store.NewID()
	credential, blob, err := enroll.IssueCredential(s.env, agentID)
	if err != nil {
		return "", "", err
	}
	row, err := audit.Row(audit.Event{TenantID: k.TenantID, EventType: audit.AgentAutoEnrolled, ActorKind: audit.ActorAgent, ActorID: agentID,
		SubjectKind: audit.SubjectAgent, SubjectID: agentID, Outcome: audit.OutcomeOK,
		Details: map[string]any{"key_id": k.KeyID, "address": peer.String(), "hostname": ident.Hostname, "agent_version": agentVersion}}, now)
	if err != nil {
		return "", "", err
	}
	err = s.st.EnrollWithAutoKey(ctx, store.AutoEnrollment{KeyUUID: k.ID, KeyID: k.KeyID, TenantID: k.TenantID, Nonce: p.GetNonce(), At: now,
		PruneBefore: now.Add(-2 * s.window), IP: peer.String(), Audit: row,
		Agent: store.Agent{ID: agentID, TenantID: k.TenantID, CredentialSealed: blob, EnrolledAt: now, LastSeen: now, AgentVersion: agentVersion,
			IdentityHint: ident.Hostname, EnrolledVia: store.EnrolledViaAuto, AutoEnrollKeyID: k.KeyID}})
	switch {
	case errors.Is(err, repo.ErrReplay):
		return refuse(ReasonReplay)
	case errors.Is(err, repo.ErrConflict):
		// The key or the switch changed since the lookup (or the limit was
		// reached by a concurrent enrollment).
		return refuse(ReasonStateChanged)
	case err != nil:
		return "", "", err
	}
	return agentID, credential, nil
}

// refused audits a refusal in the key's tenant, at most once per key and
// reason every refusalQuiet (a flood of bad requests must not flood the
// audit log). Audit failures are ignored: the caller is refused anyway.
func (s *Service) refused(ctx context.Context, k store.AutoEnrollKey, reason string, peer netip.Addr, now time.Time) {
	q := k.KeyID + "|" + reason + "|" + peer.String()
	s.mu.Lock()
	last, seen := s.quiet[q]
	if seen && now.Sub(last) < refusalQuiet {
		s.mu.Unlock()
		return
	}
	s.quiet[q] = now
	if len(s.quiet) > 10_000 {
		s.quiet = map[string]time.Time{q: now}
	}
	s.mu.Unlock()
	row, err := audit.Row(audit.Event{TenantID: k.TenantID, EventType: audit.AutoEnrollRefused, ActorKind: audit.ActorAgent, ActorID: "",
		SubjectKind: audit.SubjectAutoEnrollKey, SubjectID: k.ID, Outcome: audit.OutcomeRefused, Reason: reason,
		Details: map[string]any{"key_id": k.KeyID, "address": peer.String()}}, now)
	if err == nil {
		_ = s.st.AppendAudit(ctx, row)
	}
}

// allow records an accepted enrollment for keyID unless RatePerMinute were
// already reached in the last minute.
func (s *Service) allow(keyID string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	recent := s.rate[keyID][:0]
	for _, t := range s.rate[keyID] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= RatePerMinute {
		s.rate[keyID] = recent
		return false
	}
	s.rate[keyID] = append(recent, now)
	return true
}

// ---- helpers ----

// NormalizeCIDRs parses networks (a bare address is a single-host network),
// masks them, drops duplicates and refuses /0 ("allow everything" defeats
// the restriction). One to MaxCIDRs entries are required.
func NormalizeCIDRs(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		var p netip.Prefix
		if strings.Contains(v, "/") {
			var err error
			if p, err = netip.ParsePrefix(v); err != nil {
				return nil, invalid("allowed_cidrs", fmt.Sprintf("%q is not a network (e.g. 10.0.0.0/8)", v))
			}
		} else {
			a, err := netip.ParseAddr(v)
			if err != nil || a.Zone() != "" {
				return nil, invalid("allowed_cidrs", fmt.Sprintf("%q is not an address or network", v))
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-unmapShift(p)).Masked()
		if p.Bits() <= 0 {
			return nil, invalid("allowed_cidrs", "networks of every address (/0) are not allowed")
		}
		if s := p.String(); !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, invalid("allowed_cidrs", "at least one network is required")
	}
	if len(out) > MaxCIDRs {
		return nil, invalid("allowed_cidrs", fmt.Sprintf("at most %d networks", MaxCIDRs))
	}
	return out, nil
}

// unmapShift is the prefix length adjustment when an IPv4-mapped IPv6 prefix
// is turned into an IPv4 one.
func unmapShift(p netip.Prefix) int {
	if p.Addr().Is4In6() {
		if p.Bits() < 96 {
			return p.Bits() // shorter than the mapping itself: collapses to /0
		}
		return 96
	}
	return 0
}

func inNetworks(a netip.Addr, cidrs []string) bool {
	for _, c := range cidrs {
		if p, err := netip.ParsePrefix(c); err == nil && p.Contains(a) {
			return true
		}
	}
	return false
}

func checkName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || len([]rune(n)) > MaxNameLen {
		return "", invalid("name", fmt.Sprintf("1 to %d characters", MaxNameLen))
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return "", invalid("name", "control characters are not allowed")
		}
	}
	return n, nil
}

func checkExpiry(t *time.Time, now time.Time) error {
	if t != nil && !t.After(now) {
		return invalid("expires_at", "must be in the future")
	}
	return nil
}

func checkMax(n int) error {
	if n < 0 || n > MaxEnrollments {
		return invalid("max_enrollments", fmt.Sprintf("0 (unlimited) to %d", MaxEnrollments))
	}
	return nil
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// newKeyMaterial returns a public key id (ak_ + 24 hex) and a secret
// (aks_ + 43 base64url characters, 256 bits).
func newKeyMaterial() (keyID, secret string, err error) {
	b := make([]byte, 12+32)
	if _, err = randRead(b); err != nil {
		return "", "", err
	}
	return "ak_" + hex.EncodeToString(b[:12]), secretPrefix + base64.RawURLEncoding.EncodeToString(b[12:]), nil
}

func mapRepo(err error) error {
	switch {
	case errors.Is(err, repo.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, repo.ErrConflict):
		return ErrConflict
	}
	return err
}
