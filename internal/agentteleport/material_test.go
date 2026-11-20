package agentteleport

import (
	"errors"
	"strings"
	"testing"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
)

func TestFromPB(t *testing.T) {
	pb := &invv1.TeleportJoinMaterial{ItemId: "i", Token: testToken, ProxyAddr: "p:1", CaPins: []string{testPin},
		TeleportVersion: testVersion, Labels: map[string]string{"tangra.io/a": "b"}}
	m := FromPB(pb)
	if pb.GetToken() != "" || string(m.Token) != testToken || m.ItemID != "i" || m.ProxyAddr != "p:1" || m.CAPins[0] != testPin ||
		m.Version != testVersion || m.Labels["tangra.io/a"] != "b" {
		t.Fatalf("material = %+v", m)
	}
	m.Wipe()
	if m.Token != nil {
		t.Fatal("not wiped")
	}
	if m := FromPB(nil); m.Token == nil && len(m.Labels) != 0 {
		t.Fatal("nil material")
	}
}

func TestValidate(t *testing.T) {
	if err := material().Validate(); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("a", 256)
	labels := map[string]string{}
	for i := 0; i < 65; i++ {
		labels["tangra.io/k"+strings.Repeat("x", i)] = "v"
	}
	cases := map[string]struct {
		mut   func(m *Material)
		field string
	}{
		"empty token":     {func(m *Material) { m.Token = nil }, "token"},
		"long token":      {func(m *Material) { m.Token = []byte(long + "a") }, "token"},
		"token space":     {func(m *Material) { m.Token = []byte("a b") }, "token"},
		"token ctl":       {func(m *Material) { m.Token = []byte("a\nb") }, "token"},
		"token high":      {func(m *Material) { m.Token = []byte("a\x7f") }, "token"},
		"version":         {func(m *Material) { m.Version = "v17.0.0" }, "teleport_version"},
		"version long":    {func(m *Material) { m.Version = "12345.0.0" }, "teleport_version"},
		"proxy":           {func(m *Material) { m.ProxyAddr = "teleport.example.com" }, "proxy_addr"},
		"no pins":         {func(m *Material) { m.CAPins = nil }, "ca_pins"},
		"many pins":       {func(m *Material) { m.CAPins = []string{testPin, testPin, testPin, testPin, testPin} }, "ca_pins"},
		"bad pin":         {func(m *Material) { m.CAPins = []string{testPin, "sha256:AB"} }, "ca_pins"},
		"many labels":     {func(m *Material) { m.Labels = labels }, "labels"},
		"label key":       {func(m *Material) { m.Labels = map[string]string{"env": "prod"} }, "labels"},
		"label key long":  {func(m *Material) { m.Labels = map[string]string{"tangra.io/" + strings.Repeat("k", 55): "v"} }, "labels"},
		"label value":     {func(m *Material) { m.Labels = map[string]string{"tangra.io/a": "x\ny"} }, "labels"},
		"label value len": {func(m *Material) { m.Labels = map[string]string{"tangra.io/a": long} }, "labels"},
		"label utf8":      {func(m *Material) { m.Labels = map[string]string{"tangra.io/a": "\xff"} }, "labels"},
	}
	for name, c := range cases {
		m := material()
		c.mut(&m)
		err := m.Validate()
		if !errors.Is(err, ErrInvalidMaterial) || !strings.HasSuffix(err.Error(), "invalid "+c.field) {
			t.Errorf("%s: %v", name, err)
		}
		if err != nil && strings.Contains(err.Error(), testToken) {
			t.Errorf("%s: token in error", name)
		}
	}
	m := material()
	m.Token = []byte(long)
	m.Labels = map[string]string{"tangra.io/a.b/c-d_e": strings.Repeat("ü", 127), "tangra.io/x": ""}
	m.CAPins = []string{testPin, testPin, testPin, testPin}
	if err := m.Validate(); err != nil {
		t.Fatalf("bounds: %v", err)
	}
}

func TestValidProxyAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"teleport.example.com:443": true, "teleport.example.com.:3080": true, "proxy:443": true, "203.0.113.7:443": true,
		"[2001:db8::1]:443": true, "10.0.0.1:65535": true,
		"teleport.example.com": false, "teleport.example.com:0": false, "teleport.example.com:65536": false,
		"teleport.example.com:080": false, "teleport.example.com:https": false, ":443": false,
		"127.0.0.1:443": false, "[::1]:443": false, "169.254.1.1:443": false, "[fe80::1]:443": false, "[fe80::1%eth0]:443": false,
		"224.0.0.1:443": false, "0.0.0.0:443": false, "[ff02::1]:443": false, "localhost:443": false, "a.LOCALHOST:443": false,
		"-bad.example.com:443": false, "bad_name.example.com:443": false, "a..b:443": false,
		strings.Repeat("a.", 127) + "aa:443": false, "user@host:443": false,
	} {
		if got := ValidProxyAddr(addr); got != want {
			t.Errorf("%q = %v", addr, got)
		}
	}
}

func TestOurs(t *testing.T) {
	if ours([]byte("version: v3\n")) || ours([]byte(marker+"::: [")) || ours(renderConfig("n", material(), "t")) || !ours(renderConfig("n", material(), "")) {
		t.Fatal("ours")
	}
}
