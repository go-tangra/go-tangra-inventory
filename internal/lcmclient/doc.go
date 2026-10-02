// Package lcmclient downloads an issued certificate (certificate, chain and,
// when requested, the retained private key) from lcm over the module gRPC
// channel (SPIFFE mTLS, lcm.v1.Certificates/Download, allowed for
// svc/inventory by lcm's inventory-download policy rule) for feature 033.
//
// Security role: the inventory calls it only while an agent fetches its own
// delivery item; the result is relayed and dropped, never stored. Errors are
// mapped to closed sentinels and never carry material.
package lcmclient
