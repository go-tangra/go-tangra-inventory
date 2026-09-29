package autoenroll

import (
	"crypto/rand"
	"errors"
	"testing"
	"time"

	inventoryv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
)

const (
	key   = "k3y-material-of-some-length-0123456789abcd"
	keyID = "ak_0123456789abcdef01234567"
)

var ident = Identity{HardwareUUID: "hw-1", MachineID: "m-1", Hostname: "web01"}

func TestProofRoundTrip(t *testing.T) {
	now := time.Unix(1790000000, 0)
	p, err := NewProof(key, keyID, ident, now)
	if err != nil || p.GetTimestamp() != now.Unix() || !ValidNonce(p.GetNonce()) {
		t.Fatalf("proof %v %v", p, err)
	}
	if !Check(key, p, ident) {
		t.Fatal("valid proof refused")
	}
	req := &inventoryv1.EnrollRequest{Identity: &inventoryv1.Identity{HardwareUuid: "hw-1", MachineId: "m-1", Hostname: "web01"}}
	if FromRequest(req) != ident {
		t.Fatal("FromRequest")
	}
}

func TestCheckRefuses(t *testing.T) {
	now := time.Unix(1790000000, 0)
	p, _ := NewProof(key, keyID, ident, now)
	cases := map[string]func(*inventoryv1.AutoEnrollProof) (string, Identity){
		"wrong key":      func(q *inventoryv1.AutoEnrollProof) (string, Identity) { return key + "x", ident },
		"other host":     func(q *inventoryv1.AutoEnrollProof) (string, Identity) { i := ident; i.Hostname = "db01"; return key, i },
		"newline":        func(q *inventoryv1.AutoEnrollProof) (string, Identity) { i := ident; i.Hostname = "a\nb"; return key, i },
		"timestamp":      func(q *inventoryv1.AutoEnrollProof) (string, Identity) { q.Timestamp++; return key, ident },
		"zero timestamp": func(q *inventoryv1.AutoEnrollProof) (string, Identity) { q.Timestamp = 0; return key, ident },
		"nonce":          func(q *inventoryv1.AutoEnrollProof) (string, Identity) { q.Nonce = "short"; return key, ident },
		"key id":         func(q *inventoryv1.AutoEnrollProof) (string, Identity) { q.KeyId = "ak_nothex"; return key, ident },
		"signature":      func(q *inventoryv1.AutoEnrollProof) (string, Identity) { q.Signature = "AAAA"; return key, ident },
	}
	for name, mut := range cases {
		q := &inventoryv1.AutoEnrollProof{KeyId: p.KeyId, Timestamp: p.Timestamp, Nonce: p.Nonce, Signature: p.Signature}
		k, id := mut(q)
		if Check(k, q, id) {
			t.Errorf("%s accepted", name)
		}
	}
	if Check(key, nil, ident) {
		t.Error("nil proof accepted")
	}
}

// The canonical message and signature are fixed (other implementations,
// e.g. go-tangra-client, must produce the same bytes).
func TestKnownVector(t *testing.T) {
	m := string(Message(keyID, 1790000000, "nonce-nonce-nonce-1", ident))
	want := "go-tangra-inventory/auto-enroll/v1\nak_0123456789abcdef01234567\n1790000000\nnonce-nonce-nonce-1\nhw-1\nm-1\nweb01"
	if m != want {
		t.Fatalf("message %q", m)
	}
	// Independently computed (Python hmac/hashlib).
	if got := Sign(key, keyID, 1790000000, "nonce-nonce-nonce-1", ident); got != "fERQ8IOeYBihTsCtgvxfo54x4GygBcGDL1Wkw7behNo" {
		t.Fatalf("signature %s", got)
	}
}

func TestNonceError(t *testing.T) {
	defer func() { randRead = rand.Read }()
	randRead = func([]byte) (int, error) { return 0, errors.New("no entropy") }
	if _, err := NewProof(key, keyID, ident, time.Now()); err == nil {
		t.Fatal("entropy error swallowed")
	}
}
