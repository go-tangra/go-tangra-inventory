package agentcerts

import (
	"encoding/json"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/certmaterial"
)

// Metadata is renewal/<name>.json: the v3 field names (go-tangra-client
// CertMetadata) plus the v4 identifiers (contracts/agent-config.md §3). It
// holds no material.
type Metadata struct {
	Name              string     `json:"name"`
	CommonName        string     `json:"common_name"`
	SerialNumber      string     `json:"serial_number"`
	Fingerprint       string     `json:"fingerprint"`
	IssuedAt          time.Time  `json:"issued_at"`
	ExpiresAt         time.Time  `json:"expires_at"`
	LastUpdated       time.Time  `json:"last_updated"`
	IssuerName        string     `json:"issuer_name"`
	DNSNames          []string   `json:"dns_names"`
	IPAddresses       []string   `json:"ip_addresses"`
	PreviousSerial    string     `json:"previous_serial,omitempty"`
	RenewalCount      int        `json:"renewal_count"`
	LastHookExecution *time.Time `json:"last_hook_execution,omitempty"`
	CertificateID     string     `json:"certificate_id"`
	ItemID            string     `json:"item_id"`
	Generation        string     `json:"generation"`
	HasKey            bool       `json:"has_key"`
	HookExitCode      *int       `json:"hook_exit_code,omitempty"`
}

// maxMetaBytes bounds the metadata file read back.
const maxMetaBytes = 64 << 10

// newMeta builds the metadata of a new generation. A renewal (another
// certificate was installed under the name) records the previous serial and
// increments the renewal count; a reinstall of the same certificate keeps
// them.
func (s *Store) newMeta(req Request, b certmaterial.Bundle, prev *Metadata, gen string, hasKey, renewal bool) Metadata {
	m := Metadata{Name: req.Name, CommonName: b.CommonName, SerialNumber: b.Serial, Fingerprint: b.Fingerprint,
		IssuedAt: b.NotBefore.UTC(), ExpiresAt: b.NotAfter.UTC(), LastUpdated: s.now().UTC(), IssuerName: b.Leaf.Issuer.CommonName,
		DNSNames: append([]string{}, b.DNSNames...), IPAddresses: append([]string{}, b.IPAddresses...),
		CertificateID: req.CertificateID, ItemID: req.ItemID, Generation: gen, HasKey: hasKey}
	if prev != nil {
		m.PreviousSerial, m.RenewalCount = prev.PreviousSerial, prev.RenewalCount
		if renewal {
			m.PreviousSerial, m.RenewalCount = prev.SerialNumber, prev.RenewalCount+1
		}
	}
	return m
}

// writeMeta replaces renewal/<name>.json atomically (temp file + rename).
func (s *Store) writeMeta(m Metadata, uid, gid int) error {
	tmp := dirRenewal + "/." + m.Name + ".json.tmp"
	if err := s.fs.RemoveAll(tmp); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(m, "", "  ") // cannot fail: plain fields only
	if err := s.fs.WriteFile(tmp, append(data, '\n'), s.cfg.CertMode, uid, gid); err != nil {
		return err
	}
	if err := s.fs.Rename(tmp, metaPath(m.Name)); err != nil {
		_ = s.fs.RemoveAll(tmp)
		return err
	}
	return s.fs.SyncDir(dirRenewal)
}

// readMeta reads renewal/<name>.json; nil when absent, unreadable or not
// about this name.
func (s *Store) readMeta(name string) *Metadata {
	data, err := s.fs.ReadFile(metaPath(name), maxMetaBytes)
	if err != nil {
		return nil
	}
	var m Metadata
	if err := json.Unmarshal(data, &m); err != nil || m.Name != name {
		return nil
	}
	return &m
}
