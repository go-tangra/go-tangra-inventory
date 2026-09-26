package agentfacts

import (
	"regexp"
	"strings"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// tokenRE is the shape of a virtualization kind token.
var tokenRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)

// VirtFacts are the inputs of DetectVirtualization, read by the collector
// (Linux files; SMBIOS on both platforms).
type VirtFacts struct {
	DockerEnv         bool   // /.dockerenv exists
	ContainerEnv      bool   // /run/.containerenv exists
	PID1Environ       string // /proc/1/environ (NUL-separated)
	PID1Cgroup        string // /proc/1/cgroup
	ProcVersion       string // /proc/version
	HypervisorType    string // /sys/hypervisor/type
	XenCapabilities   string // /proc/xen/capabilities
	CPUHypervisorFlag bool   // "hypervisor" in the /proc/cpuinfo flags
	KVMModuleLoaded   bool   // informational only: a KVM host is still physical
	SMBIOSReadable    bool
	SysManufacturer   string
	SysProduct        string
	BIOSVendor        string
}

// DetectVirtualization applies the first matching rule: container markers,
// WSL, Xen domU, SMBIOS vendor strings, the cpuinfo hypervisor flag, and
// finally physical (SMBIOS readable) or unknown.
func DetectVirtualization(f VirtFacts) store.Virtualization {
	switch {
	case f.DockerEnv:
		return container("docker", "dockerenv")
	case f.ContainerEnv:
		return container("podman", "containerenv")
	}
	for _, kv := range strings.Split(f.PID1Environ, "\x00") {
		if v, ok := strings.CutPrefix(kv, "container="); ok {
			if !tokenRE.MatchString(v) {
				v = ""
			}
			return container(v, "environ")
		}
	}
	for _, k := range []string{"kubepods", "docker", "lxc"} {
		if strings.Contains(f.PID1Cgroup, "/"+k) {
			return container(k, "cgroup")
		}
	}
	if strings.Contains(strings.ToLower(f.ProcVersion), "microsoft") {
		return store.Virtualization{Role: store.RoleVM, Kind: "wsl", Source: "proc-version"}
	}
	if strings.TrimSpace(f.HypervisorType) == "xen" && !strings.Contains(f.XenCapabilities, "control_d") {
		return store.Virtualization{Role: store.RoleVM, Kind: "xen", Source: "hypervisor-type"}
	}
	if f.SMBIOSReadable {
		if kind := dmiKind(f.SysManufacturer, f.SysProduct, f.BIOSVendor); kind != "" {
			return store.Virtualization{Role: store.RoleVM, Kind: kind, Source: "dmi"}
		}
	}
	if f.CPUHypervisorFlag {
		return store.Virtualization{Role: store.RoleVM, Source: "cpuinfo"}
	}
	if f.SMBIOSReadable {
		return store.Virtualization{Role: store.RolePhysical, Source: "dmi"}
	}
	return store.Virtualization{Role: store.RoleUnknown}
}

func container(kind, source string) store.Virtualization {
	return store.Virtualization{Role: store.RoleContainer, Kind: kind, Source: source}
}

// dmiKind maps SMBIOS system manufacturer/product and BIOS vendor strings of
// known hypervisors and clouds to a kind ("" = not a known virtual platform).
func dmiKind(manufacturer, product, biosVendor string) string {
	m, p, b := strings.ToLower(manufacturer), strings.ToLower(product), strings.ToLower(biosVendor)
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(m, s) || strings.Contains(p, s) || strings.Contains(b, s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("qemu", "kvm", "bochs", "seabios", "digitalocean"):
		return "kvm"
	case has("vmware"):
		return "vmware"
	case strings.Contains(m, "microsoft") && strings.Contains(p, "virtual machine"):
		return "hyperv"
	case has("xen"):
		return "xen"
	case has("innotek", "virtualbox"):
		return "virtualbox"
	case strings.Contains(m, "amazon ec2"):
		return "aws"
	case strings.Contains(m, "google"):
		return "gce"
	case has("parallels"):
		return "parallels"
	}
	return ""
}

// CPUInfoHasHypervisor reports whether a "flags" line of /proc/cpuinfo lists
// the hypervisor flag (set by the CPU for guests only).
func CPUInfoHasHypervisor(cpuinfo string) bool {
	for _, line := range strings.Split(cpuinfo, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(k) != "flags" {
			continue
		}
		for _, fl := range strings.Fields(v) {
			if fl == "hypervisor" {
				return true
			}
		}
	}
	return false
}
