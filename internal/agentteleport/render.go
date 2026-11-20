package agentteleport

import (
	"bytes"

	"gopkg.in/yaml.v3"
)

// Fixed locations (relative to the filesystem root) and settings. None is
// ever taken from the server.
const (
	ConfigPath  = "etc/teleport.yaml"
	UnitPath    = "etc/systemd/system/teleport.service"
	BinaryPath  = "usr/local/bin/teleport"
	DataDir     = "/var/lib/teleport"
	HostUUIDRel = "var/lib/teleport/host_uuid"
	// UnitName is the systemd unit the agent installs.
	UnitName = "teleport.service"
	// DiagAddr is the node's diagnostic listener (loopback only). Its
	// /readyz answers 200 once the node has joined and connected to the
	// cluster; the host uuid file alone is written before that.
	DiagAddr = "127.0.0.1:3434"
	// ReadyURL is the readiness probe on DiagAddr.
	ReadyURL = "http://" + DiagAddr + "/readyz"
)

// marker starts every file the agent writes, so a configuration left by a
// failed join of the agent (token removed) can be told from an operator's.
const marker = "# Written by the tangra inventory-agent (Teleport join, feature 038). Do not edit.\n"

// packagedUnits are unit files of a Teleport installed by a package: their
// presence means Teleport is managed by someone else.
var packagedUnits = []string{"usr/lib/systemd/system/teleport.service", "lib/systemd/system/teleport.service"}

type joinParams struct {
	Method    string `yaml:"method"`
	TokenName string `yaml:"token_name"`
}

type teleportSection struct {
	NodeName    string     `yaml:"nodename"`
	DataDir     string     `yaml:"data_dir"`
	JoinParams  joinParams `yaml:"join_params"`
	ProxyServer string     `yaml:"proxy_server"`
	CAPin       []string   `yaml:"ca_pin"`
	DiagAddr    string     `yaml:"diag_addr"`
}

type enabledOnly struct {
	Enabled string `yaml:"enabled"`
}

type sshSection struct {
	Enabled string            `yaml:"enabled"`
	Labels  map[string]string `yaml:"labels,omitempty"`
}

// fileConfig is /etc/teleport.yaml (config version v3, SSH node only).
type fileConfig struct {
	Version      string          `yaml:"version"`
	Teleport     teleportSection `yaml:"teleport"`
	AuthService  enabledOnly     `yaml:"auth_service"`
	ProxyService enabledOnly     `yaml:"proxy_service"`
	SSHService   sshSection      `yaml:"ssh_service"`
}

// renderConfig marshals the node configuration (token "" removes it).
// Every server value goes through the YAML encoder, never into a template;
// encoding these plain types cannot fail.
func renderConfig(nodename string, m Material, token string) []byte {
	c := fileConfig{Version: "v3",
		Teleport: teleportSection{NodeName: nodename, DataDir: DataDir, JoinParams: joinParams{Method: "token", TokenName: token},
			ProxyServer: m.ProxyAddr, CAPin: m.CAPins, DiagAddr: DiagAddr},
		AuthService: enabledOnly{Enabled: "no"}, ProxyService: enabledOnly{Enabled: "no"},
		SSHService: sshSection{Enabled: "yes", Labels: m.Labels}}
	var b bytes.Buffer
	b.WriteString(marker)
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	_ = enc.Encode(c)
	_ = enc.Close()
	return b.Bytes()
}

// ours reports whether raw is a configuration the agent wrote whose token
// was removed after a failed join (it may be replaced by a new join).
func ours(raw []byte) bool {
	if !bytes.HasPrefix(raw, []byte(marker)) {
		return false
	}
	var c fileConfig
	return yaml.Unmarshal(raw, &c) == nil && c.Teleport.JoinParams.TokenName == ""
}

// unitFile is the systemd unit (fixed content).
const unitFile = marker + `[Unit]
Description=Teleport SSH node (joined by the tangra inventory-agent)
Documentation=https://goteleport.com/docs/
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/teleport start --config=/etc/teleport.yaml
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=5
LimitNOFILE=524288

[Install]
WantedBy=multi-user.target
`
