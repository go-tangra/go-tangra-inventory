// Package agentfacts holds the pure parsing and decision rules behind the
// inventory agent's host report collection: netlink address/route messages
// and sysfs interface facts, Windows adapter mapping, virtualization
// detection, Proxmox guest configuration, package-manager output, reboot and
// automatic-update rules, and BMC LAN parameter decoding.
//
// Security role: every input here comes from the host (files, command
// output, kernel messages, IPMI responses) and is treated as untrusted —
// parsers are bounded, never panic, and are fuzzed. No function in this
// package reads a credential or touches the operating system; the OS glue
// lives in internal/collector.
package agentfacts
