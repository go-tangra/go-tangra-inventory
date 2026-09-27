package agentrelease

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
)

// productionKeys is the keyring of release signing public keys, injected at
// build time so no key is kept in the source tree:
//
//	-ldflags "-X github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease.productionKeys=<id>:<base64 public key>[,<id>:<key>]"
//
// Release builds (CI, tags) inject the production key(s) from the
// AGENT_RELEASE_PUBLIC_KEYS setting; `make agent-release-dev` injects a
// locally generated development key whose id starts with "dev-"
// (scripts/check-release-binary.sh refuses such artifacts). A build without
// any key refuses every upgrade. Several ids allow key rotation: the old key
// stays until every agent has upgraded to a build that knows the new one.
var productionKeys = ""

// Keyring maps key ids to Ed25519 public keys.
type Keyring map[string]ed25519.PublicKey

// Compiled returns the keyring injected at build time (empty when none).
func Compiled() (Keyring, error) { return ParseKeyring(productionKeys) }

// ParseKeyring parses "<id>:<base64 public key>[,<id>:<key>...]".
func ParseKeyring(s string) (Keyring, error) {
	k := Keyring{}
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		id, enc, ok := strings.Cut(entry, ":")
		if !ok || !keyIDRE.MatchString(id) {
			return nil, fmt.Errorf("agentrelease: keyring entry %q: want <id>:<base64 key>", entry)
		}
		raw, err := base64.StdEncoding.DecodeString(enc)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("agentrelease: keyring entry %q: not an Ed25519 public key", id)
		}
		if _, dup := k[id]; dup {
			return nil, fmt.Errorf("agentrelease: keyring entry %q: duplicate key id", id)
		}
		k[id] = ed25519.PublicKey(raw)
	}
	return k, nil
}

// KeyIDs lists the key ids, sorted.
func (k Keyring) KeyIDs() []string {
	ids := make([]string, 0, len(k))
	for id := range k {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
