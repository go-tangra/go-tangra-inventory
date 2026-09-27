package agentfacts

import "strings"

// Package facts of the Linux agent (packaging/nfpm.yaml).
const (
	PackageName        = "tangra-inventory-agent"
	PackagedExecutable = "/usr/bin/inventory-agent"
)

// InstallInputs are the observations install type detection decides on
// (feature 023, research D10): the running OS, the resolved path of the
// agent executable and the results of
// `dpkg-query -W -f='${Status}' tangra-inventory-agent` and
// `rpm -q tangra-inventory-agent` (OK = exit status 0).
type InstallInputs struct {
	GOOS       string
	Executable string
	DpkgStatus string
	DpkgOK     bool
	RPMQuery   string
	RPMOK      bool
}

// DetectInstallType returns deb or rpm when the running executable is the
// one the installed package owns, binary otherwise (Windows agents and
// manual installs replace the binary in place).
func DetectInstallType(in InstallInputs) string {
	if in.GOOS != "linux" || in.Executable != PackagedExecutable {
		return "binary"
	}
	if in.DpkgOK && strings.TrimSpace(in.DpkgStatus) == "install ok installed" {
		return "deb"
	}
	if in.RPMOK && strings.HasPrefix(strings.TrimSpace(in.RPMQuery), PackageName+"-") {
		return "rpm"
	}
	return "binary"
}
