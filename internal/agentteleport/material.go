package agentteleport

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
)

// Material bounds (spec 038).
const (
	MaxTokenBytes      = 256
	MaxCAPins          = 4
	MaxLabels          = 64
	MaxLabelValueBytes = 255
	maxHostnameBytes   = 253
)

var (
	versionRE  = regexp.MustCompile(`^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}$`)
	caPinRE    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	labelKeyRE = regexp.MustCompile(`^tangra\.io/[A-Za-z0-9._/-]{1,54}$`)
	dnsLabelRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	uuidRE     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// ErrInvalidMaterial wraps every validation failure.
var ErrInvalidMaterial = errors.New("agentteleport: invalid join material")

// Material is validated join material. Token is a byte slice so it can be
// zeroed (Wipe) once written.
type Material struct {
	ItemID    string
	Token     []byte
	ProxyAddr string
	CAPins    []string
	Version   string
	Labels    map[string]string
}

// FromPB copies the wire material and drops the token from the message.
func FromPB(m *invv1.TeleportJoinMaterial) Material {
	out := Material{ItemID: m.GetItemId(), Token: []byte(m.GetToken()), ProxyAddr: m.GetProxyAddr(),
		CAPins: append([]string(nil), m.GetCaPins()...), Version: m.GetTeleportVersion(), Labels: map[string]string{}}
	for k, v := range m.GetLabels() {
		out.Labels[k] = v
	}
	if m != nil {
		m.Token = ""
	}
	return out
}

// Wipe zeroes the token.
func (m *Material) Wipe() {
	clear(m.Token)
	m.Token = nil
}

// Validate checks every field strictly; the error never carries the token.
func (m Material) Validate() error {
	field := ""
	switch {
	case !validToken(m.Token):
		field = "token"
	case !versionRE.MatchString(m.Version):
		field = "teleport_version"
	case !ValidProxyAddr(m.ProxyAddr):
		field = "proxy_addr"
	case len(m.CAPins) < 1 || len(m.CAPins) > MaxCAPins:
		field = "ca_pins"
	case len(m.Labels) > MaxLabels:
		field = "labels"
	}
	for _, p := range m.CAPins {
		if field == "" && !caPinRE.MatchString(p) {
			field = "ca_pins"
		}
	}
	for k, v := range m.Labels {
		if field == "" && (!labelKeyRE.MatchString(k) || !validLabelValue(v)) {
			field = "labels"
		}
	}
	if field != "" {
		return fmt.Errorf("%w: invalid %s", ErrInvalidMaterial, field)
	}
	return nil
}

// validToken: 1..MaxTokenBytes printable ASCII bytes without whitespace.
func validToken(t []byte) bool {
	if len(t) < 1 || len(t) > MaxTokenBytes {
		return false
	}
	for _, b := range t {
		if b < 0x21 || b > 0x7e {
			return false
		}
	}
	return true
}

// validLabelValue: at most MaxLabelValueBytes of valid UTF-8, printable,
// without control characters.
func validLabelValue(v string) bool {
	if len(v) > MaxLabelValueBytes || !utf8.ValidString(v) {
		return false
	}
	for _, r := range v {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// ValidProxyAddr reports whether addr is host:port with a DNS name or an IP
// address that is not loopback, link-local, multicast or unspecified, and
// a port in 1..65535.
func ValidProxyAddr(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 || strconv.Itoa(p) != port {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Zone() == "" && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
			!ip.IsMulticast() && !ip.IsUnspecified()
	}
	return validDNSName(host)
}

// validDNSName reports a hostname of letters, digits and hyphens in dot
// separated labels, which is not localhost.
func validDNSName(h string) bool {
	h = strings.TrimSuffix(h, ".")
	lower := strings.ToLower(h)
	if h == "" || len(h) > maxHostnameBytes || lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return false
	}
	for _, l := range strings.Split(h, ".") {
		if !dnsLabelRE.MatchString(l) {
			return false
		}
	}
	return true
}
