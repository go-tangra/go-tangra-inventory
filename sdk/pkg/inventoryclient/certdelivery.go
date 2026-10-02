package inventoryclient

import (
	"context"
	"strings"
	"time"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
)

// Certificate delivery (feature 033): the deployer asks inventory to relay an
// lcm certificate to inventory agents. Only references cross this API —
// certificate ids, names, serials and fingerprints. No certificate or key
// material is ever sent or returned; agents fetch it themselves.
//
// gRPC errors are returned unchanged so callers can inspect status codes
// (e.g. FailedPrecondition when delivery is disabled or the certificate is
// not found, InvalidArgument naming the offending field).

// DeliveryRequest asks for a certificate delivery to the hosts selected by
// HostIDs and/or HostTags ("key" or "key=value"; a host must match all tags).
// IdempotencyKey (the deployer job id) makes the call replay-safe; with
// RearmFailed an existing delivery's failed, hook_failed and expired items are
// re-armed.
type DeliveryRequest struct {
	TenantID        string
	IdempotencyKey  string
	ConfigurationID string
	TargetID        string
	Trigger         string // manual | auto_deploy | retry
	CertificateID   string // lcm certificate id (a reference, not material)
	Name            string // install name on the host
	KeyPolicy       string // require | certificate_only
	HostIDs         []string
	HostTags        []string
	RearmFailed     bool
}

// Delivery is a certificate delivery and its per-host items. Created is false
// when the call was an idempotent replay of an existing delivery.
type Delivery struct {
	ID             string
	TenantID       string
	CertificateID  string
	Name           string
	KeyPolicy      string
	Created        bool
	CreatedAt      time.Time
	ExpiresAt      time.Time
	Items          []DeliveryItem
	UnknownHostIDs []string // requested ids not found in the tenant
}

// DeliveryItem is the delivery state of one host. State is one of pending,
// delivered, fetched, installed, unchanged, failed, hook_failed, unsupported,
// superseded, expired, cancelled ("" when unspecified). Fingerprint is the
// lowercase hex SHA-256 of the certificate; HookExitCode is -1 when no hook ran.
type DeliveryItem struct {
	ID           string
	HostID       string
	Hostname     string
	State        string
	Reason       string
	Serial       string
	Fingerprint  string
	AgentOnline  bool
	HookExitCode int
	Attempts     int
	UpdatedAt    time.Time
}

// Preview is the resolution of a host selection, without any writes.
// Truncated is set when the selection exceeded the server's host limit.
type Preview struct {
	Hosts          []TargetHost
	UnknownHostIDs []string
	Truncated      bool
}

// TargetHost is one selected host and its delivery capability: enabled,
// disabled_on_host, upgrade_required, not_supported_platform, no_agent or
// disabled_on_server.
type TargetHost struct {
	HostID      string
	Hostname    string
	OSName      string
	Tags        map[string]string
	AgentOnline bool
	Capability  string
}

// Verification compares what the selected hosts last reported for a
// certificate name against an expected fingerprint.
type Verification struct {
	Hosts   []HostCertificateStatus
	Matched int
	Total   int
}

// HostCertificateStatus is one host's verification result. Status is one of
// match, mismatch, failed, pending, missing, revoked; Fingerprint and Serial
// are what the host last reported. LastDeliveredAt is zero when never delivered.
type HostCertificateStatus struct {
	HostID          string
	Hostname        string
	Status          string
	Fingerprint     string
	Serial          string
	Reason          string
	LastDeliveredAt time.Time
}

// CreateCertificateDelivery creates (or idempotently replays) a delivery of a
// certificate reference to the selected hosts.
func (c *Client) CreateCertificateDelivery(ctx context.Context, r DeliveryRequest) (Delivery, error) {
	d, err := c.delivery.CreateCertificateDelivery(ctx, &invv1.CreateCertificateDeliveryRequest{
		TenantId: r.TenantID, IdempotencyKey: r.IdempotencyKey, ConfigurationId: r.ConfigurationID,
		TargetId: r.TargetID, Trigger: r.Trigger, CertificateId: r.CertificateID, Name: r.Name,
		KeyPolicy: r.KeyPolicy, Selector: hostSelector(r.HostIDs, r.HostTags), RearmFailed: r.RearmFailed,
	})
	if err != nil {
		return Delivery{}, err
	}
	return toDelivery(d), nil
}

// GetCertificateDelivery returns one delivery of the tenant by id.
func (c *Client) GetCertificateDelivery(ctx context.Context, tenantID, id string) (Delivery, error) {
	d, err := c.delivery.GetCertificateDelivery(ctx, &invv1.GetCertificateDeliveryRequest{TenantId: tenantID, Id: id})
	if err != nil {
		return Delivery{}, err
	}
	return toDelivery(d), nil
}

// PreviewCertificateTargets resolves a host selection (ids and/or tags) the
// same way CreateCertificateDelivery would, without creating anything.
func (c *Client) PreviewCertificateTargets(ctx context.Context, tenantID string, ids, tags []string) (Preview, error) {
	resp, err := c.delivery.PreviewCertificateTargets(ctx, &invv1.PreviewCertificateTargetsRequest{
		TenantId: tenantID, Selector: hostSelector(ids, tags),
	})
	if err != nil {
		return Preview{}, err
	}
	out := Preview{UnknownHostIDs: resp.GetUnknownHostIds(), Truncated: resp.GetTruncated()}
	for _, h := range resp.GetHosts() {
		out.Hosts = append(out.Hosts, TargetHost{
			HostID: h.GetHostId(), Hostname: h.GetHostname(), OSName: h.GetOsName(), Tags: h.GetTags(),
			AgentOnline: h.GetAgentOnline(), Capability: h.GetCapability(),
		})
	}
	return out, nil
}

// VerifyHostCertificates reports, per selected host, whether the certificate
// installed under name has the expected (lowercase hex SHA-256) fingerprint.
func (c *Client) VerifyHostCertificates(ctx context.Context, tenantID string, ids, tags []string, name, fingerprint string) (Verification, error) {
	resp, err := c.delivery.VerifyHostCertificates(ctx, &invv1.VerifyHostCertificatesRequest{
		TenantId: tenantID, Selector: hostSelector(ids, tags), Name: name, ExpectedFingerprintSha256: fingerprint,
	})
	if err != nil {
		return Verification{}, err
	}
	out := Verification{Matched: int(resp.GetMatched()), Total: int(resp.GetTotal())}
	for _, h := range resp.GetHosts() {
		out.Hosts = append(out.Hosts, HostCertificateStatus{
			HostID: h.GetHostId(), Hostname: h.GetHostname(), Status: h.GetStatus(),
			Fingerprint: h.GetFingerprintSha256(), Serial: h.GetSerial(), Reason: h.GetReason(),
			LastDeliveredAt: unixTime(h.GetLastDeliveredAt()),
		})
	}
	return out, nil
}

// MarkCertificateRevoked cancels the tenant's active delivery items for the
// certificate and flags hosts holding it as revoked. Idempotent.
func (c *Client) MarkCertificateRevoked(ctx context.Context, tenantID, certificateID string) (cancelledItems, flaggedHosts int, err error) {
	resp, err := c.delivery.MarkCertificateRevoked(ctx, &invv1.MarkCertificateRevokedRequest{
		TenantId: tenantID, CertificateId: certificateID,
	})
	if err != nil {
		return 0, 0, err
	}
	return int(resp.GetCancelledItems()), int(resp.GetFlaggedHosts()), nil
}

func hostSelector(ids, tags []string) *invv1.HostSelector {
	return &invv1.HostSelector{HostIds: ids, HostTags: tags}
}

func toDelivery(d *invv1.CertificateDelivery) Delivery {
	out := Delivery{
		ID: d.GetId(), TenantID: d.GetTenantId(), CertificateID: d.GetCertificateId(), Name: d.GetName(),
		KeyPolicy: d.GetKeyPolicy(), Created: d.GetCreated(), CreatedAt: unixTime(d.GetCreatedAt()),
		ExpiresAt: unixTime(d.GetExpiresAt()), UnknownHostIDs: d.GetUnknownHostIds(),
	}
	for _, it := range d.GetItems() {
		out.Items = append(out.Items, DeliveryItem{
			ID: it.GetId(), HostID: it.GetHostId(), Hostname: it.GetHostname(), State: deliveryStateString(it.GetState()),
			Reason: it.GetReason(), Serial: it.GetSerial(), Fingerprint: it.GetFingerprintSha256(),
			AgentOnline: it.GetAgentOnline(), HookExitCode: int(it.GetHookExitCode()), Attempts: int(it.GetAttempts()),
			UpdatedAt: unixTime(it.GetUpdatedAt()),
		})
	}
	return out
}

// deliveryStateString maps DELIVERY_STATE_X to "x"; unspecified and values
// unknown to this SDK map to "".
func deliveryStateString(s invv1.DeliveryState) string {
	name, ok := invv1.DeliveryState_name[int32(s)]
	if !ok || s == invv1.DeliveryState_DELIVERY_STATE_UNSPECIFIED {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(name, "DELIVERY_STATE_"))
}
