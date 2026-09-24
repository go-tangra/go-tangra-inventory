// Package sealed keeps lifecycle-management secrets encrypted at rest and out
// of every response: an envelope (per-row AES-256-GCM data key wrapped by the
// KEK, the scheme of the auth service) seals the JSON settings holding CA keys,
// issuer/ACME/DNS credentials, tenant-secret values, webhook signing secrets,
// and generated workload private keys; reads replace credential fields with a
// marker; provider errors are scrubbed of stored credential values
// (research R1, SR-001).
package sealed

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Marker replaces a credential field in every read; sending it back keeps
// the stored value.
const Marker = "__set__"

// MaxSettingsBytes bounds the clear JSON.
const MaxSettingsBytes = 8 << 10

// Errors.
var (
	ErrKEK      = errors.New("sealed: KEK must be 32 bytes")
	ErrTampered = errors.New("sealed: ciphertext rejected")
	ErrTooLarge = errors.New("sealed: settings exceed 8 KiB")
)

var randReader io.Reader = rand.Reader

// Envelope seals and opens settings.
type Envelope struct{ kek cipher.AEAD }

// NewEnvelope requires a 32-byte KEK.
func NewEnvelope(kek []byte) (*Envelope, error) {
	if len(kek) != 32 {
		return nil, ErrKEK
	}
	return &Envelope{kek: gcm(kek)}, nil
}

// gcm builds AES-256-GCM over a 32-byte key; neither constructor can fail
// for a key of that length.
func gcm(key []byte) cipher.AEAD {
	block, _ := aes.NewCipher(key)
	g, _ := cipher.NewGCM(block)
	return g
}

// LoadKEK reads the key from a file (base64 or raw 32 bytes, mode 0600
// recommended) or an environment variable (base64).
func LoadKEK(source, path, env string) ([]byte, error) {
	var raw []byte
	switch source {
	case "file":
		b, err := os.ReadFile(path) // #nosec G304 -- operator-supplied key path
		if err != nil {
			return nil, fmt.Errorf("sealed: kek: %w", err)
		}
		raw = b
	case "env":
		v := os.Getenv(env)
		if v == "" {
			return nil, fmt.Errorf("sealed: kek: environment variable %q is empty", env)
		}
		raw = []byte(v)
	default:
		return nil, fmt.Errorf("sealed: kek: unknown source %q", source)
	}
	return decodeKEK(raw)
}

func decodeKEK(raw []byte) ([]byte, error) {
	trimmed := strings.TrimSpace(string(raw))
	if b, err := base64.StdEncoding.DecodeString(trimmed); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(trimmed); err == nil && len(b) == 32 {
		return b, nil
	}
	if len(raw) == 32 {
		return raw, nil
	}
	return nil, ErrKEK
}

// Seal encrypts plaintext bound to associated data (see the AD* helpers).
// Layout: nonce | wrapped DEK | nonce | ciphertext.
func (e *Envelope) Seal(plaintext, ad []byte) ([]byte, error) {
	if len(plaintext) > MaxSettingsBytes {
		return nil, ErrTooLarge
	}
	dek := make([]byte, 32)
	n1 := make([]byte, e.kek.NonceSize())
	n2 := make([]byte, e.kek.NonceSize())
	if _, err := io.ReadFull(randReader, dek); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(randReader, n1); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(randReader, n2); err != nil {
		return nil, err
	}
	g := gcm(dek)
	out := append([]byte{}, n1...)
	out = append(out, e.kek.Seal(nil, n1, dek, ad)...)
	out = append(out, n2...)
	return append(out, g.Seal(nil, n2, plaintext, ad)...), nil
}

// Open decrypts a value produced by Seal with the same associated data.
func (e *Envelope) Open(blob, ad []byte) ([]byte, error) {
	ns := e.kek.NonceSize()
	wrapLen := 32 + e.kek.Overhead()
	if len(blob) < ns+wrapLen+ns+16 {
		return nil, ErrTampered
	}
	dek, err := e.kek.Open(nil, blob[:ns], blob[ns:ns+wrapLen], ad)
	if err != nil {
		return nil, ErrTampered
	}
	rest := blob[ns+wrapLen:]
	pt, err := gcm(dek).Open(nil, rest[:ns], rest[ns:], ad)
	if err != nil {
		return nil, ErrTampered
	}
	return pt, nil
}

// ADCA is the associated data of a CA key's settings.
func ADCA(id string) []byte { return []byte("ca:" + id) }

// ADIssuer is the associated data of an issuer/ACME/DNS credential's settings.
func ADIssuer(id string) []byte { return []byte("issuer:" + id) }

// ADSecret is the associated data of a tenant-secret value's settings.
func ADSecret(id string) []byte { return []byte("secret:" + id) }

// ADWebhook is the associated data of a webhook signing secret's settings.
func ADWebhook(id string) []byte { return []byte("webhook:" + id) }

// ADCertKey is the associated data of a generated workload private key's settings.
func ADCertKey(id string) []byte { return []byte("certkey:" + id) }

// ADTarget is the associated data of a target's settings.
func ADTarget(id string) []byte { return []byte("target:" + id) }

// Settings is a decoded settings object.
type Settings map[string]any

// Decode parses settings JSON (an object, ≤ 8 KiB).
func Decode(raw []byte) (Settings, error) {
	if len(raw) > MaxSettingsBytes {
		return nil, ErrTooLarge
	}
	var s Settings
	if err := json.Unmarshal(raw, &s); err != nil || s == nil {
		return nil, errors.New("sealed: settings must be a JSON object")
	}
	return s, nil
}

// Encode serialises settings.
func Encode(s Settings) ([]byte, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxSettingsBytes {
		return nil, ErrTooLarge
	}
	return b, nil
}

// Redact returns a copy with every credential field replaced by Marker (when
// set and non-empty) or removed (when empty) — what clients may see.
func Redact(s Settings, secretFields []string) Settings {
	out := make(Settings, len(s))
	for k, v := range s {
		out[k] = v
	}
	for _, f := range secretFields {
		if v, ok := out[f]; ok {
			if str, _ := v.(string); str != "" {
				out[f] = Marker
			} else {
				delete(out, f)
			}
		}
	}
	return out
}

// Public returns the non-secret fields only (listings, backups without credentials).
func Public(s Settings, secretFields []string) Settings {
	out := make(Settings, len(s))
	for k, v := range s {
		out[k] = v
	}
	for _, f := range secretFields {
		delete(out, f)
	}
	return out
}

// Merge applies an incoming update onto the stored settings: a credential
// field sent as Marker or omitted keeps the stored value, "" clears it, any
// other value replaces it. Non-secret fields come from incoming as given.
func Merge(stored, incoming Settings, secretFields []string) Settings {
	out := make(Settings, len(incoming))
	for k, v := range incoming {
		out[k] = v
	}
	for _, f := range secretFields {
		in, present := incoming[f]
		str, _ := in.(string)
		switch {
		case !present || str == Marker:
			if old, ok := stored[f]; ok {
				out[f] = old
			} else {
				delete(out, f)
			}
		case str == "":
			delete(out, f)
		}
	}
	return out
}

// Scrub removes every stored credential value from text (provider errors
// sometimes echo the AUTH exchange) and keeps only the first line.
func Scrub(text string, stored Settings, secretFields []string) string {
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		text = text[:i]
	}
	for _, f := range secretFields {
		if v, ok := stored[f].(string); ok && v != "" {
			text = strings.ReplaceAll(text, v, "[REDACTED]")
			if enc := base64.StdEncoding.EncodeToString([]byte(v)); enc != "" {
				text = strings.ReplaceAll(text, enc, "[REDACTED]")
			}
		}
	}
	if len(text) > 1024 {
		text = text[:1024]
	}
	return text
}

// ADConfig is the associated data of a target-configuration credential blob.
func ADConfig(id string) []byte { return []byte("config:" + id) }
