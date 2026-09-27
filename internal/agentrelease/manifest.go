package agentrelease

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"
)

// Bounds of a release manifest (contracts/inventory-grpc.md §4).
const (
	MaxManifestBytes = 64 << 10
	MaxArtifacts     = 16
	MaxArtifactSize  = 150 << 20
	ManifestSchema   = 1
)

// Files of a release directory.
const (
	ManifestFile  = "agent-release.json"
	SignatureFile = "agent-release.json.sig"
)

// Verification errors. Callers map them to the closed reason codes
// (signature_invalid, unknown_key, platform_mismatch, size_mismatch,
// checksum_mismatch, version_mismatch).
var (
	ErrManifest   = errors.New("agentrelease: invalid manifest")
	ErrSignature  = errors.New("agentrelease: signature invalid")
	ErrUnknownKey = errors.New("agentrelease: unknown signing key")
	ErrPlatform   = errors.New("agentrelease: no artifact for the platform")
	ErrSize       = errors.New("agentrelease: artifact size mismatch")
	ErrChecksum   = errors.New("agentrelease: artifact checksum mismatch")
)

var (
	releaseRE = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)
	keyIDRE   = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	fileRE    = regexp.MustCompile(`^[A-Za-z0-9._+~-]{1,128}$`)
	sha256RE  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Artifact is one platform build of a release.
type Artifact struct {
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	InstallType string `json:"install_type"`
	File        string `json:"file"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
}

// Platform returns the artifact's platform.
func (a Artifact) Platform() Platform {
	return Platform{OS: a.OS, Arch: a.Arch, InstallType: a.InstallType}
}

// Manifest is the signed description of a release.
type Manifest struct {
	Schema    int        `json:"schema"`
	Version   string     `json:"version"`
	CreatedAt time.Time  `json:"created_at"`
	KeyID     string     `json:"key_id"`
	Artifacts []Artifact `json:"artifacts"`
}

// Encode renders the manifest as the canonical JSON that gets signed (a
// struct of strings, numbers and a time always marshals).
func (m Manifest) Encode() []byte {
	b, _ := json.MarshalIndent(m, "", "  ")
	return append(b, '\n')
}

// ParseManifest parses and validates a manifest strictly: at most 64 KiB,
// no unknown fields or trailing data, schema 1, a release version, a key id,
// 1-16 artifacts with unique platforms, valid file names, sizes of 1 byte to
// 150 MiB and lowercase hex sha256.
func ParseManifest(b []byte) (Manifest, error) {
	var m Manifest
	if len(b) > MaxManifestBytes {
		return m, fmt.Errorf("%w: larger than %d bytes", ErrManifest, MaxManifestBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrManifest, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Manifest{}, fmt.Errorf("%w: trailing data", ErrManifest)
	}
	if err := m.validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func (m Manifest) validate() error {
	switch {
	case m.Schema != ManifestSchema:
		return fmt.Errorf("%w: schema %d", ErrManifest, m.Schema)
	case !IsRelease(m.Version):
		return fmt.Errorf("%w: version %q", ErrManifest, m.Version)
	case !keyIDRE.MatchString(m.KeyID):
		return fmt.Errorf("%w: key id %q", ErrManifest, m.KeyID)
	case m.CreatedAt.IsZero():
		return fmt.Errorf("%w: created_at missing", ErrManifest)
	case len(m.Artifacts) == 0 || len(m.Artifacts) > MaxArtifacts:
		return fmt.Errorf("%w: %d artifacts", ErrManifest, len(m.Artifacts))
	}
	seen := map[Platform]bool{}
	for _, a := range m.Artifacts {
		p := a.Platform()
		switch {
		case !p.Valid():
			return fmt.Errorf("%w: platform %s", ErrManifest, p)
		case seen[p]:
			return fmt.Errorf("%w: duplicate platform %s", ErrManifest, p)
		case !fileRE.MatchString(a.File) || a.File == "." || a.File == "..":
			return fmt.Errorf("%w: file %q", ErrManifest, a.File)
		case a.Size < 1 || a.Size > MaxArtifactSize:
			return fmt.Errorf("%w: size %d", ErrManifest, a.Size)
		case !sha256RE.MatchString(a.SHA256):
			return fmt.Errorf("%w: sha256 of %s", ErrManifest, a.File)
		}
		seen[p] = true
	}
	return nil
}

// Artifact returns the entry for platform p.
func (m Manifest) Artifact(p Platform) (Artifact, error) {
	for _, a := range m.Artifacts {
		if a.Platform() == p {
			return a, nil
		}
	}
	return Artifact{}, fmt.Errorf("%w: %s", ErrPlatform, p)
}
