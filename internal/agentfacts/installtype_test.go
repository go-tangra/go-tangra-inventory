package agentfacts

import "testing"

func TestDetectInstallType(t *testing.T) {
	cases := []struct {
		name string
		in   InstallInputs
		want string
	}{
		{"windows", InstallInputs{GOOS: "windows", Executable: `C:\Program Files\agent\inventory-agent.exe`}, "binary"},
		{"deb", InstallInputs{GOOS: "linux", Executable: "/usr/bin/inventory-agent", DpkgStatus: "install ok installed", DpkgOK: true}, "deb"},
		{"deb half-configured", InstallInputs{GOOS: "linux", Executable: "/usr/bin/inventory-agent", DpkgStatus: "install ok half-configured", DpkgOK: true}, "binary"},
		{"deb removed", InstallInputs{GOOS: "linux", Executable: "/usr/bin/inventory-agent", DpkgStatus: "deinstall ok config-files", DpkgOK: true}, "binary"},
		{"rpm", InstallInputs{GOOS: "linux", Executable: "/usr/bin/inventory-agent", RPMQuery: "tangra-inventory-agent-4.4.0-1.x86_64\n", RPMOK: true}, "rpm"},
		{"rpm not installed", InstallInputs{GOOS: "linux", Executable: "/usr/bin/inventory-agent", RPMQuery: "package tangra-inventory-agent is not installed", RPMOK: false}, "binary"},
		{"rpm other output", InstallInputs{GOOS: "linux", Executable: "/usr/bin/inventory-agent", RPMQuery: "something", RPMOK: true}, "binary"},
		{"package elsewhere", InstallInputs{GOOS: "linux", Executable: "/opt/agent/inventory-agent", DpkgStatus: "install ok installed", DpkgOK: true}, "binary"},
		{"no package manager", InstallInputs{GOOS: "linux", Executable: "/usr/bin/inventory-agent"}, "binary"},
		{"darwin", InstallInputs{GOOS: "darwin", Executable: "/usr/local/bin/inventory-agent"}, "binary"},
	}
	for _, c := range cases {
		if got := DetectInstallType(c.in); got != c.want {
			t.Errorf("%s = %q, want %q", c.name, got, c.want)
		}
	}
	if PackagedExecutable != "/usr/bin/inventory-agent" || PackageName != "tangra-inventory-agent" {
		t.Fatal("package constants")
	}
}
