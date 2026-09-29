// Package autoenroll builds and checks the proof an inventory agent (or a
// tool such as go-tangra-client acting for it) presents to enroll with a
// tenant's auto-enrollment key instead of a single-use enrollment token
// (feature 029).
//
// The key never travels: the proof is an HMAC-SHA256 over a canonical
// message binding the key id, a unix timestamp, a single-use nonce and the
// identity sent in the same EnrollRequest. The server additionally checks
// that the timestamp is recent, that the nonce was not seen before and that
// the caller's address lies inside the key's allowed networks.
package autoenroll

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"strconv"
	"strings"
	"time"

	inventoryv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
)

// Version prefixes every canonical message.
const Version = "go-tangra-inventory/auto-enroll/v1"

var (
	keyIDRE = regexp.MustCompile(`^ak_[0-9a-f]{24}$`)
	nonceRE = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
)

// ValidKeyID reports whether id has the shape of an auto-enrollment key id.
func ValidKeyID(id string) bool { return keyIDRE.MatchString(id) }

// ValidNonce reports whether n is an acceptable nonce.
func ValidNonce(n string) bool { return nonceRE.MatchString(n) }

// Identity is the part of the enrollment request covered by the proof.
type Identity struct {
	HardwareUUID string
	MachineID    string
	Hostname     string
}

// FromRequest extracts the covered identity of an EnrollRequest.
func FromRequest(req *inventoryv1.EnrollRequest) Identity {
	id := req.GetIdentity()
	return Identity{HardwareUUID: id.GetHardwareUuid(), MachineID: id.GetMachineId(), Hostname: id.GetHostname()}
}

// Message is the canonical message: Version, key id, timestamp, nonce and
// the three identity fields, one per line. Callers must reject values that
// contain a newline (Check does) so fields cannot be shifted.
func Message(keyID string, ts int64, nonce string, id Identity) []byte {
	return []byte(strings.Join([]string{Version, keyID, strconv.FormatInt(ts, 10), nonce, id.HardwareUUID, id.MachineID, id.Hostname}, "\n"))
}

// Sign returns base64url(HMAC-SHA256(key, Message(...))) without padding.
func Sign(key, keyID string, ts int64, nonce string, id Identity) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write(Message(keyID, ts, nonce, id))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// randRead is a test seam.
var randRead = rand.Read

// NewNonce returns a fresh 24-character random nonce.
func NewNonce() (string, error) {
	b := make([]byte, 18)
	if _, err := randRead(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewProof builds the proof for an EnrollRequest carrying id, at now.
func NewProof(key, keyID string, id Identity, now time.Time) (*inventoryv1.AutoEnrollProof, error) {
	nonce, err := NewNonce()
	if err != nil {
		return nil, err
	}
	ts := now.Unix()
	return &inventoryv1.AutoEnrollProof{KeyId: keyID, Timestamp: ts, Nonce: nonce, Signature: Sign(key, keyID, ts, nonce, id)}, nil
}

// Check reports whether p is well formed and its signature matches key and
// id (constant time). Freshness, nonce reuse and the caller's address are
// the server's business.
func Check(key string, p *inventoryv1.AutoEnrollProof, id Identity) bool {
	if p == nil || !ValidKeyID(p.GetKeyId()) || !ValidNonce(p.GetNonce()) || p.GetTimestamp() <= 0 {
		return false
	}
	for _, v := range []string{id.HardwareUUID, id.MachineID, id.Hostname} {
		if strings.ContainsAny(v, "\r\n") {
			return false
		}
	}
	want := Sign(key, p.GetKeyId(), p.GetTimestamp(), p.GetNonce(), id)
	return hmac.Equal([]byte(want), []byte(p.GetSignature()))
}
