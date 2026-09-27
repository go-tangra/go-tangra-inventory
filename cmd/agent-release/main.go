// Command agent-release is the release tooling of agent self-upgrade
// (feature 023). It is not shipped in any image.
//
//	agent-release keygen -out-private <file> -key-id <id>
//	agent-release sign   -key-env AGENT_RELEASE_SIGNING_KEY -key-id <id> -version <v> -dir dist/agent
//	agent-release verify -dir dist/agent [-version <v>] [-keyring <id>:<base64 public key>]
//
// keygen writes a new Ed25519 seed (base64, 0600, never overwritten) and
// prints the keyring entry "<id>:<base64 public key>" to inject into builds
// (AGENT_RELEASE_PUBLIC_KEYS). sign writes agent-release.json and
// agent-release.json.sig over the eight agent artifacts in -dir with the
// seed read from the named environment variable — never from a flag value
// or a file. verify checks a release directory against the compiled keyring
// (or -keyring): signature, manifest, version and every artifact's size and
// sha256. Standard library only.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

var envNameRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)

// artifactPatterns maps each platform to the file name pattern of its build
// output (nfpm names deb/rpm packages after the version and architecture).
var artifactPatterns = []struct {
	p    agentrelease.Platform
	glob string
}{
	{agentrelease.Platform{OS: "linux", Arch: "amd64", InstallType: "deb"}, "*_amd64.deb"},
	{agentrelease.Platform{OS: "linux", Arch: "arm64", InstallType: "deb"}, "*_arm64.deb"},
	{agentrelease.Platform{OS: "linux", Arch: "amd64", InstallType: "rpm"}, "*.x86_64.rpm"},
	{agentrelease.Platform{OS: "linux", Arch: "arm64", InstallType: "rpm"}, "*.aarch64.rpm"},
	{agentrelease.Platform{OS: "linux", Arch: "amd64", InstallType: "binary"}, "inventory-agent-linux-amd64"},
	{agentrelease.Platform{OS: "linux", Arch: "arm64", InstallType: "binary"}, "inventory-agent-linux-arm64"},
	{agentrelease.Platform{OS: "windows", Arch: "amd64", InstallType: "binary"}, "inventory-agent-windows-amd64.exe"},
	{agentrelease.Platform{OS: "windows", Arch: "arm64", InstallType: "binary"}, "inventory-agent-windows-arm64.exe"},
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: agent-release keygen|sign|verify [flags]")
		return 2
	}
	var err error
	switch args[0] {
	case "keygen":
		err = keygen(args[1:], stdout)
	case "sign":
		err = sign(args[1:], getenv, stdout)
	case "verify":
		err = verify(args[1:], stdout)
	default:
		fmt.Fprintf(stderr, "agent-release: unknown command %q\n", args[0])
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "agent-release:", err)
		var ue usageError
		if errors.As(err, &ue) {
			return 2
		}
		return 1
	}
	return 0
}

type usageError struct{ error }

func parse(fs *flag.FlagSet, args []string) error {
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}
	return nil
}

func keygen(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	out := fs.String("out-private", "", "file for the new private key seed (base64, mode 0600)")
	keyID := fs.String("key-id", "", "key id, ^[a-z0-9-]{1,32}$")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *out == "" || !regexp.MustCompile(`^[a-z0-9-]{1,32}$`).MatchString(*keyID) {
		return usageError{errors.New("keygen needs -out-private <file> and -key-id <id>")}
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- operator-chosen output path
	if err != nil {
		return fmt.Errorf("keygen: %w", err)
	}
	if _, err := fmt.Fprintln(f, base64.StdEncoding.EncodeToString(priv.Seed())); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s:%s\n", *keyID, base64.StdEncoding.EncodeToString(pub))
	return nil
}

func sign(args []string, getenv func(string) string, stdout io.Writer) error {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	keyEnv := fs.String("key-env", "", "name of the environment variable holding the base64 private key seed")
	keyID := fs.String("key-id", "", "key id recorded in the manifest")
	version := fs.String("version", "", "release version")
	dir := fs.String("dir", "", "directory with the agent artifacts")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *keyEnv == "" || *keyID == "" || *version == "" || *dir == "" {
		return usageError{errors.New("sign needs -key-env, -key-id, -version and -dir")}
	}
	if !envNameRE.MatchString(*keyEnv) {
		return errors.New("sign: -key-env takes an environment variable name, not a key")
	}
	secret := strings.TrimSpace(getenv(*keyEnv))
	if secret == "" {
		return fmt.Errorf("sign: %s is not set (the signing key exists only in the protected release environment)", *keyEnv)
	}
	seed, err := base64.StdEncoding.DecodeString(secret)
	if err != nil || len(seed) != ed25519.SeedSize {
		return fmt.Errorf("sign: %s does not hold a base64 Ed25519 seed", *keyEnv)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	m := agentrelease.Manifest{Schema: agentrelease.ManifestSchema, Version: *version, KeyID: *keyID,
		CreatedAt: time.Now().UTC().Truncate(time.Second)}
	for _, ap := range artifactPatterns {
		matches, _ := filepath.Glob(filepath.Join(*dir, ap.glob))
		switch len(matches) {
		case 0:
			return fmt.Errorf("sign: no artifact for %s (%s) in %s", ap.p, ap.glob, *dir)
		case 1:
		default:
			return fmt.Errorf("sign: ambiguous artifacts for %s: %v", ap.p, matches)
		}
		a, err := describe(matches[0], ap.p)
		if err != nil {
			return err
		}
		m.Artifacts = append(m.Artifacts, a)
	}
	b := m.Encode()
	if _, err := agentrelease.ParseManifest(b); err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	if err := os.WriteFile(filepath.Join(*dir, agentrelease.ManifestFile), b, 0o644); err != nil { // #nosec G306 -- public release metadata
		return err
	}
	sig := agentrelease.EncodeSignature(ed25519.Sign(priv, b)) + "\n"
	if err := os.WriteFile(filepath.Join(*dir, agentrelease.SignatureFile), []byte(sig), 0o644); err != nil { // #nosec G306 -- public signature
		return err
	}
	fmt.Fprintf(stdout, "signed %s with key %s: %d artifacts\n", *version, *keyID, len(m.Artifacts))
	return nil
}

// describe hashes one artifact file.
func describe(path string, p agentrelease.Platform) (agentrelease.Artifact, error) {
	f, err := os.Open(path) // #nosec G304 -- file found in the release directory
	if err != nil {
		return agentrelease.Artifact{}, err
	}
	defer f.Close()
	h := agentrelease.NewSHA256()
	n, err := io.Copy(h, f)
	if err != nil {
		return agentrelease.Artifact{}, err
	}
	return agentrelease.Artifact{OS: p.OS, Arch: p.Arch, InstallType: p.InstallType, File: filepath.Base(path), Size: n, SHA256: h.Hex()}, nil
}

func verify(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	dir := fs.String("dir", "", "release directory")
	version := fs.String("version", "", "expected version (optional)")
	keyringFlag := fs.String("keyring", "", "public keyring <id>:<base64> (default: the keyring compiled into this binary)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *dir == "" {
		return usageError{errors.New("verify needs -dir")}
	}
	keys, err := agentrelease.Compiled()
	if *keyringFlag != "" {
		keys, err = agentrelease.ParseKeyring(*keyringFlag)
	}
	if err != nil {
		return err
	}
	manifest, err := os.ReadFile(filepath.Join(*dir, agentrelease.ManifestFile)) // #nosec G304 -- release directory
	if err != nil {
		return err
	}
	sigRaw, err := os.ReadFile(filepath.Join(*dir, agentrelease.SignatureFile)) // #nosec G304 -- release directory
	if err != nil {
		return err
	}
	sig, err := agentrelease.DecodeSignature(sigRaw)
	if err != nil {
		return err
	}
	m, err := agentrelease.ParseManifest(manifest)
	if err != nil {
		return err
	}
	if _, err := keys.Verify(manifest, sig, m.KeyID); err != nil {
		return err
	}
	if *version != "" && m.Version != *version {
		return fmt.Errorf("verify: manifest version %s, want %s", m.Version, *version)
	}
	for _, a := range m.Artifacts {
		f, err := os.Open(filepath.Join(*dir, a.File)) // #nosec G304 -- name validated by the manifest parser
		if err != nil {
			return err
		}
		v := agentrelease.NewArtifactVerifier(a)
		_, err = io.Copy(v, f)
		_ = f.Close()
		if err == nil {
			err = v.Finish()
		}
		if err != nil {
			return fmt.Errorf("verify: %s: %w", a.File, err)
		}
	}
	fmt.Fprintf(stdout, "release %s verified (key %s, %d artifacts)\n", m.Version, m.KeyID, len(m.Artifacts))
	return nil
}
