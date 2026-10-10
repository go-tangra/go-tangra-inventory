//go:build !linux

package agentmodules

// NewOS reports ErrUnsupported: module delivery is Linux-only.
func NewOS(Config, Deps) (*Writer, error) { return nil, ErrUnsupported }
