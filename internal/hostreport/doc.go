// Package hostreport builds the host report projection IPAM consumes: a pure
// function of a host row and its latest snapshot carrying only identity,
// interfaces, primary addresses, virtualization, BMC LAN settings, hypervisor
// guests, update state, pending updates and truncation counters — never the
// full software list, users, services or any credential. Its digest is the
// change signal behind HostReportService watermarks.
//
// Security role: this is the only data inventory hands to IPAM through the
// cross-tenant-capable HostReportService, so what is (and is not) projected
// here bounds that disclosure. It is held to 100 % test coverage.
package hostreport
