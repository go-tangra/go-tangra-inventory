//go:build !linux

package agentcerts

// NewOS reports ErrUnsupported: the certificate store is Linux-only.
func NewOS(Config, Deps) (*Store, error) { return nil, ErrUnsupported }
