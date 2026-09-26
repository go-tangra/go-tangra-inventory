//go:build linux

package collector

import (
	"io"
	"os"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentfacts"
)

// maxFactBytes bounds each /proc or /sys fact file read.
const maxFactBytes = 256 << 10

func readFact(path string) string {
	f, err := os.Open(path) // #nosec G304 -- fixed /proc and /sys paths
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	b, _ := io.ReadAll(io.LimitReader(f, maxFactBytes))
	return string(b)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// platformVirtFacts reads the Linux container/hypervisor markers.
func platformVirtFacts(f *agentfacts.VirtFacts) {
	f.DockerEnv = exists("/.dockerenv")
	f.ContainerEnv = exists("/run/.containerenv")
	f.PID1Environ = readFact("/proc/1/environ")
	f.PID1Cgroup = readFact("/proc/1/cgroup")
	f.ProcVersion = readFact("/proc/version")
	f.HypervisorType = readFact("/sys/hypervisor/type")
	f.XenCapabilities = readFact("/proc/xen/capabilities")
	f.CPUHypervisorFlag = agentfacts.CPUInfoHasHypervisor(readFact("/proc/cpuinfo"))
	f.KVMModuleLoaded = exists("/sys/module/kvm")
}
