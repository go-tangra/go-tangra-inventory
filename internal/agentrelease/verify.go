package agentrelease

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"strings"
)

// Verify checks a release manifest in the documented order: the key id is
// in the keyring, the Ed25519 signature is valid over the exact manifest
// bytes, the manifest parses strictly and names the same key id. Only then
// is the manifest returned.
func (k Keyring) Verify(manifest, signature []byte, keyID string) (Manifest, error) {
	pub, ok := k[keyID]
	if !ok {
		return Manifest{}, fmt.Errorf("%w: %q", ErrUnknownKey, keyID)
	}
	if len(signature) != ed25519.SignatureSize || !ed25519.Verify(pub, manifest, signature) {
		return Manifest{}, ErrSignature
	}
	m, err := ParseManifest(manifest)
	if err != nil {
		return Manifest{}, err
	}
	if m.KeyID != keyID {
		return Manifest{}, fmt.Errorf("%w: manifest names key %q, signed with %q", ErrManifest, m.KeyID, keyID)
	}
	return m, nil
}

// EncodeSignature renders a signature as the content of agent-release.json.sig.
func EncodeSignature(sig []byte) string { return base64.StdEncoding.EncodeToString(sig) }

// DecodeSignature parses the content of agent-release.json.sig (base64).
func DecodeSignature(b []byte) ([]byte, error) {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, ErrSignature
	}
	return sig, nil
}

// ArtifactVerifier checks a streamed artifact against its manifest entry:
// writing more than the declared size fails at once, Finish checks the
// final size and sha256.
type ArtifactVerifier struct {
	want    Artifact
	h       hash.Hash
	written int64
}

// NewArtifactVerifier returns a verifier for a.
func NewArtifactVerifier(a Artifact) *ArtifactVerifier {
	return &ArtifactVerifier{want: a, h: sha256.New()}
}

// Write hashes p (io.Writer).
func (v *ArtifactVerifier) Write(p []byte) (int, error) {
	if v.written+int64(len(p)) > v.want.Size {
		return 0, fmt.Errorf("%w: more than %d bytes", ErrSize, v.want.Size)
	}
	v.written += int64(len(p))
	return v.h.Write(p)
}

// Written is the number of bytes accepted so far.
func (v *ArtifactVerifier) Written() int64 { return v.written }

// Finish checks the size and sha256 of everything written.
func (v *ArtifactVerifier) Finish() error {
	if v.written != v.want.Size {
		return fmt.Errorf("%w: %d of %d bytes", ErrSize, v.written, v.want.Size)
	}
	if hex.EncodeToString(v.h.Sum(nil)) != v.want.SHA256 {
		return ErrChecksum
	}
	return nil
}

// Hasher is a sha256 hash that reports its hex digest.
type Hasher struct{ hash.Hash }

// NewSHA256 returns a new Hasher.
func NewSHA256() Hasher { return Hasher{sha256.New()} }

// Hex is the lowercase hex digest.
func (h Hasher) Hex() string { return hex.EncodeToString(h.Sum(nil)) }
