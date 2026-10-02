package certmaterial

import (
	"bytes"
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"time"
)

// Bundle bounds (research D17).
const (
	MaxCertBytes   = 64 << 10
	MaxChainBytes  = 256 << 10
	MaxKeyBytes    = 16 << 10
	MaxBundleBytes = 512 << 10
	MaxChainCerts  = 10
	MaxSANs        = 100
)

// Error is a bundle validation failure. Reason is the closed reason code
// reported for the delivery item; messages never contain material.
type Error struct {
	Reason string
	msg    string
}

func (e *Error) Error() string { return "certmaterial: " + e.msg }

// Is matches errors of the same reason and message (the sentinels below).
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Reason == e.Reason && t.msg == e.msg
}

// Reasons.
const (
	ReasonBundleTooLarge      = "bundle_too_large"
	ReasonInvalidBundle       = "invalid_bundle"
	ReasonKeyMismatch         = "key_mismatch"
	ReasonCertificateNotValid = "certificate_not_valid"
)

// Sentinel errors (compare with errors.Is; Reason maps to the item reason).
var (
	ErrTooLarge      = &Error{ReasonBundleTooLarge, "bundle exceeds the size limits"}
	ErrNoCertificate = &Error{ReasonInvalidBundle, "no certificate"}
	ErrMalformed     = &Error{ReasonInvalidBundle, "malformed PEM"}
	ErrTooManyCerts  = &Error{ReasonInvalidBundle, "too many chain certificates"}
	ErrTooManySANs   = &Error{ReasonInvalidBundle, "too many subject alternative names"}
	ErrBadKey        = &Error{ReasonInvalidBundle, "unsupported, encrypted or malformed private key"}
	ErrKeyRequired   = &Error{ReasonInvalidBundle, "private key required"}
	ErrKeyMismatch   = &Error{ReasonKeyMismatch, "private key does not match the certificate"}
	ErrNotValid      = &Error{ReasonCertificateNotValid, "certificate is not valid now"}
)

// Reason returns the closed reason code of err ("invalid_bundle" for
// errors that are not bundle errors).
func Reason(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Reason
	}
	return ReasonInvalidBundle
}

// Options control ParseBundle.
type Options struct {
	// RequireKey refuses a bundle without a private key.
	RequireKey bool
	// Now is the clock of the validity check (default time.Now).
	Now func() time.Time
}

// Bundle is a validated certificate bundle. CertPEM, ChainPEM and
// FullChainPEM are re-encoded from the parsed DER (headers and text outside
// the PEM blocks dropped); KeyPEM is the key block as delivered.
type Bundle struct {
	Leaf         *x509.Certificate
	Chain        []*x509.Certificate
	CertPEM      []byte
	ChainPEM     []byte
	FullChainPEM []byte
	KeyPEM       []byte
	HasKey       bool
	Fingerprint  string // sha256 of the leaf DER, lowercase hex
	Serial       string // lowercase hex
	CommonName   string
	DNSNames     []string
	IPAddresses  []string
	NotBefore    time.Time
	NotAfter     time.Time
}

// ParseBundle validates a leaf certificate (cert: the leaf, optionally
// followed by chain certificates), its chain and an optional private key:
// sizes, PEM structure without trailing data, at most 10 chain
// certificates, at most 100 DNS and 100 IP SANs, an unencrypted
// PKCS#8/PKCS#1/SEC1 key matching the leaf's public key and the validity
// window. It does no I/O.
func ParseBundle(cert, chain, key []byte, opts Options) (Bundle, error) {
	if len(cert)+len(chain)+len(key) > MaxBundleBytes || len(cert) > MaxCertBytes || len(chain) > MaxChainBytes || len(key) > MaxKeyBytes {
		return Bundle{}, ErrTooLarge
	}
	certs, err := parseCerts(cert)
	if err != nil {
		return Bundle{}, err
	}
	if len(certs) == 0 {
		return Bundle{}, ErrNoCertificate
	}
	rest, err := parseCerts(chain)
	if err != nil {
		return Bundle{}, err
	}
	b := Bundle{Leaf: certs[0], Chain: append(certs[1:], rest...)}
	if len(b.Chain) > MaxChainCerts {
		return Bundle{}, ErrTooManyCerts
	}
	if len(b.Leaf.DNSNames) > MaxSANs || len(b.Leaf.IPAddresses) > MaxSANs {
		return Bundle{}, ErrTooManySANs
	}
	if len(bytes.TrimSpace(key)) > 0 {
		signer, err := parseKey(key)
		if err != nil {
			return Bundle{}, err
		}
		pub, ok := b.Leaf.PublicKey.(interface{ Equal(crypto.PublicKey) bool })
		if !ok || !pub.Equal(signer.Public()) {
			return Bundle{}, ErrKeyMismatch
		}
		b.KeyPEM, b.HasKey = key, true
	} else if opts.RequireKey {
		return Bundle{}, ErrKeyRequired
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	if t := now(); t.Before(b.Leaf.NotBefore) || t.After(b.Leaf.NotAfter) {
		return Bundle{}, ErrNotValid
	}
	b.CertPEM = encodeCerts(b.Leaf)
	b.ChainPEM = encodeCerts(b.Chain...)
	b.FullChainPEM = FullChain(b.CertPEM, b.ChainPEM)
	b.Fingerprint = Fingerprint(b.Leaf.Raw)
	b.Serial = b.Leaf.SerialNumber.Text(16)
	b.CommonName = b.Leaf.Subject.CommonName
	b.DNSNames = append([]string(nil), b.Leaf.DNSNames...)
	for _, ip := range b.Leaf.IPAddresses {
		b.IPAddresses = append(b.IPAddresses, ip.String())
	}
	b.NotBefore, b.NotAfter = b.Leaf.NotBefore, b.Leaf.NotAfter
	return b, nil
}

// parseCerts decodes a sequence of CERTIFICATE blocks; anything else
// (another block type, PEM headers, non-whitespace outside the blocks) is
// malformed.
func parseCerts(data []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	rest := bytes.TrimSpace(data)
	for len(rest) > 0 {
		blk, r := pem.Decode(rest)
		if blk == nil || blk.Type != "CERTIFICATE" || len(blk.Headers) > 0 || !bytes.HasPrefix(rest, []byte("-----BEGIN")) {
			return nil, ErrMalformed
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return nil, ErrMalformed
		}
		out = append(out, c)
		rest = bytes.TrimSpace(r)
	}
	return out, nil
}

// parseKey decodes exactly one unencrypted private key block.
func parseKey(data []byte) (crypto.Signer, error) {
	trimmed := bytes.TrimSpace(data)
	blk, rest := pem.Decode(trimmed)
	if blk == nil || len(bytes.TrimSpace(rest)) > 0 || len(blk.Headers) > 0 || !bytes.HasPrefix(trimmed, []byte("-----BEGIN")) {
		return nil, ErrBadKey
	}
	var (
		k   any
		err error
	)
	switch blk.Type {
	case "PRIVATE KEY":
		k, err = x509.ParsePKCS8PrivateKey(blk.Bytes)
	case "RSA PRIVATE KEY":
		k, err = x509.ParsePKCS1PrivateKey(blk.Bytes)
	case "EC PRIVATE KEY":
		k, err = x509.ParseECPrivateKey(blk.Bytes)
	default:
		return nil, ErrBadKey
	}
	if err != nil {
		return nil, ErrBadKey
	}
	s, ok := k.(crypto.Signer)
	if !ok {
		return nil, ErrBadKey
	}
	return s, nil
}

func encodeCerts(cs ...*x509.Certificate) []byte {
	var buf bytes.Buffer
	for _, c := range cs {
		_ = pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
	}
	return buf.Bytes()
}

// Fingerprint is the lowercase hex SHA-256 of a certificate's DER.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// FullChain joins the leaf PEM and the chain PEM with single newlines and a
// final newline (certbot fullchain.pem).
func FullChain(cert, chain []byte) []byte {
	var buf bytes.Buffer
	for _, p := range [][]byte{cert, chain} {
		if t := bytes.TrimSpace(p); len(t) > 0 {
			buf.Write(t)
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes()
}
