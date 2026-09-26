package agentfacts

import (
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func TestDetectVirtualization(t *testing.T) {
	phys := VirtFacts{SMBIOSReadable: true, SysManufacturer: "Dell Inc.", SysProduct: "PowerEdge R640", BIOSVendor: "Dell Inc."}
	cases := []struct {
		name string
		f    VirtFacts
		want store.Virtualization
	}{
		{"dockerenv", VirtFacts{DockerEnv: true, SMBIOSReadable: true, SysManufacturer: "QEMU"}, store.Virtualization{Role: store.RoleContainer, Kind: "docker", Source: "dockerenv"}},
		{"containerenv", VirtFacts{ContainerEnv: true}, store.Virtualization{Role: store.RoleContainer, Kind: "podman", Source: "containerenv"}},
		{"environ lxc", VirtFacts{PID1Environ: "PATH=/bin\x00container=lxc\x00HOME=/"}, store.Virtualization{Role: store.RoleContainer, Kind: "lxc", Source: "environ"}},
		{"environ nspawn", VirtFacts{PID1Environ: "container=systemd-nspawn"}, store.Virtualization{Role: store.RoleContainer, Kind: "systemd-nspawn", Source: "environ"}},
		{"environ junk", VirtFacts{PID1Environ: "container=Evil Value!", SMBIOSReadable: true, SysManufacturer: "Dell"}, store.Virtualization{Role: store.RoleContainer, Kind: "", Source: "environ"}},
		{"cgroup kubepods", VirtFacts{PID1Cgroup: "12:pids:/kubepods/burstable/pod1/abc\n"}, store.Virtualization{Role: store.RoleContainer, Kind: "kubepods", Source: "cgroup"}},
		{"cgroup docker", VirtFacts{PID1Cgroup: "1:name=systemd:/docker/0123\n"}, store.Virtualization{Role: store.RoleContainer, Kind: "docker", Source: "cgroup"}},
		{"cgroup lxc", VirtFacts{PID1Cgroup: "0::/lxc.payload.101\n"}, store.Virtualization{Role: store.RoleContainer, Kind: "lxc", Source: "cgroup"}},
		{"wsl", VirtFacts{ProcVersion: "Linux version 5.15.90.1-microsoft-standard-WSL2"}, store.Virtualization{Role: store.RoleVM, Kind: "wsl", Source: "proc-version"}},
		{"xen domU", VirtFacts{HypervisorType: "xen", XenCapabilities: ""}, store.Virtualization{Role: store.RoleVM, Kind: "xen", Source: "hypervisor-type"}},
		{"xen dom0", VirtFacts{HypervisorType: "xen", XenCapabilities: "control_d", SMBIOSReadable: true, SysManufacturer: "HP", SysProduct: "ProLiant"}, store.Virtualization{Role: store.RolePhysical, Source: "dmi"}},
		{"qemu", VirtFacts{SMBIOSReadable: true, SysManufacturer: "QEMU", SysProduct: "Standard PC (i440FX + PIIX, 1996)", BIOSVendor: "SeaBIOS"}, store.Virtualization{Role: store.RoleVM, Kind: "kvm", Source: "dmi"}},
		{"seabios only", VirtFacts{SMBIOSReadable: true, BIOSVendor: "SeaBIOS"}, store.Virtualization{Role: store.RoleVM, Kind: "kvm", Source: "dmi"}},
		{"vmware", VirtFacts{SMBIOSReadable: true, SysManufacturer: "VMware, Inc.", SysProduct: "VMware Virtual Platform"}, store.Virtualization{Role: store.RoleVM, Kind: "vmware", Source: "dmi"}},
		{"hyperv", VirtFacts{SMBIOSReadable: true, SysManufacturer: "Microsoft Corporation", SysProduct: "Virtual Machine"}, store.Virtualization{Role: store.RoleVM, Kind: "hyperv", Source: "dmi"}},
		{"surface is physical", VirtFacts{SMBIOSReadable: true, SysManufacturer: "Microsoft Corporation", SysProduct: "Surface Pro 9"}, store.Virtualization{Role: store.RolePhysical, Source: "dmi"}},
		{"xen dmi", VirtFacts{SMBIOSReadable: true, SysManufacturer: "Xen", SysProduct: "HVM domU"}, store.Virtualization{Role: store.RoleVM, Kind: "xen", Source: "dmi"}},
		{"virtualbox", VirtFacts{SMBIOSReadable: true, SysManufacturer: "innotek GmbH", SysProduct: "VirtualBox"}, store.Virtualization{Role: store.RoleVM, Kind: "virtualbox", Source: "dmi"}},
		{"ec2", VirtFacts{SMBIOSReadable: true, SysManufacturer: "Amazon EC2", SysProduct: "m5.large"}, store.Virtualization{Role: store.RoleVM, Kind: "aws", Source: "dmi"}},
		{"gce", VirtFacts{SMBIOSReadable: true, SysManufacturer: "Google", SysProduct: "Google Compute Engine"}, store.Virtualization{Role: store.RoleVM, Kind: "gce", Source: "dmi"}},
		{"parallels", VirtFacts{SMBIOSReadable: true, SysManufacturer: "Parallels Software International Inc."}, store.Virtualization{Role: store.RoleVM, Kind: "parallels", Source: "dmi"}},
		{"digitalocean", VirtFacts{SMBIOSReadable: true, SysManufacturer: "DigitalOcean", SysProduct: "Droplet"}, store.Virtualization{Role: store.RoleVM, Kind: "kvm", Source: "dmi"}},
		{"cpuinfo only", VirtFacts{CPUHypervisorFlag: true}, store.Virtualization{Role: store.RoleVM, Kind: "", Source: "cpuinfo"}},
		{"physical", phys, store.Virtualization{Role: store.RolePhysical, Source: "dmi"}},
		// A KVM hypervisor host (kvm module loaded, no hypervisor cpu flag) stays physical.
		{"kvm host", VirtFacts{SMBIOSReadable: true, SysManufacturer: "Supermicro", SysProduct: "X11", KVMModuleLoaded: true}, store.Virtualization{Role: store.RolePhysical, Source: "dmi"}},
		{"unreadable", VirtFacts{}, store.Virtualization{Role: store.RoleUnknown}},
	}
	for _, c := range cases {
		if got := DetectVirtualization(c.f); got != c.want {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}

func TestCPUInfoHypervisor(t *testing.T) {
	if !CPUInfoHasHypervisor("processor : 0\nflags\t\t: fpu vme hypervisor lahf_lm\n") {
		t.Fatal("hypervisor flag missed")
	}
	if CPUInfoHasHypervisor("flags : fpu vme\nmodel name : hypervisor-lake\n") {
		t.Fatal("non-flag line matched")
	}
}
