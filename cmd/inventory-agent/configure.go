package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
)

// Paths installed by the tangra-inventory-agent package (packaging/nfpm.yaml),
// which also creates /etc/inventory-agent (0750) and /var/lib/inventory-agent
// (0700): configure writes files, never directories or their modes.
const (
	packagedConfig  = "/etc/inventory-agent/agent.yaml"
	packagedKeyFile = "/etc/inventory-agent/auto-enroll.key"
	systemdUnitName = "inventory-agent.service"
)

// configureDeps are the environment of the configure command (tests replace them).
type configureDeps struct {
	isAdmin   func() bool
	systemd   func() bool                // a running systemd manages the unit
	systemctl func(args ...string) error // runs systemctl
}

func defaultConfigureDeps() configureDeps {
	return configureDeps{
		isAdmin: isAdmin,
		systemd: func() bool {
			fi, err := os.Stat("/run/systemd/system")
			return runtime.GOOS == "linux" && err == nil && fi.IsDir()
		},
		systemctl: func(args ...string) error {
			out, err := exec.Command("systemctl", args...).CombinedOutput() // #nosec G204 -- fixed verbs and unit name
			if err != nil {
				return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
			}
			return nil
		},
	}
}

// runConfigure is `inventory-agent configure`: it writes the given settings
// into the agent config (default: the packaged agent.yaml), keeping its
// comments and other settings, stores the auto-enrollment secret read from
// stdin (-auto-enroll-secret-stdin) with mode 0600, checks the result like
// the daemon does and (re)starts the systemd service. Nothing is written
// when the result would not be valid. Exit 0 configured, 1 error.
func runConfigure(args []string, stdin io.Reader, out io.Writer, deps configureDeps) int {
	fs := flag.NewFlagSet("configure", flag.ContinueOnError)
	fs.SetOutput(out)
	configPath := fs.String("config", packagedConfig, "agent config YAML to update")
	ingest := fs.String("ingest", "", "ingest endpoint host:port")
	token := fs.String("token", "", "path to an enrollment token file")
	caFile := fs.String("ca-file", "", "PEM CA bundle that signs the ingest server certificate")
	serverName := fs.String("server-name", "", "name to verify in the ingest server certificate")
	autoKeyID := fs.String("auto-enroll-key-id", "", "auto-enrollment key id ak_…")
	autoKeyFile := fs.String("auto-enroll-key-file", "", "path of the auto-enrollment key secret (default "+packagedKeyFile+" with -auto-enroll-key-id)")
	secretStdin := fs.Bool("auto-enroll-secret-stdin", false, "read the key secret aks_… from stdin and store it in the key file (mode 0600)")
	noStart := fs.Bool("no-start", false, "only write the configuration; do not enable and restart the service")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(out, "error: configure: "+format+"\n", a...)
		return exitError
	}
	if fs.NArg() > 0 {
		return fail("unexpected argument %q", fs.Arg(0))
	}
	if !deps.isAdmin() {
		return fail("must run as root (Administrator on Windows)")
	}
	if *autoKeyID != "" && *autoKeyFile == "" {
		*autoKeyFile = packagedKeyFile
	}
	if *secretStdin && *autoKeyFile == "" {
		return fail("-auto-enroll-secret-stdin needs -auto-enroll-key-id or -auto-enroll-key-file")
	}

	raw, err := os.ReadFile(*configPath) // #nosec G304 -- operator-supplied config path
	if err != nil {
		return fail("%v (install the tangra-inventory-agent package, or pass -config)", err)
	}
	set := []struct {
		path  []string
		value string
	}{
		{[]string{"ingest_endpoint"}, *ingest},
		{[]string{"token_file"}, *token},
		{[]string{"ca_file"}, *caFile},
		{[]string{"server_name"}, *serverName},
		{[]string{"auto_enroll", "key_id"}, *autoKeyID},
		{[]string{"auto_enroll", "key_file"}, *autoKeyFile},
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fail("%s: %v", *configPath, err)
	}
	if doc.Kind == 0 { // empty file
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return fail("%s: not a YAML mapping", *configPath)
	}
	for _, s := range set {
		if s.value != "" {
			setScalar(doc.Content[0], s.path, s.value)
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fail("%v", err)
	}
	if err := enc.Close(); err != nil {
		return fail("%v", err)
	}

	var secret []byte
	if *secretStdin {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fail("read secret: %v", err)
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "aks_") || strings.ContainsAny(line, " \t") {
			return fail("the secret read from stdin must look like aks_…")
		}
		secret = []byte(line + "\n")
	}

	// Validate before writing anything, the way the daemon will read it.
	next, err := writeTemp(*configPath, buf.Bytes(), 0o600)
	if err != nil {
		return fail("%v", err)
	}
	defer os.Remove(next)
	cfg, err := config.LoadAgent(next)
	if err == nil {
		err = cfg.Validate()
	}
	if err != nil {
		return fail("the resulting configuration is not valid: %v", err)
	}
	if secret == nil && cfg.AutoEnroll.Configured() {
		if _, err := os.Stat(cfg.AutoEnroll.KeyFile); err != nil {
			return fail("key file: %v (store the secret with -auto-enroll-secret-stdin)", err)
		}
	}

	if secret != nil {
		if err := writeFile(*autoKeyFile, secret, 0o600); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(out, "configure: key secret stored in %s (mode 0600)\n", *autoKeyFile)
	}
	mode := os.FileMode(0o640)
	if fi, err := os.Stat(*configPath); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.Chmod(next, mode); err != nil {
		return fail("%v", err)
	}
	if err := os.Rename(next, *configPath); err != nil {
		return fail("%v", err)
	}
	fmt.Fprintf(out, "configure: %s updated\n", *configPath)

	switch {
	case *noStart:
		fmt.Fprintln(out, "configure: start the agent with: systemctl restart inventory-agent")
	case !deps.systemd():
		fmt.Fprintln(out, "configure: no systemd; start the agent with: inventory-agent -daemon -config "+*configPath)
	default:
		if err := deps.systemctl("enable", systemdUnitName); err != nil {
			return fail("%v", err)
		}
		if err := deps.systemctl("restart", systemdUnitName); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintln(out, "configure: inventory-agent service enabled and (re)started; follow it with: journalctl -u inventory-agent -f")
	}
	return exitOK
}

// setScalar sets the string at path (nested mapping keys) in m, replacing an
// existing value in place (its line comment stays) or appending the keys.
func setScalar(m *yaml.Node, path []string, value string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != path[0] {
			continue
		}
		v := m.Content[i+1]
		if len(path) == 1 {
			*v = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, LineComment: v.LineComment}
			return
		}
		if v.Kind != yaml.MappingNode {
			*v = yaml.Node{Kind: yaml.MappingNode, LineComment: v.LineComment}
		}
		setScalar(v, path[1:], value)
		return
	}
	key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: path[0]}
	val := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
	if len(path) > 1 {
		val = &yaml.Node{Kind: yaml.MappingNode}
		setScalar(val, path[1:], value)
	}
	at := len(m.Content)
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == insertAfter[path[0]] {
			at = i + 2
		}
	}
	m.Content = append(m.Content[:at], append([]*yaml.Node{key, val}, m.Content[at:]...)...)
}

// insertAfter places a key missing from the config next to its neighbour in
// the packaged agent.yaml instead of after the last section.
var insertAfter = map[string]string{
	"token_file":  "ingest_endpoint",
	"auto_enroll": "state_file",
	"ca_file":     "ingest_endpoint",
	"server_name": "ingest_endpoint",
}

// writeTemp writes data to a new file next to path and returns its name.
func writeTemp(path string, data []byte, mode os.FileMode) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	err = f.Chmod(mode)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

// writeFile replaces path atomically with data in the given mode.
func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := writeTemp(path, data, mode)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
